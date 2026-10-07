package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/NaheedRayan/goat-architecture/internal/platform/httpx"
	"github.com/NaheedRayan/goat-architecture/internal/platform/money"
	"github.com/NaheedRayan/goat-architecture/internal/shipping"
)

func (m *Module) shippingMethods(w http.ResponseWriter, r *http.Request) {
	m.renderShipping(w, r, "", http.StatusOK)
}

func (m *Module) renderShipping(w http.ResponseWriter, r *http.Request, msg string, status int) {
	ms, err := m.o.Shipping.All(r.Context())
	if err != nil {
		m.fail(w, r, err)
		return
	}
	httpx.Render(w, r, status, ShippingPage(ms, msg))
}

func readMethod(r *http.Request) (shipping.Method, error) {
	m := shipping.Method{
		Name: r.FormValue("name"), Description: r.FormValue("description"), Active: r.FormValue("active") != "",
	}
	price, err := money.ParseCents(r.FormValue("price"))
	if err != nil {
		return m, shipping.ValidationError("price must be a number like 5.00")
	}
	m.PriceCents = price
	if raw := strings.TrimSpace(r.FormValue("free_over")); raw != "" {
		c, err := money.ParseCents(raw)
		if err != nil {
			return m, shipping.ValidationError("the free-shipping threshold must be a number like 100.00, or empty")
		}
		m.FreeOverCents = &c
	}
	if m.MinDays, err = optInt(r.FormValue("min_days")); err != nil {
		return m, shipping.ValidationError("minimum days must be a whole number")
	}
	if m.MaxDays, err = optInt(r.FormValue("max_days")); err != nil {
		return m, shipping.ValidationError("maximum days must be a whole number")
	}
	return m, nil
}

// optInt parses a whole number; an empty field is zero.
func optInt(s string) (int, error) {
	if s = strings.TrimSpace(s); s == "" {
		return 0, nil
	}
	return strconv.Atoi(s)
}

func (m *Module) createShipping(w http.ResponseWriter, r *http.Request) {
	method, err := readMethod(r)
	if err == nil {
		method, err = m.o.Shipping.Create(r.Context(), method)
	}
	m.shippingResult(w, r, "shipping.create", method.ID.String(), err)
}

func (m *Module) updateShipping(w http.ResponseWriter, r *http.Request) {
	id, ok := uuidParam(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	method, err := readMethod(r)
	method.ID = id
	if err == nil {
		err = m.o.Shipping.Update(r.Context(), method)
	}
	m.shippingResult(w, r, "shipping.update", id.String(), err)
}

func (m *Module) deleteShipping(w http.ResponseWriter, r *http.Request) {
	id, ok := uuidParam(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	err := m.o.Shipping.Delete(r.Context(), id)
	if errors.Is(err, shipping.ErrNotFound) {
		err = nil // deleting twice is harmless
	}
	m.shippingResult(w, r, "shipping.delete", id.String(), err)
}

func (m *Module) shippingResult(w http.ResponseWriter, r *http.Request, action, id string, err error) {
	var ve shipping.ValidationError
	switch {
	case err == nil:
		m.audit(r, action, "shipping_method", id, nil)
		http.Redirect(w, r, "/admin/shipping", http.StatusSeeOther)
	case errors.As(err, &ve):
		m.renderShipping(w, r, ve.Error(), http.StatusUnprocessableEntity)
	case errors.Is(err, shipping.ErrNotFound):
		http.NotFound(w, r)
	default:
		m.fail(w, r, err)
	}
}
