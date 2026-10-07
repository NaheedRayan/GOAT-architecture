// Package http serves review submission and renders the reviews block of a product page.
package http

import (
	"context"
	"errors"
	"log/slog"
	nethttp "net/http"
	"net/url"
	"strconv"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/platform/auth"
	"github.com/NaheedRayan/goat-architecture/internal/review/app"
	"github.com/NaheedRayan/goat-architecture/internal/review/domain"
)

// Products resolves a product page's slug and a customer's display name.
type Products interface {
	IDBySlug(ctx context.Context, slug string) (uuid.UUID, error)
}

type Names interface {
	NameOf(ctx context.Context, userID uuid.UUID) (string, error)
}

type Handler struct {
	svc      *app.Service
	products Products
	names    Names
	log      *slog.Logger
}

func NewHandler(svc *app.Service, p Products, n Names, log *slog.Logger) *Handler {
	return &Handler{svc: svc, products: p, names: n, log: log}
}

func (h *Handler) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Require)
		r.Post("/products/{slug}/reviews", h.submit)
	})
}

const listLimit = 20

func (h *Handler) submit(w nethttp.ResponseWriter, r *nethttp.Request) {
	slug := chi.URLParam(r, "slug")
	back := "/products/" + url.PathEscape(slug)
	pid, err := h.products.IDBySlug(r.Context(), slug)
	if err != nil {
		nethttp.NotFound(w, r)
		return
	}
	c, _ := auth.FromContext(r.Context())
	name, _ := h.names.NameOf(r.Context(), c.UserID)
	rating, _ := strconv.Atoi(r.FormValue("rating"))
	_, err = h.svc.Submit(r.Context(), c.UserID, pid, name, rating, r.FormValue("title"), r.FormValue("body"))
	var ve domain.ValidationError
	flash := "saved"
	switch {
	case err == nil:
	case errors.As(err, &ve):
		flash = "invalid"
	case errors.Is(err, domain.ErrNotPurchased):
		flash = "not-purchased"
	default:
		h.log.Error("save review", "err", err)
		flash = "error"
	}
	nethttp.Redirect(w, r, back+"?review="+flash+"#reviews", nethttp.StatusSeeOther)
}

// Section renders the reviews block for a product page. flash is the outcome code
// of a just-submitted review.
func (h *Handler) Section(ctx context.Context, productID uuid.UUID, slug, flash string) templ.Component {
	vm := SectionVM{Slug: slug, Flash: flash, Draft: Draft{Rating: 0}}
	rs, sum, err := h.svc.ForProduct(ctx, productID, listLimit)
	if err != nil {
		h.log.Warn("load reviews", "err", err)
		return templ.NopComponent
	}
	vm.Reviews, vm.Summary = rs, sum
	if c, ok := auth.FromContext(ctx); ok {
		vm.SignedIn = true
		if mine, err := h.svc.Mine(ctx, c.UserID, productID); err == nil && mine != nil {
			vm.Mine = mine
			vm.Draft = Draft{Rating: mine.Rating, Title: mine.Title, Body: mine.Body}
		}
		if ok, err := h.svc.CanReview(ctx, c.UserID, productID); err == nil {
			vm.CanReview = ok
		}
	}
	return Section(vm)
}
