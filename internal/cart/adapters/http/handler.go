// Package http serves the cart pages and the cart-aware middleware.
package http

import (
	"errors"
	"log/slog"
	nethttp "net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/cart/app"
	"github.com/NaheedRayan/goat-architecture/internal/cart/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/auth"
	"github.com/NaheedRayan/goat-architecture/internal/platform/httpx"
	"github.com/NaheedRayan/goat-architecture/internal/platform/ratelimit"
	"github.com/NaheedRayan/goat-architecture/internal/platform/ui"
)

const guestCookie = "guest_id"

type Handler struct {
	svc    *app.Service
	secure bool
	log    *slog.Logger
	adds   *ratelimit.Limiter // stops anonymous clients from minting unlimited carts
}

func NewHandler(svc *app.Service, secureCookies bool, log *slog.Logger) *Handler {
	return &Handler{svc: svc, secure: secureCookies, log: log, adds: ratelimit.New(100, 2*time.Second)}
}

func (h *Handler) Routes(r chi.Router) {
	r.Get("/cart", h.show)
	r.Post("/cart/items", h.add)
	r.Post("/cart/items/{id}/update", h.update)
	r.Post("/cart/items/{id}/remove", h.remove)
}

func guestID(r *nethttp.Request) (uuid.UUID, bool) {
	ck, err := r.Cookie(guestCookie)
	if err != nil {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(ck.Value)
	return id, err == nil
}

// owner resolves the cart owner key; ok is false for a visitor with no cart yet.
func owner(r *nethttp.Request) (string, bool) {
	if c, ok := auth.FromContext(r.Context()); ok {
		return domain.UserOwner(c.UserID), true
	}
	if g, ok := guestID(r); ok {
		return domain.GuestOwner(g), true
	}
	return "", false
}

// ownerForWrite also creates the guest cookie for first-time anonymous visitors.
func (h *Handler) ownerForWrite(w nethttp.ResponseWriter, r *nethttp.Request) string {
	if o, ok := owner(r); ok {
		return o
	}
	g := app.NewGuestID()
	nethttp.SetCookie(w, &nethttp.Cookie{
		Name: guestCookie, Value: g.String(), Path: "/", HttpOnly: true, Secure: h.secure,
		SameSite: nethttp.SameSiteLaxMode, Expires: time.Now().Add(90 * 24 * time.Hour),
	})
	return domain.GuestOwner(g)
}

// Middleware merges a guest cart into the user's cart after sign-in and makes
// the item count available to the page header.
func (h *Handler) Middleware(next nethttp.Handler) nethttp.Handler {
	return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if c, ok := auth.FromContext(r.Context()); ok {
			if g, ok := guestID(r); ok {
				if err := h.svc.Merge(r.Context(), domain.GuestOwner(g), domain.UserOwner(c.UserID)); err != nil {
					h.log.Error("merge guest cart", "err", err)
				} else {
					nethttp.SetCookie(w, &nethttp.Cookie{Name: guestCookie, Value: "", Path: "/", MaxAge: -1})
				}
			}
		}
		if r.Method == nethttp.MethodGet {
			if o, ok := owner(r); ok {
				if n, err := h.svc.Count(r.Context(), o); err == nil {
					r = r.WithContext(ui.WithCartCount(r.Context(), n))
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) show(w nethttp.ResponseWriter, r *nethttp.Request) {
	var v domain.View
	if o, ok := owner(r); ok {
		var err error
		if v, err = h.svc.View(r.Context(), o); err != nil {
			h.fail(w, r, err)
			return
		}
	}
	httpx.Render(w, r, nethttp.StatusOK, CartPage(v))
}

func (h *Handler) add(w nethttp.ResponseWriter, r *nethttp.Request) {
	if ok, wait := h.adds.Allow(httpx.ClientIP(r)); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		httpx.Render(w, r, nethttp.StatusTooManyRequests, ui.ErrorPage(429, "You're adding things too quickly. Please wait a moment."))
		return
	}
	pid, err := uuid.Parse(r.FormValue("variant_id"))
	qty, qerr := strconv.Atoi(r.FormValue("quantity"))
	if err != nil || qerr != nil {
		nethttp.Error(w, "bad request", nethttp.StatusBadRequest)
		return
	}
	err = h.svc.Add(r.Context(), h.ownerForWrite(w, r), pid, qty)
	switch {
	case errors.Is(err, domain.ErrProductUnavailable), errors.Is(err, domain.ErrInvalidQuantity), errors.Is(err, domain.ErrCartFull):
		nethttp.Error(w, err.Error(), nethttp.StatusUnprocessableEntity)
		return
	case err != nil:
		h.fail(w, r, err)
		return
	}
	nethttp.Redirect(w, r, "/cart", nethttp.StatusSeeOther)
}

func (h *Handler) update(w nethttp.ResponseWriter, r *nethttp.Request) {
	pid, err := uuid.Parse(chi.URLParam(r, "id"))
	qty, qerr := strconv.Atoi(r.FormValue("quantity"))
	o, ok := owner(r)
	if err != nil || qerr != nil || !ok {
		nethttp.Redirect(w, r, "/cart", nethttp.StatusSeeOther)
		return
	}
	if err := h.svc.SetQuantity(r.Context(), o, pid, qty); err != nil && !errors.Is(err, domain.ErrInvalidQuantity) {
		h.fail(w, r, err)
		return
	}
	nethttp.Redirect(w, r, "/cart", nethttp.StatusSeeOther)
}

func (h *Handler) remove(w nethttp.ResponseWriter, r *nethttp.Request) {
	pid, err := uuid.Parse(chi.URLParam(r, "id"))
	o, ok := owner(r)
	if err == nil && ok {
		if err := h.svc.Remove(r.Context(), o, pid); err != nil {
			h.fail(w, r, err)
			return
		}
	}
	nethttp.Redirect(w, r, "/cart", nethttp.StatusSeeOther)
}

func (h *Handler) fail(w nethttp.ResponseWriter, r *nethttp.Request, err error) {
	h.log.Error("cart http", "path", r.URL.Path, "err", err)
	httpx.Render(w, r, nethttp.StatusInternalServerError, ui.ErrorPage(500, "Something went wrong on our side."))
}
