// Package http serves payment pages and the provider webhook.
package http

import (
	"errors"
	"io"
	"log/slog"
	nethttp "net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/payment/app"
	"github.com/NaheedRayan/goat-architecture/internal/payment/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/auth"
	"github.com/NaheedRayan/goat-architecture/internal/platform/httpx"
	"github.com/NaheedRayan/goat-architecture/internal/platform/ui"
)

type Handler struct {
	svc         *app.Service
	mockEnabled bool
	log         *slog.Logger
}

func NewHandler(svc *app.Service, mockEnabled bool, log *slog.Logger) *Handler {
	return &Handler{svc: svc, mockEnabled: mockEnabled, log: log}
}

func (h *Handler) Routes(r chi.Router) {
	r.Post("/webhooks/payment", h.webhook)
	r.Group(func(r chi.Router) {
		r.Use(auth.Require)
		r.Get("/pay/{id}", h.start)
		if h.mockEnabled {
			r.Get("/pay/{id}/mock", h.mockPage)
			r.Post("/pay/{id}/mock", h.mockConfirm)
		}
	})
}

func paymentID(r *nethttp.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	return id, err == nil
}

// start sends the shopper to the provider's checkout.
func (h *Handler) start(w nethttp.ResponseWriter, r *nethttp.Request) {
	pid, ok := paymentID(r)
	if !ok {
		nethttp.NotFound(w, r)
		return
	}
	c, _ := auth.FromContext(r.Context())
	p, sess, err := h.svc.Checkout(r.Context(), pid, c.UserID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		httpx.Render(w, r, nethttp.StatusNotFound, ui.ErrorPage(404, "Payment not found."))
	case errors.Is(err, domain.ErrAlreadySettled):
		nethttp.Redirect(w, r, "/orders/"+p.OrderID.String(), nethttp.StatusSeeOther)
	case err != nil:
		h.fail(w, r, err)
	default:
		nethttp.Redirect(w, r, sess.URL, nethttp.StatusSeeOther)
	}
}

func (h *Handler) mockPage(w nethttp.ResponseWriter, r *nethttp.Request) {
	pid, ok := paymentID(r)
	if !ok {
		nethttp.NotFound(w, r)
		return
	}
	c, _ := auth.FromContext(r.Context())
	p, _, err := h.svc.Checkout(r.Context(), pid, c.UserID)
	if errors.Is(err, domain.ErrAlreadySettled) {
		nethttp.Redirect(w, r, "/orders/"+p.OrderID.String(), nethttp.StatusSeeOther)
		return
	}
	if errors.Is(err, domain.ErrNotFound) {
		httpx.Render(w, r, nethttp.StatusNotFound, ui.ErrorPage(404, "Payment not found."))
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.Render(w, r, nethttp.StatusOK, MockCheckoutPage(p))
}

func (h *Handler) mockConfirm(w nethttp.ResponseWriter, r *nethttp.Request) {
	pid, ok := paymentID(r)
	if !ok {
		nethttp.NotFound(w, r)
		return
	}
	c, _ := auth.FromContext(r.Context())
	p, err := h.svc.Get(r.Context(), pid)
	if err != nil || p.UserID != c.UserID {
		httpx.Render(w, r, nethttp.StatusNotFound, ui.ErrorPage(404, "Payment not found."))
		return
	}
	if _, err := h.svc.Settle(r.Context(), pid, r.FormValue("outcome") == "succeeded", "mock_"+pid.String()); err != nil {
		h.fail(w, r, err)
		return
	}
	nethttp.Redirect(w, r, "/orders/"+p.OrderID.String(), nethttp.StatusSeeOther)
}

func (h *Handler) webhook(w nethttp.ResponseWriter, r *nethttp.Request) {
	body, err := io.ReadAll(nethttp.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		nethttp.Error(w, "bad request", nethttp.StatusBadRequest)
		return
	}
	switch err := h.svc.HandleWebhook(r.Context(), r.Header, body); {
	case errors.Is(err, domain.ErrInvalidSignature):
		nethttp.Error(w, "invalid signature", nethttp.StatusUnauthorized)
	case errors.Is(err, domain.ErrNotFound):
		nethttp.Error(w, "unknown payment", nethttp.StatusNotFound)
	case err != nil:
		h.log.Error("payment webhook", "err", err)
		nethttp.Error(w, "error", nethttp.StatusInternalServerError)
	default:
		w.WriteHeader(nethttp.StatusNoContent)
	}
}

func (h *Handler) fail(w nethttp.ResponseWriter, r *nethttp.Request, err error) {
	h.log.Error("payment http", "path", r.URL.Path, "err", err)
	httpx.Render(w, r, nethttp.StatusInternalServerError, ui.ErrorPage(500, "Something went wrong on our side."))
}
