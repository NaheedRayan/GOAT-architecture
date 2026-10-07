// Package http serves the wishlist toggle routes.
package http

import (
	"context"
	"errors"
	"log/slog"
	nethttp "net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/platform/auth"
	"github.com/NaheedRayan/goat-architecture/internal/platform/httpx"
	"github.com/NaheedRayan/goat-architecture/internal/wishlist/app"
)

// Products tells whether a product exists and can be wished for.
type Products interface {
	Exists(ctx context.Context, id uuid.UUID) bool
}

type Handler struct {
	svc      *app.Service
	products Products
	log      *slog.Logger
}

func NewHandler(svc *app.Service, p Products, log *slog.Logger) *Handler {
	return &Handler{svc: svc, products: p, log: log}
}

func (h *Handler) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Require)
		r.Post("/wishlist/{product}", h.add)
		r.Post("/wishlist/{product}/remove", h.remove)
	})
}

func (h *Handler) add(w nethttp.ResponseWriter, r *nethttp.Request) {
	pid, c, ok := h.parse(w, r)
	if !ok {
		return
	}
	if !h.products.Exists(r.Context(), pid) {
		nethttp.NotFound(w, r)
		return
	}
	if err := h.svc.Add(r.Context(), c.UserID, pid); err != nil && !errors.As(err, new(app.ErrFull)) {
		h.log.Error("add to wishlist", "err", err)
		nethttp.Error(w, "something went wrong", nethttp.StatusInternalServerError)
		return
	}
	h.done(w, r)
}

func (h *Handler) remove(w nethttp.ResponseWriter, r *nethttp.Request) {
	pid, c, ok := h.parse(w, r)
	if !ok {
		return
	}
	if err := h.svc.Remove(r.Context(), c.UserID, pid); err != nil {
		h.log.Error("remove from wishlist", "err", err)
		nethttp.Error(w, "something went wrong", nethttp.StatusInternalServerError)
		return
	}
	h.done(w, r)
}

func (h *Handler) parse(w nethttp.ResponseWriter, r *nethttp.Request) (uuid.UUID, auth.Claims, bool) {
	pid, err := uuid.Parse(chi.URLParam(r, "product"))
	if err != nil {
		nethttp.NotFound(w, r)
		return uuid.Nil, auth.Claims{}, false
	}
	c, _ := auth.FromContext(r.Context())
	return pid, c, true
}

func (h *Handler) done(w nethttp.ResponseWriter, r *nethttp.Request) {
	next := httpx.SafeNext(r.FormValue("next"))
	if next == "" || next == "/" {
		next = "/wishlist"
	}
	nethttp.Redirect(w, r, next, nethttp.StatusSeeOther)
}
