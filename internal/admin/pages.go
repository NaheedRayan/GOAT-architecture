package admin

import (
	"errors"
	"net/http"

	"github.com/NaheedRayan/goat-architecture/internal/content"
	"github.com/NaheedRayan/goat-architecture/internal/platform/httpx"
)

// PageForm is the page editor's fields, kept across a failed save.
type PageForm struct {
	ID        string
	Slug      string
	Title     string
	Body      string
	Published bool
}

func (m *Module) pages(w http.ResponseWriter, r *http.Request) {
	ps, err := m.o.Content.All(r.Context())
	if err != nil {
		m.fail(w, r, err)
		return
	}
	httpx.Render(w, r, http.StatusOK, PagesPage(ps))
}

func (m *Module) pageNew(w http.ResponseWriter, r *http.Request) {
	httpx.Render(w, r, http.StatusOK, PageEditPage(PageForm{}, nil, ""))
}

func (m *Module) pageEdit(w http.ResponseWriter, r *http.Request) {
	id, ok := uuidParam(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	p, err := m.o.Content.Get(r.Context(), id)
	if errors.Is(err, content.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		m.fail(w, r, err)
		return
	}
	f := PageForm{ID: p.ID.String(), Slug: p.Slug, Title: p.Title, Body: p.Body, Published: p.Published}
	httpx.Render(w, r, http.StatusOK, PageEditPage(f, content.Placeholders(p.Title+"\n"+p.Body), ""))
}

func readPage(r *http.Request) PageForm {
	return PageForm{Slug: r.FormValue("slug"), Title: r.FormValue("title"), Body: r.FormValue("body"), Published: r.FormValue("published") != ""}
}

func (m *Module) pageSave(w http.ResponseWriter, r *http.Request) {
	f := readPage(r)
	p := content.Page{Slug: f.Slug, Title: f.Title, Body: f.Body, Published: f.Published}
	var err error
	action := "page.create"
	if id, ok := uuidParam(r, "id"); ok {
		p.ID, action = id, "page.update"
		err = m.o.Content.Update(r.Context(), p)
		f.ID = id.String()
	} else {
		p, err = m.o.Content.Create(r.Context(), p)
	}
	var ve content.ValidationError
	switch {
	case err == nil:
		m.audit(r, action, "page", p.ID.String(), map[string]any{"slug": p.Slug, "published": p.Published})
		http.Redirect(w, r, "/admin/pages", http.StatusSeeOther)
	case errors.As(err, &ve):
		httpx.Render(w, r, http.StatusUnprocessableEntity, PageEditPage(f, content.Placeholders(f.Title+"\n"+f.Body), ve.Error()))
	case errors.Is(err, content.ErrSlugTaken):
		httpx.Render(w, r, http.StatusConflict, PageEditPage(f, content.Placeholders(f.Title+"\n"+f.Body), err.Error()))
	case errors.Is(err, content.ErrNotFound):
		http.NotFound(w, r)
	default:
		m.fail(w, r, err)
	}
}

func (m *Module) pageDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := uuidParam(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := m.o.Content.Delete(r.Context(), id); err != nil && !errors.Is(err, content.ErrNotFound) {
		m.fail(w, r, err)
		return
	}
	m.audit(r, "page.delete", "page", id.String(), nil)
	http.Redirect(w, r, "/admin/pages", http.StatusSeeOther)
}
