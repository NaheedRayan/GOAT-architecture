package admin

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/NaheedRayan/goat-architecture/internal/platform/httpx"
	"github.com/NaheedRayan/goat-architecture/internal/platform/money"
	"github.com/NaheedRayan/goat-architecture/internal/promotion"
)

func (m *Module) coupons(w http.ResponseWriter, r *http.Request) {
	m.renderCoupons(w, r, "", http.StatusOK)
}

func (m *Module) renderCoupons(w http.ResponseWriter, r *http.Request, msg string, status int) {
	cs, err := m.o.Promotions.List(r.Context())
	if err != nil {
		m.fail(w, r, err)
		return
	}
	httpx.Render(w, r, status, CouponsPage(cs, msg))
}

func optCount(s string) (*int, error) {
	n, err := optInt(s)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	return &n, nil
}

// readCoupon parses the coupon form. Dates are UTC days: a coupon runs from the start
// of its first day to the end of its last.
func readCoupon(r *http.Request) (promotion.Coupon, error) {
	c := promotion.Coupon{Code: r.FormValue("code"), Kind: r.FormValue("kind"), Active: r.FormValue("active") != ""}
	bad := func(msg string) (promotion.Coupon, error) { return c, promotion.ValidationError(msg) }
	switch c.Kind {
	case promotion.KindPercent:
		n, err := optInt(r.FormValue("value"))
		if err != nil {
			return bad("the percentage must be a whole number")
		}
		c.Value = int64(n)
	case promotion.KindFixed:
		v, err := money.ParseCents(r.FormValue("value"))
		if err != nil {
			return bad("the amount must be a number like 10.00")
		}
		c.Value = v
	}
	if raw := strings.TrimSpace(r.FormValue("min_subtotal")); raw != "" {
		v, err := money.ParseCents(raw)
		if err != nil {
			return bad("the minimum order must be a number like 50.00, or empty")
		}
		c.MinSubtotal = v
	}
	if t, ok := parseDay(r.FormValue("starts")); !ok {
		return bad("the start date is not valid")
	} else if t != nil {
		c.StartsAt = t
	}
	if t, ok := parseDay(r.FormValue("ends")); !ok {
		return bad("the end date is not valid")
	} else if t != nil {
		end := t.Add(24*time.Hour - time.Second)
		c.EndsAt = &end
	}
	var err error
	if c.MaxUses, err = optCount(r.FormValue("max_uses")); err != nil {
		return bad("the total usage limit must be a whole number, or empty")
	}
	if c.MaxUsesPerUser, err = optCount(r.FormValue("max_per_user")); err != nil {
		return bad("the per-customer limit must be a whole number, or empty")
	}
	return c, nil
}

func (m *Module) createCoupon(w http.ResponseWriter, r *http.Request) {
	c, err := readCoupon(r)
	if err == nil {
		c, err = m.o.Promotions.Create(r.Context(), c)
	}
	m.couponResult(w, r, "coupon.create", c.ID.String(), map[string]any{"code": c.Code}, err)
}

func (m *Module) updateCoupon(w http.ResponseWriter, r *http.Request) {
	id, ok := uuidParam(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	cur, err := m.o.Promotions.Get(r.Context(), id)
	if errors.Is(err, promotion.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		m.fail(w, r, err)
		return
	}
	// The code and type are fixed once created; the form only edits the rest.
	form := r.Form
	if form == nil {
		_ = r.ParseForm()
		form = r.Form
	}
	form.Set("code", cur.Code)
	form.Set("kind", cur.Kind)
	c, err := readCoupon(r)
	c.ID = id
	if err == nil {
		err = m.o.Promotions.Update(r.Context(), c)
	}
	m.couponResult(w, r, "coupon.update", id.String(), map[string]any{"code": cur.Code, "active": c.Active}, err)
}

func (m *Module) deleteCoupon(w http.ResponseWriter, r *http.Request) {
	id, ok := uuidParam(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	err := m.o.Promotions.Delete(r.Context(), id)
	if errors.Is(err, promotion.ErrNotFound) {
		err = nil
	}
	m.couponResult(w, r, "coupon.delete", id.String(), nil, err)
}

func (m *Module) couponResult(w http.ResponseWriter, r *http.Request, action, id string, detail map[string]any, err error) {
	var ve promotion.ValidationError
	switch {
	case err == nil:
		m.audit(r, action, "coupon", id, detail)
		http.Redirect(w, r, "/admin/coupons", http.StatusSeeOther)
	case errors.As(err, &ve):
		m.renderCoupons(w, r, ve.Error(), http.StatusUnprocessableEntity)
	case errors.Is(err, promotion.ErrInUse):
		m.renderCoupons(w, r, err.Error(), http.StatusConflict)
	case errors.Is(err, promotion.ErrNotFound):
		http.NotFound(w, r)
	default:
		m.fail(w, r, err)
	}
}
