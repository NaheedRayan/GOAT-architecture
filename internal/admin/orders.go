package admin

import (
	"context"
	"encoding/csv"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/order"
	"github.com/NaheedRayan/goat-architecture/internal/platform/httpx"
	"github.com/NaheedRayan/goat-architecture/internal/platform/money"
)

const ordersPerPage = 50

// OrderFilter is the search form, round-tripped through the query string.
type OrderFilter struct {
	Status  string
	Query   string
	From    string // YYYY-MM-DD
	To      string // YYYY-MM-DD, inclusive
	Returns bool   // only orders with a pending return request
}

func (f OrderFilter) values(page int) url.Values {
	v := url.Values{}
	if f.Status != "" {
		v.Set("status", f.Status)
	}
	if f.Query != "" {
		v.Set("q", f.Query)
	}
	if f.From != "" {
		v.Set("from", f.From)
	}
	if f.To != "" {
		v.Set("to", f.To)
	}
	if f.Returns {
		v.Set("returns", "1")
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	return v
}

func parseDay(s string) (*time.Time, bool) {
	if s == "" {
		return nil, true
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return nil, false
	}
	return &t, true
}

// filterFrom reads and validates the search form. Invalid dates are ignored, not errors.
func filterFrom(r *http.Request) (order.ListFilter, OrderFilter) {
	q := r.URL.Query()
	f := OrderFilter{Query: strings.TrimSpace(q.Get("q")), From: q.Get("from"), To: q.Get("to"), Returns: q.Get("returns") == "1"}
	for _, s := range orderStatuses {
		if q.Get("status") == s {
			f.Status = s
		}
	}
	lf := order.ListFilter{Status: f.Status, Query: f.Query}
	if f.Returns {
		lf.ReturnStatus = order.ReturnRequested
	}
	if t, ok := parseDay(f.From); ok {
		lf.From = t
	} else {
		f.From = ""
	}
	if t, ok := parseDay(f.To); ok {
		if t != nil {
			end := t.AddDate(0, 0, 1) // inclusive end date
			lf.To = &end
		}
	} else {
		f.To = ""
	}
	return lf, f
}

func (m *Module) orders(w http.ResponseWriter, r *http.Request) {
	lf, f := filterFrom(r)
	page := min(atoi(r.URL.Query().Get("page"), 1), 10000)
	// Ask for one extra row to learn whether a next page exists.
	os, err := m.o.Orders.List(r.Context(), lf, ordersPerPage+1, (page-1)*ordersPerPage)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	hasNext := len(os) > ordersPerPage
	if hasNext {
		os = os[:ordersPerPage]
	}
	pending, _ := m.o.Orders.PendingReturns(r.Context())
	httpx.Render(w, r, http.StatusOK, OrdersPage(OrdersVM{Orders: os, Filter: f, Page: page, HasNext: hasNext, PendingReturns: pending}))
}

// csvSafe neutralises spreadsheet formula injection: a cell that starts with = + - @
// (or a tab/CR) would be executed by Excel or Sheets, so it gets a leading apostrophe.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func (m *Module) exportOrders(w http.ResponseWriter, r *http.Request) {
	lf, f := filterFrom(r)
	const maxRows = 10000
	os, err := m.o.Orders.List(r.Context(), lf, maxRows, 0)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	m.audit(r, "orders.export", "orders", "", map[string]any{"rows": len(os), "filter": f.values(1).Encode()})
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="orders-`+time.Now().UTC().Format("20060102")+`.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"order", "placed_at_utc", "status", "customer", "email", "phone", "city", "country", "subtotal", "discount",
		"shipping", "tax", "total", "refunded", "currency", "payment_method", "shipping_method", "coupon", "carrier", "tracking_number"})
	dec := func(c int64) string { return money.Decimal(c) }
	for _, o := range os {
		_ = cw.Write([]string{
			o.ID.String(), o.CreatedAt.UTC().Format(time.RFC3339), o.Status, csvSafe(o.Shipping.FullName), csvSafe(o.ContactEmail),
			csvSafe(o.Shipping.Phone), csvSafe(o.Shipping.City), csvSafe(o.Shipping.Country), dec(o.SubtotalCents), dec(o.DiscountCents),
			dec(o.ShippingCents), dec(o.TaxCents), dec(o.TotalCents), dec(o.RefundCents), o.Currency, o.PaymentMethod,
			csvSafe(o.ShippingMethod), csvSafe(o.CouponCode), csvSafe(o.Tracking.Carrier), csvSafe(o.Tracking.Number),
		})
	}
	cw.Flush()
}

// ---- one order ----

func (m *Module) orderDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := uuidParam(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	m.renderOrder(w, r, id, doneMessage(r.URL.Query().Get("done")), "", http.StatusOK)
}

func (m *Module) renderOrder(w http.ResponseWriter, r *http.Request, id uuid.UUID, ok, errMsg string, status int) {
	o, err := m.o.Orders.Get(r.Context(), id)
	if errors.Is(err, order.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		m.fail(w, r, err)
		return
	}
	httpx.Render(w, r, status, OrderDetailPage(OrderVM{
		Order: o, IsAdmin: isAdmin(r), Done: ok, Error: errMsg, ReturnDeadline: m.o.Orders.ReturnDeadline(o),
	}))
}

func doneMessage(code string) string {
	switch code {
	case "shipped":
		return "Marked as shipped. The customer was emailed."
	case "delivered":
		return "Marked as delivered."
	case "cancelled":
		return "Order cancelled."
	case "refunded":
		return "Order refunded."
	case "note":
		return "Note saved."
	case "return-approved":
		return "Return approved and refunded."
	case "return-rejected":
		return "Return declined. The customer was emailed."
	}
	return ""
}

// act runs one order action and shows the outcome on the order page.
func (m *Module) act(w http.ResponseWriter, r *http.Request, code string, detail map[string]any,
	fn func(ctx context.Context, actor, id uuid.UUID) error) {
	id, ok := uuidParam(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	err := fn(r.Context(), actor(r).UserID, id)
	var ve order.ValidationError
	switch {
	case err == nil:
		m.audit(r, "order."+code, "order", id.String(), detail)
		http.Redirect(w, r, "/admin/orders/"+id.String()+"?done="+code, http.StatusSeeOther)
	case errors.Is(err, order.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, order.ErrInvalidTransition), errors.Is(err, order.ErrPaymentReceived):
		m.renderOrder(w, r, id, "", "That can't be done in the order's current state (it may have just changed). Review it and try again.", http.StatusConflict)
	case errors.As(err, &ve):
		m.renderOrder(w, r, id, "", ve.Error(), http.StatusUnprocessableEntity)
	default:
		m.fail(w, r, err)
	}
}

func (m *Module) ship(w http.ResponseWriter, r *http.Request) {
	t := order.Tracking{Carrier: r.FormValue("carrier"), Number: r.FormValue("number"), URL: r.FormValue("url")}
	m.act(w, r, "shipped", map[string]any{"carrier": t.Carrier, "number": t.Number},
		func(ctx context.Context, a, id uuid.UUID) error { return m.o.Orders.Ship(ctx, a, id, t) })
}

func (m *Module) deliver(w http.ResponseWriter, r *http.Request) {
	m.act(w, r, "delivered", nil, func(ctx context.Context, a, id uuid.UUID) error { return m.o.Orders.Deliver(ctx, a, id) })
}

// cancelOrder: staff may cancel unpaid orders; cancelling a paid order refunds money, so it needs an admin.
func (m *Module) cancelOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := uuidParam(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !isAdmin(r) {
		o, err := m.o.Orders.Get(r.Context(), id)
		if errors.Is(err, order.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			m.fail(w, r, err)
			return
		}
		if o.Status != order.StatusAwaitingPayment {
			m.forbid(w, r)
			return
		}
	}
	reason := r.FormValue("reason")
	m.act(w, r, "cancelled", map[string]any{"reason": reason},
		func(ctx context.Context, a, id uuid.UUID) error { return m.o.Orders.CancelByStaff(ctx, a, id, reason) })
}

func (m *Module) refundOrder(w http.ResponseWriter, r *http.Request) {
	restock, reason := r.FormValue("restock") == "1", r.FormValue("reason")
	m.act(w, r, "refunded", map[string]any{"restock": restock, "reason": reason},
		func(ctx context.Context, a, id uuid.UUID) error {
			return m.o.Orders.Refund(ctx, a, id, restock, reason)
		})
}

func (m *Module) orderNote(w http.ResponseWriter, r *http.Request) {
	note := r.FormValue("note")
	m.act(w, r, "note", nil, func(ctx context.Context, a, id uuid.UUID) error { return m.o.Orders.SetNote(ctx, a, id, note) })
}

func (m *Module) approveReturn(w http.ResponseWriter, r *http.Request) {
	restock, note := r.FormValue("restock") == "1", r.FormValue("note")
	m.act(w, r, "return-approved", map[string]any{"restock": restock},
		func(ctx context.Context, a, id uuid.UUID) error {
			return m.o.Orders.ResolveReturn(ctx, a, id, true, note, restock)
		})
}

func (m *Module) rejectReturn(w http.ResponseWriter, r *http.Request) {
	note := r.FormValue("note")
	m.act(w, r, "return-rejected", map[string]any{"note": note},
		func(ctx context.Context, a, id uuid.UUID) error {
			return m.o.Orders.ResolveReturn(ctx, a, id, false, note, false)
		})
}
