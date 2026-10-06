// Package admin is the back office. It owns no data: it composes the public
// APIs of catalog, inventory and order behind an admin-only router.
package admin

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/catalog"
	"github.com/NaheedRayan/goat-architecture/internal/inventory"
	"github.com/NaheedRayan/goat-architecture/internal/order"
	"github.com/NaheedRayan/goat-architecture/internal/platform/auth"
	"github.com/NaheedRayan/goat-architecture/internal/platform/httpx"
	"github.com/NaheedRayan/goat-architecture/internal/platform/money"
	"github.com/NaheedRayan/goat-architecture/internal/platform/ui"
)

type Options struct {
	Catalog   catalog.API
	Inventory inventory.API
	Orders    order.API
	Log       *slog.Logger
}

type Module struct{ o Options }

func New(o Options) *Module { return &Module{o: o} }

func (m *Module) Routes(r chi.Router) {
	r.Route("/admin", func(r chi.Router) {
		r.Use(auth.RequireRole(auth.RoleAdmin))
		r.Get("/", m.dashboard)
		r.Get("/products", m.products)
		r.Get("/products/new", m.productForm)
		r.Post("/products", m.createProduct)
		r.Get("/products/{id}/edit", m.productForm)
		r.Post("/products/{id}", m.updateProduct)
		r.Post("/products/{id}/stock", m.addStock)
		r.Post("/products/{id}/stock/{lot}", m.setStock)
		r.Post("/products/{id}/delete", m.archiveProduct)
		r.Post("/products/{id}/restore", m.restoreProduct)
		r.Get("/categories", m.categories)
		r.Post("/categories", m.createCategory)
		r.Post("/categories/{id}", m.renameCategory)
		r.Post("/categories/{id}/delete", m.deleteCategory)
		r.Get("/orders", m.orders)
		r.Get("/orders/{id}", m.orderDetail)
		r.Post("/orders/{id}/ship", m.ship)
	})
}

var orderStatuses = []string{order.StatusAwaitingPayment, order.StatusPaid, order.StatusFulfilling, order.StatusShipped, order.StatusCancelled}

func (m *Module) dashboard(w http.ResponseWriter, r *http.Request) {
	counts, err := m.o.Orders.CountByStatus(r.Context())
	if err != nil {
		m.fail(w, r, err)
		return
	}
	stats := make([]StatusCount, 0, len(orderStatuses))
	for _, s := range orderStatuses {
		stats = append(stats, StatusCount{Status: s, N: counts[s]})
	}
	httpx.Render(w, r, http.StatusOK, DashboardPage(stats))
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
	stock, err := m.o.Inventory.Available(r.Context(), ids)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	cats, err := m.o.Catalog.Categories(r.Context())
	if err != nil {
		m.fail(w, r, err)
		return
	}
	httpx.Render(w, r, http.StatusOK, ProductsPage(page, stock, cats, deleted))
}

func (m *Module) productForm(w http.ResponseWriter, r *http.Request) {
	form := ProductForm{Active: true}
	var lots []inventory.Lot
	if idStr := chi.URLParam(r, "id"); idStr != "" {
		id, err := uuid.Parse(idStr)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		p, err := m.o.Catalog.ByID(r.Context(), id)
		if errors.Is(err, catalog.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			m.fail(w, r, err)
			return
		}
		form = formFromProduct(p)
		if lots, err = m.o.Inventory.Lots(r.Context(), id); err != nil {
			m.fail(w, r, err)
			return
		}
	}
	m.renderForm(w, r, form, lots, "", http.StatusOK)
}

func (m *Module) renderForm(w http.ResponseWriter, r *http.Request, f ProductForm, lots []inventory.Lot, msg string, status int) {
	cats, err := m.o.Catalog.Categories(r.Context())
	if err != nil {
		m.fail(w, r, err)
		return
	}
	httpx.Render(w, r, status, ProductFormPage(f, cats, lots, msg))
}

func readForm(r *http.Request) (ProductForm, catalog.ProductInput, error) {
	f := ProductForm{
		ID: chi.URLParam(r, "id"), Name: r.FormValue("name"), Slug: r.FormValue("slug"), CategoryID: r.FormValue("category_id"),
		Price: r.FormValue("price"), ImageURL: r.FormValue("image_url"), Description: r.FormValue("description"),
		Active: r.FormValue("active") != "",
	}
	in := catalog.ProductInput{Slug: f.Slug, Name: f.Name, Description: f.Description, ImageURL: f.ImageURL, Active: f.Active}
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
			http.Redirect(w, r, "/admin/products/"+p.ID.String()+"/edit", http.StatusSeeOther)
			return
		}
	}
	m.formError(w, r, f, nil, err)
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
			http.Redirect(w, r, "/admin/products", http.StatusSeeOther)
			return
		}
	}
	lots, _ := m.o.Inventory.Lots(r.Context(), id)
	m.formError(w, r, f, lots, err)
}

func (m *Module) formError(w http.ResponseWriter, r *http.Request, f ProductForm, lots []inventory.Lot, err error) {
	var ve catalog.ValidationError
	switch {
	case errors.As(err, &ve):
		m.renderForm(w, r, f, lots, ve.Error(), http.StatusUnprocessableEntity)
	case errors.Is(err, catalog.ErrSlugTaken):
		m.renderForm(w, r, f, lots, err.Error(), http.StatusConflict)
	case errors.Is(err, catalog.ErrCategoryGone):
		f.CategoryID = "" // the picked category was deleted meanwhile
		m.renderForm(w, r, f, lots, "That category was just deleted. Pick another one and save again.", http.StatusConflict)
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
	http.Redirect(w, r, "/admin/products/"+id.String()+"/edit", http.StatusSeeOther)
}

func (m *Module) addStock(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	qty, qerr := strconv.Atoi(r.FormValue("quantity"))
	if err != nil || qerr != nil || qty <= 0 || qty > 1_000_000 {
		http.Error(w, "quantity must be a positive number", http.StatusUnprocessableEntity)
		return
	}
	if err := m.o.Inventory.AddLot(r.Context(), id, r.FormValue("label"), qty); err != nil {
		m.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/products/"+id.String()+"/edit", http.StatusSeeOther)
}

func (m *Module) setStock(w http.ResponseWriter, r *http.Request) {
	pid, err1 := uuid.Parse(chi.URLParam(r, "id"))
	lot, err2 := uuid.Parse(chi.URLParam(r, "lot"))
	qty, err3 := strconv.Atoi(strings.TrimSpace(r.FormValue("quantity")))
	if err1 != nil || err2 != nil {
		http.NotFound(w, r)
		return
	}
	if err3 != nil {
		http.Error(w, "quantity must be a whole number", http.StatusUnprocessableEntity)
		return
	}
	switch err := m.o.Inventory.SetLotQuantity(r.Context(), pid, lot, qty); {
	case errors.Is(err, inventory.ErrInvalidQuantity):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	case errors.Is(err, inventory.ErrLotNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		m.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/products/"+pid.String()+"/edit", http.StatusSeeOther)
}

func (m *Module) renderCategories(w http.ResponseWriter, r *http.Request, newName, errMsg string, status int) {
	stats, err := m.o.Catalog.CategoryStats(r.Context())
	if err != nil {
		m.fail(w, r, err)
		return
	}
	httpx.Render(w, r, status, CategoriesPage(stats, newName, errMsg))
}

func (m *Module) categories(w http.ResponseWriter, r *http.Request) {
	m.renderCategories(w, r, "", "", http.StatusOK)
}

// categoryError renders user-correctable category problems on the page itself.
func (m *Module) categoryError(w http.ResponseWriter, r *http.Request, newName string, err error) {
	var ve catalog.ValidationError
	switch {
	case errors.As(err, &ve):
		m.renderCategories(w, r, newName, ve.Error(), http.StatusUnprocessableEntity)
	case errors.Is(err, catalog.ErrCategoryExists):
		m.renderCategories(w, r, newName, err.Error(), http.StatusConflict)
	case errors.Is(err, catalog.ErrNotFound):
		m.renderCategories(w, r, newName, "That category no longer exists.", http.StatusNotFound)
	default:
		m.fail(w, r, err)
	}
}

func (m *Module) createCategory(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	if _, err := m.o.Catalog.CreateCategory(r.Context(), name); err != nil {
		m.categoryError(w, r, name, err)
		return
	}
	http.Redirect(w, r, "/admin/categories", http.StatusSeeOther)
}

func (m *Module) renameCategory(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := m.o.Catalog.RenameCategory(r.Context(), id, r.FormValue("name")); err != nil {
		m.categoryError(w, r, "", err)
		return
	}
	http.Redirect(w, r, "/admin/categories", http.StatusSeeOther)
}

func (m *Module) deleteCategory(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Deleting twice (double click, stale page) is not an error.
	if err := m.o.Catalog.DeleteCategory(r.Context(), id); err != nil && !errors.Is(err, catalog.ErrNotFound) {
		m.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/categories", http.StatusSeeOther)
}

const ordersPerPage = 50

func (m *Module) orders(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	page := min(atoi(r.URL.Query().Get("page"), 1), 10000)
	// Ask for one extra row to learn whether a next page exists.
	os, err := m.o.Orders.List(r.Context(), status, ordersPerPage+1, (page-1)*ordersPerPage)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	hasNext := len(os) > ordersPerPage
	if hasNext {
		os = os[:ordersPerPage]
	}
	httpx.Render(w, r, http.StatusOK, OrdersPage(os, status, page, hasNext))
}

func (m *Module) orderDetail(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	o, err := m.o.Orders.Get(r.Context(), id)
	if errors.Is(err, order.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		m.fail(w, r, err)
		return
	}
	httpx.Render(w, r, http.StatusOK, OrderDetailPage(o))
}

func (m *Module) ship(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch err := m.o.Orders.Ship(r.Context(), id); {
	case errors.Is(err, order.ErrNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(err, order.ErrInvalidTransition):
		http.Error(w, "only orders being prepared can be shipped", http.StatusConflict)
		return
	case err != nil:
		m.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/orders/"+id.String(), http.StatusSeeOther)
}

func (m *Module) fail(w http.ResponseWriter, r *http.Request, err error) {
	m.o.Log.Error("admin http", "path", r.URL.Path, "err", err)
	httpx.Render(w, r, http.StatusInternalServerError, ui.ErrorPage(500, "Something went wrong."))
}

func atoi(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return def
}
