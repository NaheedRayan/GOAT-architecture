// Command seed loads a small demo catalog with stock and product images. It is
// safe to re-run: existing products only get their image filled in.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"

	"github.com/NaheedRayan/goat-architecture/internal/catalog"
	"github.com/NaheedRayan/goat-architecture/internal/inventory"
	"github.com/NaheedRayan/goat-architecture/internal/platform/config"
	"github.com/NaheedRayan/goat-architecture/internal/platform/db"
)

type variant struct {
	label string
	cents int64 // 0 = use the product price
	stock int
}

type item struct {
	name, category, desc string
	cents                int64
	stock                int // for single-form products
	option               string
	variants             []variant
}

func sizes(labels string, stock int) []variant {
	var out []variant
	for _, l := range strings.Fields(labels) {
		out = append(out, variant{label: l, stock: stock})
	}
	return out
}

var items = []item{
	{name: "Trail Running Shoes", category: "Footwear", desc: "Lightweight, grippy shoes for road and trail.", cents: 8900, option: "Size", variants: sizes("40 41 42 43 44", 6)},
	{name: "Everyday Canvas Sneakers", category: "Footwear", desc: "Classic low-top sneakers that go with everything.", cents: 5400, option: "Size", variants: sizes("40 41 42 43", 10)},
	{name: "Insulated Water Bottle", category: "Outdoor", desc: "Keeps drinks cold for 24 hours or hot for 12.", cents: 2800, option: "Colour",
		variants: []variant{{"Ocean Blue", 0, 30}, {"Matte Black", 0, 30}}},
	{name: "Packable Rain Jacket", category: "Outdoor", desc: "Waterproof shell that folds into its own pocket.", cents: 7500, option: "Size",
		variants: []variant{{"S", 0, 4}, {"M", 0, 6}, {"L", 0, 5}, {"XL", 8000, 3}}},
	{name: "Merino Wool Socks", category: "Apparel", desc: "Soft, breathable and naturally odour resistant.", cents: 1800, option: "Size", variants: sizes("S M L", 40)},
	{name: "Organic Cotton Tee", category: "Apparel", desc: "Heavyweight tee with a relaxed fit.", cents: 2600, option: "Size", variants: sizes("S M L XL", 20)},
	{name: "Compact Daypack 20L", category: "Outdoor", desc: "Everyday backpack with a padded laptop sleeve.", cents: 6400, stock: 3},
	{name: "Limited Edition Cap", category: "Apparel", desc: "Numbered run. Once it is gone, it is gone.", cents: 2200, stock: 0},
}

func main() {
	if err := run(); err != nil {
		slog.Error("seed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	tx := db.NewTxManager(pool)
	cat := catalog.New(catalog.Options{Pool: pool, Currency: cfg.Currency}).API()
	inv := inventory.New(inventory.Options{Pool: pool, Tx: tx, ReservationTTL: cfg.ReservationTTL, Log: slog.Default()}).API()

	cats := map[string]catalog.Category{}
	existing, err := cat.Categories(ctx)
	if err != nil {
		return err
	}
	for _, c := range existing {
		cats[c.Name] = c
	}
	created, updated := 0, 0
	for _, it := range items {
		c, ok := cats[it.category]
		if !ok {
			if c, err = cat.CreateCategory(ctx, it.category); err != nil {
				return err
			}
			cats[it.category] = c
		}
		image := "/static/products/" + slugify(it.name) + ".svg"
		p, err := cat.CreateProduct(ctx, catalog.ProductInput{
			CategoryID: &c.ID, Name: it.name, Description: it.desc, PriceCents: it.cents, ImageURL: image,
			OptionName: it.option, Active: true,
		})
		isNew := err == nil
		if errors.Is(err, catalog.ErrSlugTaken) {
			existing, err := cat.BySlug(ctx, slugify(it.name))
			if err != nil {
				return err
			}
			p = existing
			if existing.ImageURL == "" || existing.OptionName != it.option {
				if err := cat.UpdateProduct(ctx, existing.ID, catalog.ProductInput{
					CategoryID: existing.CategoryID, Slug: existing.Slug, Name: existing.Name, Description: existing.Description,
					PriceCents: existing.PriceCents, ImageURL: image, OptionName: it.option, Active: existing.Active,
				}); err != nil {
					return err
				}
				updated++
			}
		} else if err != nil {
			return err
		}
		if isNew {
			created++
		}
		// Variants are added only to products that still have just their single default form.
		if len(it.variants) > 0 && len(p.Variants) == 1 && p.Variants[0].Label == "" {
			for _, v := range it.variants {
				in := catalog.VariantInput{Label: v.label, Active: true}
				if v.cents > 0 {
					c := v.cents
					in.PriceCents = &c
				}
				nv, err := cat.AddVariant(ctx, p.ID, in)
				if err != nil {
					return err
				}
				if v.stock > 0 {
					if err := inv.AddLot(ctx, nv.ID, "seed", v.stock); err != nil {
						return err
					}
				}
			}
			// The unnamed default form is superseded by the sizes/colours above.
			def := p.Variants[0]
			if err := cat.UpdateVariant(ctx, def.ID, catalog.VariantInput{SKU: def.SKU, Label: "Default", PriceCents: def.PriceCents, Active: false}); err != nil {
				return err
			}
			updated++
		} else if isNew && it.stock > 0 {
			if err := inv.AddLot(ctx, p.Variants[0].ID, "seed", it.stock); err != nil {
				return err
			}
		}
	}
	fmt.Printf("seeded %d new products, added images to %d existing\n", created, updated)
	return nil
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
}
