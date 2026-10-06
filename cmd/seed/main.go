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

type item struct {
	name, category, desc string
	cents                int64
	stock                int
}

var items = []item{
	{"Trail Running Shoes", "Footwear", "Lightweight, grippy shoes for road and trail.", 8900, 25},
	{"Everyday Canvas Sneakers", "Footwear", "Classic low-top sneakers that go with everything.", 5400, 40},
	{"Insulated Water Bottle", "Outdoor", "Keeps drinks cold for 24 hours or hot for 12.", 2800, 60},
	{"Packable Rain Jacket", "Outdoor", "Waterproof shell that folds into its own pocket.", 7500, 18},
	{"Merino Wool Socks", "Apparel", "Soft, breathable and naturally odour resistant.", 1800, 120},
	{"Organic Cotton Tee", "Apparel", "Heavyweight tee with a relaxed fit.", 2600, 80},
	{"Compact Daypack 20L", "Outdoor", "Everyday backpack with a padded laptop sleeve.", 6400, 3},
	{"Limited Edition Cap", "Apparel", "Numbered run. Once it is gone, it is gone.", 2200, 0},
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
			CategoryID: &c.ID, Name: it.name, Description: it.desc, PriceCents: it.cents, ImageURL: image, Active: true,
		})
		if errors.Is(err, catalog.ErrSlugTaken) {
			existing, err := cat.BySlug(ctx, slugify(it.name))
			if err != nil {
				return err
			}
			if existing.ImageURL == "" {
				if err := cat.UpdateProduct(ctx, existing.ID, catalog.ProductInput{
					CategoryID: existing.CategoryID, Slug: existing.Slug, Name: existing.Name, Description: existing.Description,
					PriceCents: existing.PriceCents, ImageURL: image, Active: existing.Active,
				}); err != nil {
					return err
				}
				updated++
			}
			continue
		}
		if err != nil {
			return err
		}
		if it.stock > 0 {
			if err := inv.AddLot(ctx, p.ID, "seed", it.stock); err != nil {
				return err
			}
		}
		created++
	}
	fmt.Printf("seeded %d new products, added images to %d existing\n", created, updated)
	return nil
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
}
