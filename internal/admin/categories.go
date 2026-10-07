package admin

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/catalog"
	"github.com/NaheedRayan/goat-architecture/internal/platform/httpx"
)

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
