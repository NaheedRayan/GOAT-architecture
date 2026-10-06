// Package http serves checkout and the customer's order pages.
package http

import (
	"errors"
	"log/slog"
	nethttp "net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/order/app"
	"github.com/NaheedRayan/goat-architecture/internal/order/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/auth"
	"github.com/NaheedRayan/goat-architecture/internal/platform/httpx"
	"github.com/NaheedRayan/goat-architecture/internal/platform/id"
	"github.com/NaheedRayan/goat-architecture/internal/platform/ui"
)

type Handler struct {
	svc       *app.Service
	cartOwner func(userID uuid.UUID) string
	log       *slog.Logger
}

func NewHandler(svc *app.Service, cartOwner func(uuid.UUID) string, log *slog.Logger) *Handler {
	return &Handler{svc: svc, cartOwner: cartOwner, log: log}
}

func (h *Handler) Routes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.Require)
		r.Get("/checkout", h.checkoutForm)
		r.Post("/checkout", h.checkout)
		r.Get("/orders", h.list)
		r.Get("/orders/{id}", h.detail)
		r.Post("/orders/{id}/cancel", h.cancel)
	})
}

func claims(r *nethttp.Request) auth.Claims {
	c, _ := auth.FromContext(r.Context())
	return c
}

func (h *Handler) renderCheckout(w nethttp.ResponseWriter, r *nethttp.Request, f ShippingForm, errMsg string, status int) {
	c := claims(r)
	cart, err := h.svc.Cart(r.Context(), h.cartOwner(c.UserID))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if len(cart.Lines) == 0 {
		nethttp.Redirect(w, r, "/cart", nethttp.StatusSeeOther)
		return
	}
	saved, err := h.svc.SavedAddresses(r.Context(), c.UserID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if f.Key == "" {
		f.Key = id.New().String()
	}
	if f.blank() { // first visit: prefill with the default address, never overwrite typed input
		for _, a := range saved {
			if a.IsDefault {
				f.FullName, f.Phone, f.Line1, f.Line2 = a.FullName, a.Phone, a.Line1, a.Line2
				f.City, f.PostalCode, f.Country = a.City, a.PostalCode, a.Country
			}
		}
	}
	httpx.Render(w, r, status, CheckoutPage(cart, saved, f, errMsg))
}

func (h *Handler) checkoutForm(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.renderCheckout(w, r, ShippingForm{}, "", nethttp.StatusOK)
}

func (h *Handler) checkout(w nethttp.ResponseWriter, r *nethttp.Request) {
	c := claims(r)
	f := ShippingForm{
		Key: r.FormValue("idempotency_key"), FullName: r.FormValue("full_name"), Phone: r.FormValue("phone"),
		Line1: r.FormValue("line1"), Line2: r.FormValue("line2"), City: r.FormValue("city"),
		PostalCode: r.FormValue("postal_code"), Country: r.FormValue("country"),
	}
	expected, _ := strconv.ParseInt(r.FormValue("expected_total"), 10, 64)
	o, err := h.svc.Checkout(r.Context(), app.CheckoutInput{
		UserID: c.UserID, CartOwner: h.cartOwner(c.UserID), IdempotencyKey: f.Key, ExpectedTotalCents: expected,
		Shipping: domain.ShippingAddress{FullName: f.FullName, Phone: f.Phone, Line1: f.Line1, Line2: f.Line2,
			City: f.City, PostalCode: f.PostalCode, Country: f.Country},
	})
	var ve domain.ValidationError
	var oos domain.InsufficientStockError
	var changed domain.PriceChangedError
	switch {
	case errors.As(err, &changed):
		// Nothing was charged or reserved; show the new total and let the shopper confirm.
		h.renderCheckout(w, r, f, "Prices in your cart changed, so the total is now different. Please review it and place the order again.", nethttp.StatusConflict)
	case errors.Is(err, domain.ErrOrderCancelled):
		f.Key = "" // a fresh attempt needs a fresh key, but keep what was typed
		h.renderCheckout(w, r, f, "That order was cancelled. Review your cart and place a new order.", nethttp.StatusConflict)
	case errors.Is(err, domain.ErrEmptyCart):
		nethttp.Redirect(w, r, "/cart", nethttp.StatusSeeOther)
	case errors.As(err, &ve):
		h.renderCheckout(w, r, f, ve.Error(), nethttp.StatusUnprocessableEntity)
	case errors.As(err, &oos):
		// The transaction rolled back, so the key and the typed address stay valid.
		h.renderCheckout(w, r, f, oos.Error()+". Update your cart and try again.", nethttp.StatusConflict)
	case err != nil:
		h.fail(w, r, err)
	case o.Status == domain.StatusAwaitingPayment && o.PaymentID != nil:
		nethttp.Redirect(w, r, "/pay/"+o.PaymentID.String(), nethttp.StatusSeeOther)
	default:
		nethttp.Redirect(w, r, "/orders/"+o.ID.String(), nethttp.StatusSeeOther)
	}
}

func (h *Handler) list(w nethttp.ResponseWriter, r *nethttp.Request) {
	orders, err := h.svc.ListByUser(r.Context(), claims(r).UserID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.Render(w, r, nethttp.StatusOK, ListPage(orders))
}

func (h *Handler) detail(w nethttp.ResponseWriter, r *nethttp.Request) {
	oid, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Render(w, r, nethttp.StatusNotFound, ui.ErrorPage(404, "Order not found."))
		return
	}
	o, err := h.svc.GetForUser(r.Context(), claims(r).UserID, oid)
	if errors.Is(err, domain.ErrNotFound) {
		httpx.Render(w, r, nethttp.StatusNotFound, ui.ErrorPage(404, "Order not found."))
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.Render(w, r, nethttp.StatusOK, DetailPage(o))
}

func (h *Handler) cancel(w nethttp.ResponseWriter, r *nethttp.Request) {
	oid, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		nethttp.NotFound(w, r)
		return
	}
	switch err := h.svc.Cancel(r.Context(), claims(r).UserID, oid); {
	case errors.Is(err, domain.ErrNotFound):
		httpx.Render(w, r, nethttp.StatusNotFound, ui.ErrorPage(404, "Order not found."))
		return
	case errors.Is(err, domain.ErrInvalidTransition), errors.Is(err, domain.ErrPaymentReceived):
		// Already paid or shipped (or paying right now): just show the order as it is.
	case err != nil:
		h.fail(w, r, err)
		return
	}
	nethttp.Redirect(w, r, "/orders/"+oid.String(), nethttp.StatusSeeOther)
}

func (h *Handler) fail(w nethttp.ResponseWriter, r *nethttp.Request, err error) {
	h.log.Error("order http", "path", r.URL.Path, "err", err)
	httpx.Render(w, r, nethttp.StatusInternalServerError, ui.ErrorPage(500, "Something went wrong on our side."))
}
