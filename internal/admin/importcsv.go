package admin

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/NaheedRayan/goat-architecture/internal/catalog"
	"github.com/NaheedRayan/goat-architecture/internal/platform/httpx"
	"github.com/NaheedRayan/goat-architecture/internal/platform/money"
)

const (
	maxImportRows  = 1000
	maxImportBytes = 2 << 20
	maxImportError = 100
)

// ImportResult reports what a CSV import did.
type ImportResult struct {
	Products int
	Variants int
	Skipped  int
	Errors   []ImportError
	Stopped  bool // the file had more rows than the limit
}

type ImportError struct {
	Line int
	Msg  string
}

func (m *Module) importForm(w http.ResponseWriter, r *http.Request) {
	httpx.Render(w, r, http.StatusOK, ImportPage(nil, ""))
}

const sampleCSV = `name,slug,category,price,description,image_url,active,sku,stock,option_name,variant,variant_price
Canvas Tote,canvas-tote,Bags,18.00,Sturdy cotton tote bag,,1,TOTE-1,40,,,
Rain Jacket,rain-jacket,Outerwear,75.00,Waterproof shell that packs away,,1,,,Size,,
,rain-jacket,,,,,,JACKET-S,5,,S,
,rain-jacket,,,,,,JACKET-M,8,,M,
,rain-jacket,,,,,,JACKET-XL,3,,XL,80.00
`

func (m *Module) importSample(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="products-sample.csv"`)
	_, _ = io.WriteString(w, sampleCSV)
}

func (m *Module) importProducts(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(maxImportBytes); err != nil {
		httpx.Render(w, r, http.StatusRequestEntityTooLarge, ImportPage(nil, "That file is too large (2 MB max) or the upload was interrupted."))
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		httpx.Render(w, r, http.StatusUnprocessableEntity, ImportPage(nil, "Choose a CSV file to import."))
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxImportBytes+1))
	if err != nil || len(data) > maxImportBytes {
		httpx.Render(w, r, http.StatusRequestEntityTooLarge, ImportPage(nil, "That file is too large (2 MB max)."))
		return
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")) // Excel adds a byte-order mark
	res, err := m.runImport(r, data)
	if err != nil {
		httpx.Render(w, r, http.StatusUnprocessableEntity, ImportPage(nil, err.Error()))
		return
	}
	m.audit(r, "import.products", "catalog", "", map[string]any{"products": res.Products, "variants": res.Variants, "skipped": res.Skipped, "errors": len(res.Errors)})
	httpx.Render(w, r, http.StatusOK, ImportPage(&res, ""))
}

// runImport creates products and variants from CSV rows. Each row stands alone: a bad row is
// reported and skipped, good rows are kept. Re-importing the same file is safe, since a
// product whose slug already exists is skipped instead of duplicated.
func (m *Module) runImport(r *http.Request, data []byte) (ImportResult, error) {
	ctx := r.Context()
	cr := csv.NewReader(bytes.NewReader(data))
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return ImportResult{}, errors.New("the file is empty or is not a CSV")
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(h))] = i
	}
	if _, ok := col["price"]; !ok {
		return ImportResult{}, errors.New(`the first row must be a header with at least "name" and "price" columns (download the sample)`)
	}
	if _, ok := col["name"]; !ok {
		return ImportResult{}, errors.New(`the first row must be a header with at least "name" and "price" columns (download the sample)`)
	}
	get := func(rec []string, name string) string {
		if i, ok := col[name]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}

	var res ImportResult
	fail := func(line int, format string, a ...any) {
		if len(res.Errors) < maxImportError {
			res.Errors = append(res.Errors, ImportError{Line: line, Msg: fmt.Sprintf(format, a...)})
		}
	}
	categories := map[string]catalog.Category{}
	existing, err := m.o.Catalog.Categories(ctx)
	if err != nil {
		return res, err
	}
	for _, c := range existing {
		categories[strings.ToLower(c.Name)] = c
	}
	products := map[string]catalog.Product{} // key (slug) -> product created or found in this import
	productStock := map[string]int{}         // stock on the product row (the default variant)
	gotVariants := map[string]bool{}         // products that received variant rows

	rows := 0
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		line, _ := cr.FieldPos(0)
		if err != nil {
			fail(line, "unreadable row: %v", err)
			continue
		}
		if rows++; rows > maxImportRows {
			res.Stopped = true
			break
		}
		if len(strings.TrimSpace(strings.Join(rec, ""))) == 0 {
			continue // blank line
		}

		if label := get(rec, "variant"); label != "" {
			m.importVariantRow(r, &res, fail, line, rec, get, label, products, gotVariants)
			continue
		}

		name := get(rec, "name")
		cents, perr := money.ParseCents(get(rec, "price"))
		slug := get(rec, "slug")
		if slug == "" {
			slug = catalog.Slugify(name)
		}
		switch {
		case name == "":
			fail(line, "a product row needs a name")
			continue
		case perr != nil:
			fail(line, "%q: price must be a number like 12.50", name)
			continue
		}
		if slug != "" {
			if p, err := m.o.Catalog.BySlug(ctx, slug); err == nil {
				products[slug] = p // later variant rows may still target it
				res.Skipped++
				continue
			}
		}
		stock, serr := optInt(get(rec, "stock"))
		if serr != nil || stock < 0 || stock > inventoryMaxLot {
			fail(line, "%q: stock must be a whole number between 0 and 1,000,000", name)
			continue
		}
		in := catalog.ProductInput{
			Name: name, Slug: slug, Description: get(rec, "description"), PriceCents: cents, ImageURL: get(rec, "image_url"),
			Active: truthy(get(rec, "active"), true), SKU: get(rec, "sku"), OptionName: get(rec, "option_name"),
		}
		if cat := get(rec, "category"); cat != "" {
			c, ok := categories[strings.ToLower(cat)]
			if !ok {
				nc, cerr := m.o.Catalog.CreateCategory(ctx, cat)
				if cerr != nil {
					fail(line, "%q: category %q: %v", name, cat, cerr)
					continue
				}
				c = nc
				categories[strings.ToLower(cat)] = c
			}
			in.CategoryID = &c.ID
		}
		p, err := m.o.Catalog.CreateProduct(ctx, in)
		if err != nil {
			fail(line, "%q: %v", name, err)
			continue
		}
		if stock > 0 {
			if err := m.o.Inventory.AddLot(ctx, p.Variants[0].ID, "import", stock); err != nil {
				fail(line, "%q: created, but stock failed: %v", name, err)
			}
		}
		key := slug
		if key == "" {
			key = p.Slug
		}
		products[key] = p
		productStock[key] = stock
		res.Products++
	}

	// A product that was given variants no longer needs its unnamed single form.
	for key := range gotVariants {
		p := products[key]
		if productStock[key] == 0 {
			if cur, err := m.o.Catalog.ByID(ctx, p.ID); err == nil {
				for _, v := range cur.Variants {
					if v.ID == p.Variants[0].ID {
						_ = m.o.Catalog.UpdateVariant(ctx, v.ID, catalog.VariantInput{SKU: v.SKU, Label: v.Label, PriceCents: v.PriceCents, Active: false})
					}
				}
			}
		}
	}
	return res, nil
}

func (m *Module) importVariantRow(r *http.Request, res *ImportResult, fail func(int, string, ...any), line int, rec []string,
	get func([]string, string) string, label string, products map[string]catalog.Product, gotVariants map[string]bool) {
	ctx := r.Context()
	key := get(rec, "slug")
	if key == "" {
		key = catalog.Slugify(get(rec, "name"))
	}
	p, ok := products[key]
	if !ok {
		if found, err := m.o.Catalog.BySlug(ctx, key); err == nil && key != "" {
			p, ok = found, true
			products[key] = p
		}
	}
	if !ok {
		fail(line, "variant %q: no product with slug %q (put the product row first)", label, key)
		return
	}
	in := catalog.VariantInput{SKU: get(rec, "sku"), Label: label, Active: truthy(get(rec, "active"), true)}
	if raw := get(rec, "variant_price"); raw != "" {
		c, err := money.ParseCents(raw)
		if err != nil {
			fail(line, "variant %q: price must be a number like 12.50", label)
			return
		}
		in.PriceCents = &c
	}
	stock, serr := optInt(get(rec, "stock"))
	if serr != nil || stock < 0 || stock > inventoryMaxLot {
		fail(line, "variant %q: stock must be a whole number between 0 and 1,000,000", label)
		return
	}
	v, err := m.o.Catalog.AddVariant(ctx, p.ID, in)
	if err != nil {
		if errors.Is(err, catalog.ErrLabelTaken) {
			res.Skipped++ // already there (a repeated import)
			return
		}
		fail(line, "variant %q of %q: %v", label, p.Name, err)
		return
	}
	if stock > 0 {
		if err := m.o.Inventory.AddLot(ctx, v.ID, "import", stock); err != nil {
			fail(line, "variant %q: created, but stock failed: %v", label, err)
		}
	}
	gotVariants[key] = true
	res.Variants++
}

// truthy reads yes/no style cells; an empty cell gives the default.
func truthy(s string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return def
	case "1", "true", "yes", "y", "on":
		return true
	}
	return false
}
