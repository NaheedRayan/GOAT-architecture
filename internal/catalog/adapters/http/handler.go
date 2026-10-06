// Package http serves the public storefront pages for browsing the catalog.
package http

import (
	"context"
	"errors"
	"log/slog"
	nethttp "net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/catalog/app"
	"github.com/NaheedRayan/goat-architecture/internal/catalog/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/httpx"
	"github.com/NaheedRayan/goat-architecture/internal/platform/ui"
)

// StockLookup lets the storefront show availability without importing the
// inventory module. It may be nil, in which case every product shows as available.
type StockLookup interface {
	Available(ctx context.Context, productIDs []uuid.UUID) (map[uuid.UUID]int, error)
}

type Handler struct {
	svc   *app.Service
	stock StockLookup
	log   *slog.Logger
}

func NewHandler(svc *app.Service, stock StockLookup, log *slog.Logger) *Handler {
	return &Handler{svc: svc, stock: stock, log: log}
}

func (h *Handler) Routes(r chi.Router) {
	r.Get("/", h.home)
	r.Get("/products", h.list)
	r.Get("/products/{slug}", h.detail)
}

// Card is a product plus the availability the UI needs.
type Card struct {
	Product domain.Product
	InStock bool
}

func (h *Handler) cards(ctx context.Context, ps []domain.Product) []Card {
	avail := map[uuid.UUID]int{}
	known := false
	if h.stock != nil && len(ps) > 0 {
		ids := make([]uuid.UUID, len(ps))
		for i, p := range ps {
			ids[i] = p.ID
		}
		if m, err := h.stock.Available(ctx, ids); err != nil {
			h.log.Warn("stock lookup failed", "err", err)
		} else {
			avail, known = m, true
		}
	}
	out := make([]Card, len(ps))
	for i, p := range ps {
		out[i] = Card{Product: p, InStock: !known || avail[p.ID] > 0}
	}
	return out
}

func (h *Handler) home(w nethttp.ResponseWriter, r *nethttp.Request) {
	if r.URL.Path != "/" {
		nethttp.NotFound(w, r)
		return
	}
	page, err := h.svc.List(r.Context(), domain.Filter{PerPage: 8})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.Render(w, r, nethttp.StatusOK, HomePage(h.cards(r.Context(), page.Products)))
}

func (h *Handler) list(w nethttp.ResponseWriter, r *nethttp.Request) {
	qv := r.URL.Query()
	pg, _ := strconv.Atoi(qv.Get("page"))
	f := domain.Filter{Query: qv.Get("q"), CategorySlug: qv.Get("category"), Page: pg}
	page, err := h.svc.List(r.Context(), f)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	cats, err := h.svc.ActiveCategories(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.Render(w, r, nethttp.StatusOK, ListPage(f, page, cats, h.cards(r.Context(), page.Products)))
}

func (h *Handler) detail(w nethttp.ResponseWriter, r *nethttp.Request) {
	p, err := h.svc.BySlug(r.Context(), chi.URLParam(r, "slug"))
	if errors.Is(err, domain.ErrNotFound) || err == nil && !p.Active {
		httpx.Render(w, r, nethttp.StatusNotFound, ui.ErrorPage(404, "We couldn't find that product."))
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.Render(w, r, nethttp.StatusOK, DetailPage(h.cards(r.Context(), []domain.Product{p})[0]))
}

func (h *Handler) fail(w nethttp.ResponseWriter, r *nethttp.Request, err error) {
	h.log.Error("catalog http", "path", r.URL.Path, "err", err)
	httpx.Render(w, r, nethttp.StatusInternalServerError, ui.ErrorPage(500, "Something went wrong on our side."))
}
