package admin

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/catalog"
	"github.com/NaheedRayan/goat-architecture/internal/inventory"
	"github.com/NaheedRayan/goat-architecture/internal/platform/httpx"
	"github.com/NaheedRayan/goat-architecture/internal/platform/money"
)

// VariantRow is a variant with its stock batches, as shown on the product page.
type VariantRow struct {
	Variant   catalog.Variant
	Lots      []inventory.Lot
	Available int
}

func (m *Module) products(w http.ResponseWriter, r *http.Request) {
	deleted := r.URL.Query().Get("view") == "deleted"
	page, err := m.o.Catalog.List(r.Context(), catalog.Filter{
		IncludeInactive: true, Archived: deleted, PerPage: 60, Page: atoi(r.URL.Query().Get("page"), 1),
	})
	if err != nil {
		m.fail(w, r, err)
		return
	}
	ids := make([]uuid.UUID, len(page.Products))
	for i, p := range page.Products {
		ids[i] = p.ID
	}
	byProduct, err := m.o.Catalog.VariantsByProducts(r.Context(), ids)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	var vids []uuid.UUID
	for _, vs := range byProduct {
		for _, v := range vs {
			vids = append(vids, v.ID)
		}
	}
	avail, err := m.o.Inventory.Available(r.Context(), vids)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	stock := map[uuid.UUID]int{}
	for pid, vs := range byProduct {
		for _, v := range vs {
			stock[pid] += avail[v.ID]
		}
	}
	cats, err := m.o.Catalog.Categories(r.Context())
	if err != nil {
		m.fail(w, r, err)
		return
	}
	httpx.Render(w, r, http.StatusOK, ProductsPage(page, stock, cats, deleted))
}

// variantRows loads stock batches for each variant of a product.
func (m *Module) variantRows(ctx context.Context, vs []catalog.Variant) ([]VariantRow, error) {
	rows := make([]VariantRow, len(vs))
	for i, v := range vs {
		lots, err := m.o.Inventory.Lots(ctx, v.ID)
		if err != nil {
			return nil, err
		}
		total := 0
		for _, l := range lots {
			total += l.Quantity
		}
		rows[i] = VariantRow{Variant: v, Lots: lots, Available: total}
	}
	return rows, nil
}

func (m *Module) productForm(w http.ResponseWriter, r *http.Request) {
	form := ProductForm{Active: true}
	if idStr := chi.URLParam(r, "id"); idStr != "" {
		id, err := uuid.Parse(idStr)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		m.renderProduct(w, r, id, "", http.StatusOK)
		return
	}
	m.renderForm(w, r, form, nil, "", http.StatusOK)
}

// renderProduct shows the edit page for a stored product, optionally with an error message.
func (m *Module) renderProduct(w http.ResponseWriter, r *http.Request, id uuid.UUID, msg string, status int) {
	p, err := m.o.Catalog.ByID(r.Context(), id)
	if errors.Is(err, catalog.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		m.fail(w, r, err)
		return
	}
	rows, err := m.variantRows(r.Context(), p.Variants)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	m.renderForm(w, r, formFromProduct(p), rows, msg, status)
}

func (m *Module) renderForm(w http.ResponseWriter, r *http.Request, f ProductForm, rows []VariantRow, msg string, status int) {
	cats, err := m.o.Catalog.Categories(r.Context())
	if err != nil {
		m.fail(w, r, err)
		return
	}
	httpx.Render(w, r, status, ProductFormPage(f, cats, rows, msg))
}

func readForm(r *http.Request) (ProductForm, catalog.ProductInput, error) {
	f := ProductForm{
		ID: chi.URLParam(r, "id"), Name: r.FormValue("name"), Slug: r.FormValue("slug"), CategoryID: r.FormValue("category_id"),
		Price: r.FormValue("price"), ImageURL: r.FormValue("image_url"), Description: r.FormValue("description"),
		Active: r.FormValue("active") != "", OptionName: r.FormValue("option_name"), SKU: r.FormValue("sku"),
	}
	in := catalog.ProductInput{
		Slug: f.Slug, Name: f.Name, Description: f.Description, ImageURL: f.ImageURL, Active: f.Active,
		OptionName: f.OptionName, SKU: f.SKU,
	}
	cents, err := money.ParseCents(f.Price)
	if err != nil {
		return f, in, catalog.ValidationError("price must be a number like 12.50")
	}
	in.PriceCents = cents
	if f.CategoryID != "" {
		cid, err := uuid.Parse(f.CategoryID)
		if err != nil {
			return f, in, catalog.ValidationError("unknown category")
		}
		in.CategoryID = &cid
	}
	return f, in, nil
}

func (m *Module) createProduct(w http.ResponseWriter, r *http.Request) {
	f, in, err := readForm(r)
	if err == nil {
		var p catalog.Product
		if p, err = m.o.Catalog.CreateProduct(r.Context(), in); err == nil {
			m.audit(r, "product.create", "product", p.ID.String(), map[string]any{"name": p.Name})
			http.Redirect(w, r, "/admin/products/"+p.ID.String()+"/edit", http.StatusSeeOther)
			return
		}
	}
	m.formError(w, r, f, uuid.Nil, err)
}

func (m *Module) updateProduct(w http.ResponseWriter, r *http.Request) {
	id, perr := uuid.Parse(chi.URLParam(r, "id"))
	if perr != nil {
		http.NotFound(w, r)
		return
	}
	f, in, err := readForm(r)
	if err == nil {
		if err = m.o.Catalog.UpdateProduct(r.Context(), id, in); err == nil {
			m.audit(r, "product.update", "product", id.String(), map[string]any{"name": in.Name, "price_cents": in.PriceCents, "active": in.Active})
			http.Redirect(w, r, "/admin/products", http.StatusSeeOther)
			return
		}
	}
	m.formError(w, r, f, id, err)
}

// formError renders a product-form problem on the page itself, keeping the typed values.
func (m *Module) formError(w http.ResponseWriter, r *http.Request, f ProductForm, id uuid.UUID, err error) {
	var rows []VariantRow
	if id != uuid.Nil {
		if p, perr := m.o.Catalog.ByID(r.Context(), id); perr == nil {
			rows, _ = m.variantRows(r.Context(), p.Variants)
		}
	}
	var ve catalog.ValidationError
	switch {
	case errors.As(err, &ve):
		m.renderForm(w, r, f, rows, ve.Error(), http.StatusUnprocessableEntity)
	case errors.Is(err, catalog.ErrSlugTaken), errors.Is(err, catalog.ErrSKUTaken):
		m.renderForm(w, r, f, rows, err.Error(), http.StatusConflict)
	case errors.Is(err, catalog.ErrCategoryGone):
		f.CategoryID = "" // the picked category was deleted meanwhile
		m.renderForm(w, r, f, rows, "That category was just deleted. Pick another one and save again.", http.StatusConflict)
	case errors.Is(err, catalog.ErrNotFound):
		http.NotFound(w, r)
	default:
		m.fail(w, r, err)
	}
}

// archiveProduct is the "Delete" action: the product leaves the store but
// order history is untouched. Repeating it (double click) is harmless.
func (m *Module) archiveProduct(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := m.o.Catalog.ArchiveProduct(r.Context(), id); err != nil && !errors.Is(err, catalog.ErrNotFound) {
		m.fail(w, r, err)
		return
	}
	m.audit(r, "product.delete", "product", id.String(), nil)
	http.Redirect(w, r, "/admin/products", http.StatusSeeOther)
}

func (m *Module) restoreProduct(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := m.o.Catalog.RestoreProduct(r.Context(), id); err != nil && !errors.Is(err, catalog.ErrNotFound) {
		m.fail(w, r, err)
		return
	}
	m.audit(r, "product.restore", "product", id.String(), nil)
	http.Redirect(w, r, "/admin/products/"+id.String()+"/edit", http.StatusSeeOther)
}

// ---- variants ----

func readVariant(r *http.Request) (catalog.VariantInput, error) {
	in := catalog.VariantInput{SKU: r.FormValue("sku"), Label: r.FormValue("label"), Active: r.FormValue("active") != ""}
	if raw := strings.TrimSpace(r.FormValue("price")); raw != "" {
		cents, err := money.ParseCents(raw)
		if err != nil {
			return in, catalog.ValidationError("variant price must be a number like 12.50, or empty to use the product price")
		}
		in.PriceCents = &cents
	}
	return in, nil
}

// variantError re-renders the product page with the problem.
func (m *Module) variantError(w http.ResponseWriter, r *http.Request, productID uuid.UUID, err error) {
	var ve catalog.ValidationError
	switch {
	case errors.As(err, &ve):
		m.renderProduct(w, r, productID, ve.Error(), http.StatusUnprocessableEntity)
	case errors.Is(err, catalog.ErrSKUTaken), errors.Is(err, catalog.ErrLabelTaken), errors.Is(err, catalog.ErrNeedsLabel):
		m.renderProduct(w, r, productID, err.Error(), http.StatusConflict)
	case errors.Is(err, catalog.ErrVariantNotFound), errors.Is(err, catalog.ErrNotFound):
		http.NotFound(w, r)
	default:
		m.fail(w, r, err)
	}
}

func (m *Module) addVariant(w http.ResponseWriter, r *http.Request) {
	pid, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	in, err := readVariant(r)
	if err == nil {
		var v catalog.Variant
		if v, err = m.o.Catalog.AddVariant(r.Context(), pid, in); err == nil {
			m.audit(r, "variant.create", "variant", v.ID.String(), map[string]any{"product_id": pid, "label": v.Label})
			http.Redirect(w, r, "/admin/products/"+pid.String()+"/edit", http.StatusSeeOther)
			return
		}
	}
	m.variantError(w, r, pid, err)
}

// productOfVariant finds the product a variant belongs to (for redirects and error pages).
func (m *Module) productOfVariant(ctx context.Context, vid uuid.UUID) (uuid.UUID, error) {
	infos, err := m.o.Catalog.VariantInfos(ctx, []uuid.UUID{vid})
	if err != nil {
		return uuid.Nil, err
	}
	if len(infos) == 0 {
		return uuid.Nil, catalog.ErrVariantNotFound
	}
	return infos[0].ProductID, nil
}

func (m *Module) updateVariant(w http.ResponseWriter, r *http.Request) {
	vid, err := uuid.Parse(chi.URLParam(r, "vid"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	pid, err := m.productOfVariant(r.Context(), vid)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	in, err := readVariant(r)
	if err == nil {
		if err = m.o.Catalog.UpdateVariant(r.Context(), vid, in); err == nil {
			m.audit(r, "variant.update", "variant", vid.String(), map[string]any{"label": in.Label, "sku": in.SKU, "active": in.Active})
			http.Redirect(w, r, "/admin/products/"+pid.String()+"/edit", http.StatusSeeOther)
			return
		}
	}
	m.variantError(w, r, pid, err)
}

// ---- stock ----

func (m *Module) receiveStock(w http.ResponseWriter, r *http.Request) {
	vid, err := uuid.Parse(chi.URLParam(r, "vid"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	pid, err := m.productOfVariant(r.Context(), vid)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	qty, qerr := strconv.Atoi(strings.TrimSpace(r.FormValue("quantity")))
	if qerr != nil || qty <= 0 || qty > inventoryMaxLot {
		m.renderProduct(w, r, pid, "Quantity received must be a whole number between 1 and 1,000,000.", http.StatusUnprocessableEntity)
		return
	}
	if err := m.o.Inventory.AddLot(r.Context(), vid, r.FormValue("label"), qty); err != nil {
		m.fail(w, r, err)
		return
	}
	m.audit(r, "stock.receive", "variant", vid.String(), map[string]any{"quantity": qty, "label": r.FormValue("label")})
	http.Redirect(w, r, "/admin/products/"+pid.String()+"/edit", http.StatusSeeOther)
}

const inventoryMaxLot = 1_000_000

func (m *Module) setStock(w http.ResponseWriter, r *http.Request) {
	vid, err1 := uuid.Parse(chi.URLParam(r, "vid"))
	lot, err2 := uuid.Parse(chi.URLParam(r, "lot"))
	if err1 != nil || err2 != nil {
		http.NotFound(w, r)
		return
	}
	pid, err := m.productOfVariant(r.Context(), vid)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	qty, qerr := strconv.Atoi(strings.TrimSpace(r.FormValue("quantity")))
	if qerr != nil {
		m.renderProduct(w, r, pid, "Quantity must be a whole number.", http.StatusUnprocessableEntity)
		return
	}
	switch err := m.o.Inventory.SetLotQuantity(r.Context(), vid, lot, qty); {
	case errors.Is(err, inventory.ErrInvalidQuantity):
		m.renderProduct(w, r, pid, err.Error(), http.StatusUnprocessableEntity)
		return
	case errors.Is(err, inventory.ErrLotNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		m.fail(w, r, err)
		return
	}
	m.audit(r, "stock.set", "lot", lot.String(), map[string]any{"variant_id": vid, "quantity": qty})
	http.Redirect(w, r, "/admin/products/"+pid.String()+"/edit", http.StatusSeeOther)
}

// ---- images ----

func (m *Module) addImage(w http.ResponseWriter, r *http.Request) {
	pid, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		m.renderProduct(w, r, pid, "That upload was too large or malformed. Images can be up to 8 MB.", http.StatusRequestEntityTooLarge)
		return
	}
	file, _, err := r.FormFile("image")
	if err != nil {
		m.renderProduct(w, r, pid, "Choose an image file to upload.", http.StatusUnprocessableEntity)
		return
	}
	defer file.Close()
	img, err := m.o.Catalog.AddImage(r.Context(), pid, file, r.FormValue("alt"))
	var ve catalog.ValidationError
	switch {
	case errors.As(err, &ve):
		m.renderProduct(w, r, pid, ve.Error(), http.StatusUnprocessableEntity)
		return
	case errors.Is(err, catalog.ErrUploadsDisabled):
		m.renderProduct(w, r, pid, err.Error(), http.StatusServiceUnavailable)
		return
	case errors.Is(err, catalog.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		m.fail(w, r, err)
		return
	}
	m.audit(r, "image.add", "product", pid.String(), map[string]any{"image_id": img.ID})
	http.Redirect(w, r, "/admin/products/"+pid.String()+"/edit", http.StatusSeeOther)
}

func (m *Module) removeImage(w http.ResponseWriter, r *http.Request) {
	pid, err1 := uuid.Parse(chi.URLParam(r, "id"))
	iid, err2 := uuid.Parse(chi.URLParam(r, "img"))
	if err1 != nil || err2 != nil {
		http.NotFound(w, r)
		return
	}
	if err := m.o.Catalog.RemoveImage(r.Context(), pid, iid); err != nil && !errors.Is(err, catalog.ErrImageNotFound) {
		m.fail(w, r, err)
		return
	}
	m.audit(r, "image.remove", "product", pid.String(), map[string]any{"image_id": iid})
	http.Redirect(w, r, "/admin/products/"+pid.String()+"/edit", http.StatusSeeOther)
}

func (m *Module) moveImage(w http.ResponseWriter, r *http.Request) {
	pid, err1 := uuid.Parse(chi.URLParam(r, "id"))
	iid, err2 := uuid.Parse(chi.URLParam(r, "img"))
	if err1 != nil || err2 != nil {
		http.NotFound(w, r)
		return
	}
	delta := 1
	if r.FormValue("dir") == "up" {
		delta = -1
	}
	if err := m.o.Catalog.MoveImage(r.Context(), pid, iid, delta); err != nil && !errors.Is(err, catalog.ErrImageNotFound) {
		m.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/products/"+pid.String()+"/edit", http.StatusSeeOther)
}
