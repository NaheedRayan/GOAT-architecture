// Package http serves checkout and the customer's order pages.
package http

import (
	"errors"
	"log/slog"
	nethttp "net/http"
	"strconv"
	"strings"
	"time"

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
	svc          *app.Service
	cartOwnerFor func(r *nethttp.Request) (string, bool)
	guestSignIn  func(w nethttp.ResponseWriter, r *nethttp.Request, email, name string) (uuid.UUID, error)
	isGuestErr   func(error) (GuestIssue, bool) // maps a guest sign-in failure to something to show the shopper
	log          *slog.Logger
}

// GuestIssue is a reason guest checkout could not create the account.
type GuestIssue struct {
	Msg    string
	Status int  // HTTP status to answer with
	SignIn bool // offer a "sign in" button (the email already has an account)
}

func NewHandler(svc *app.Service, cartOwnerFor func(*nethttp.Request) (string, bool),
	guestSignIn func(nethttp.ResponseWriter, *nethttp.Request, string, string) (uuid.UUID, error),
	isGuestErr func(error) (GuestIssue, bool), log *slog.Logger) *Handler {
	return &Handler{svc: svc, cartOwnerFor: cartOwnerFor, guestSignIn: guestSignIn, isGuestErr: isGuestErr, log: log}
}

func (h *Handler) Routes(r chi.Router) {
	r.Get("/checkout", h.checkoutForm)
	r.Post("/checkout", h.checkout)
	r.Group(func(r chi.Router) {
		r.Use(auth.Require)
		r.Get("/orders", h.list)
		r.Get("/orders/{id}", h.detail)
		r.Post("/orders/{id}/cancel", h.cancel)
		r.Post("/orders/{id}/return", h.requestReturn)
	})
}

func claims(r *nethttp.Request) (auth.Claims, bool) { return auth.FromContext(r.Context()) }

// ---- checkout ----

// CheckoutForm carries every field the checkout page posts, so a re-render keeps what was typed.
type CheckoutForm struct {
	Key            string
	Email, Name    string // guests only
	FullName       string
	Phone          string
	Line1, Line2   string
	City           string
	PostalCode     string
	Country        string
	ShippingMethod string
	Coupon         string
	PaymentMethod  string
}

func (f CheckoutForm) blankAddress() bool {
	return f.FullName == "" && f.Phone == "" && f.Line1 == "" && f.Line2 == "" && f.City == "" && f.PostalCode == "" && f.Country == ""
}

func readForm(r *nethttp.Request) CheckoutForm {
	v := r.FormValue
	return CheckoutForm{
		Key: v("idempotency_key"), Email: strings.TrimSpace(v("email")), Name: strings.TrimSpace(v("name")),
		FullName: v("full_name"), Phone: v("phone"), Line1: v("line1"), Line2: v("line2"), City: v("city"),
		PostalCode: v("postal_code"), Country: v("country"), ShippingMethod: v("shipping_method"),
		Coupon: strings.TrimSpace(v("coupon")), PaymentMethod: v("payment_method"),
	}
}

func (f CheckoutForm) address() domain.ShippingAddress {
	return domain.ShippingAddress{FullName: f.FullName, Phone: f.Phone, Line1: f.Line1, Line2: f.Line2, City: f.City, PostalCode: f.PostalCode, Country: f.Country}
}

// CheckoutVM is everything the checkout page shows.
type CheckoutVM struct {
	Quote    app.Quote
	Form     CheckoutForm
	Guest    bool
	Saved    []app.SavedAddress
	Error    string
	SignIn   bool // show a "sign in" prompt (the guest's email already has an account)
	Currency string
}

// who identifies the shopper: the signed-in user, or an anonymous visitor with a guest cart.
func (h *Handler) who(r *nethttp.Request) (userID uuid.UUID, owner string, ok bool) {
	if c, authed := claims(r); authed {
		return c.UserID, "user:" + c.UserID.String(), true
	}
	owner, ok = h.cartOwnerFor(r)
	return uuid.Nil, owner, ok
}

func (h *Handler) render(w nethttp.ResponseWriter, r *nethttp.Request, f CheckoutForm, msg string, status int, signIn bool) {
	userID, owner, ok := h.who(r)
	if !ok {
		nethttp.Redirect(w, r, "/cart", nethttp.StatusSeeOther)
		return
	}
	methodID, _ := uuid.Parse(f.ShippingMethod)
	quote, err := h.svc.Quote(r.Context(), app.QuoteInput{UserID: userID, CartOwner: owner, ShippingMethodID: methodID, CouponCode: f.Coupon, PaymentMethod: f.PaymentMethod})
	if errors.Is(err, domain.ErrEmptyCart) {
		nethttp.Redirect(w, r, "/cart", nethttp.StatusSeeOther)
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	vm := CheckoutVM{Quote: quote, Form: f, Guest: userID == uuid.Nil, Error: msg, SignIn: signIn, Currency: quote.Cart.Currency}
	// What the page shows is the server's choice, so the posted values always match the displayed total.
	vm.Form.PaymentMethod = quote.PaymentMethod
	if quote.Shipping != nil {
		vm.Form.ShippingMethod = quote.Shipping.ID.String()
	}
	if vm.Form.Key == "" {
		vm.Form.Key = id.New().String()
	}
	if !vm.Guest {
		if vm.Saved, err = h.svc.SavedAddresses(r.Context(), userID); err != nil {
			h.fail(w, r, err)
			return
		}
		if vm.Form.blankAddress() { // first visit: prefill with the default address, never overwrite typed input
			for _, a := range vm.Saved {
				if a.IsDefault {
					vm.Form.FullName, vm.Form.Phone, vm.Form.Line1, vm.Form.Line2 = a.FullName, a.Phone, a.Line1, a.Line2
					vm.Form.City, vm.Form.PostalCode, vm.Form.Country = a.City, a.PostalCode, a.Country
				}
			}
		}
	}
	httpx.Render(w, r, status, CheckoutPage(vm))
}

func (h *Handler) checkoutForm(w nethttp.ResponseWriter, r *nethttp.Request) {
	h.render(w, r, CheckoutForm{Coupon: strings.TrimSpace(r.URL.Query().Get("coupon"))}, "", nethttp.StatusOK, false)
}

func (h *Handler) checkout(w nethttp.ResponseWriter, r *nethttp.Request) {
	f := readForm(r)
	if r.FormValue("action") == "refresh" { // totals update (shipping, payment method, coupon): nothing is placed
		h.render(w, r, f, "", nethttp.StatusOK, false)
		return
	}
	userID, owner, ok := h.who(r)
	if !ok {
		nethttp.Redirect(w, r, "/cart", nethttp.StatusSeeOther)
		return
	}
	methodID, _ := uuid.Parse(f.ShippingMethod)
	expected, _ := strconv.ParseInt(r.FormValue("expected_total"), 10, 64)
	in := app.CheckoutInput{
		UserID: userID, CartOwner: owner, IdempotencyKey: f.Key, Shipping: f.address(), ShippingMethodID: methodID,
		CouponCode: f.Coupon, PaymentMethod: f.PaymentMethod, ExpectedTotalCents: expected,
	}

	if userID == uuid.Nil { // buying without an account: check everything first, then create the account
		if f.Email == "" || f.Name == "" {
			h.render(w, r, f, "Enter your name and email so we can send your order confirmation.", nethttp.StatusUnprocessableEntity, false)
			return
		}
		if _, err := h.svc.Preflight(r.Context(), in); err != nil {
			h.checkoutError(w, r, f, err)
			return
		}
		uid, err := h.guestSignIn(w, r, f.Email, f.Name)
		if issue, isGuest := h.isGuestErr(err); isGuest {
			h.render(w, r, f, issue.Msg, issue.Status, issue.SignIn)
			return
		}
		if err != nil {
			h.fail(w, r, err)
			return
		}
		in.UserID = uid
	}

	o, err := h.svc.Checkout(r.Context(), in)
	if err != nil {
		h.checkoutError(w, r, f, err)
		return
	}
	switch {
	case o.Status == domain.StatusAwaitingPayment && o.PaymentID != nil:
		nethttp.Redirect(w, r, "/pay/"+o.PaymentID.String(), nethttp.StatusSeeOther)
	default:
		nethttp.Redirect(w, r, "/orders/"+o.ID.String()+"?placed=1", nethttp.StatusSeeOther)
	}
}

func (h *Handler) checkoutError(w nethttp.ResponseWriter, r *nethttp.Request, f CheckoutForm, err error) {
	var ve domain.ValidationError
	var oos domain.InsufficientStockError
	var changed domain.PriceChangedError
	var coupon domain.CouponRejectedError
	switch {
	case errors.Is(err, domain.ErrEmptyCart):
		nethttp.Redirect(w, r, "/cart", nethttp.StatusSeeOther)
	case errors.As(err, &changed):
		// Nothing was charged or reserved; the page now shows the new total for the shopper to confirm.
		h.render(w, r, f, "Prices in your cart changed, so the total is now different. Please review it and place the order again.", nethttp.StatusConflict, false)
	case errors.As(err, &coupon):
		h.render(w, r, f, coupon.Msg, nethttp.StatusUnprocessableEntity, false)
	case errors.As(err, &ve):
		h.render(w, r, f, ve.Error(), nethttp.StatusUnprocessableEntity, false)
	case errors.Is(err, domain.ErrPaymentMethod), errors.Is(err, domain.ErrShippingMethod):
		h.render(w, r, f, err.Error(), nethttp.StatusUnprocessableEntity, false)
	case errors.As(err, &oos):
		// The transaction rolled back, so the key and the typed address stay valid.
		h.render(w, r, f, oos.Error()+". Update your cart and try again.", nethttp.StatusConflict, false)
	case errors.Is(err, domain.ErrOrderCancelled):
		f.Key = "" // a fresh attempt needs a fresh key, but keep what was typed
		h.render(w, r, f, "That order was cancelled. Review your cart and place a new order.", nethttp.StatusConflict, false)
	default:
		h.fail(w, r, err)
	}
}

// ---- orders ----

func (h *Handler) list(w nethttp.ResponseWriter, r *nethttp.Request) {
	c, _ := claims(r)
	orders, err := h.svc.ListByUser(r.Context(), c.UserID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.Render(w, r, nethttp.StatusOK, ListPage(orders))
}

func (h *Handler) loadOrder(w nethttp.ResponseWriter, r *nethttp.Request) (domain.Order, bool) {
	c, _ := claims(r)
	oid, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Render(w, r, nethttp.StatusNotFound, ui.ErrorPage(404, "Order not found."))
		return domain.Order{}, false
	}
	o, err := h.svc.GetForUser(r.Context(), c.UserID, oid)
	if errors.Is(err, domain.ErrNotFound) {
		httpx.Render(w, r, nethttp.StatusNotFound, ui.ErrorPage(404, "Order not found."))
		return domain.Order{}, false
	}
	if err != nil {
		h.fail(w, r, err)
		return domain.Order{}, false
	}
	return o, true
}

// DetailVM is what the order page shows.
type DetailVM struct {
	Order          domain.Order
	Placed         bool
	CanCancel      bool
	CanReturn      bool
	ReturnDeadline time.Time
	ReturnDays     int
	TaxLabel       string
	Notice         string
	Error          string
}

func (h *Handler) detailVM(r *nethttp.Request, o domain.Order) DetailVM {
	st := h.svc.StoreSettings()
	now := time.Now()
	return DetailVM{
		Order: o, Placed: r.URL.Query().Get("placed") == "1", CanCancel: o.CanCustomerCancel(),
		CanReturn: o.CanRequestReturn(now, st.ReturnWindowDays), ReturnDeadline: o.ReturnDeadline(st.ReturnWindowDays),
		ReturnDays: st.ReturnWindowDays, TaxLabel: st.TaxLabel,
	}
}

func (h *Handler) detail(w nethttp.ResponseWriter, r *nethttp.Request) {
	o, ok := h.loadOrder(w, r)
	if !ok {
		return
	}
	httpx.Render(w, r, nethttp.StatusOK, DetailPage(h.detailVM(r, o)))
}

func (h *Handler) cancel(w nethttp.ResponseWriter, r *nethttp.Request) {
	c, _ := claims(r)
	oid, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		nethttp.NotFound(w, r)
		return
	}
	switch err := h.svc.CancelByCustomer(r.Context(), c.UserID, oid); {
	case errors.Is(err, domain.ErrNotFound):
		httpx.Render(w, r, nethttp.StatusNotFound, ui.ErrorPage(404, "Order not found."))
		return
	case errors.Is(err, domain.ErrInvalidTransition), errors.Is(err, domain.ErrPaymentReceived):
		// Already being prepared (or paying right now): show the order as it is.
	case err != nil:
		h.fail(w, r, err)
		return
	}
	nethttp.Redirect(w, r, "/orders/"+oid.String(), nethttp.StatusSeeOther)
}

func (h *Handler) requestReturn(w nethttp.ResponseWriter, r *nethttp.Request) {
	c, _ := claims(r)
	oid, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		nethttp.NotFound(w, r)
		return
	}
	err = h.svc.RequestReturn(r.Context(), c.UserID, oid, r.FormValue("reason"))
	var ve domain.ValidationError
	switch {
	case errors.Is(err, domain.ErrNotFound):
		httpx.Render(w, r, nethttp.StatusNotFound, ui.ErrorPage(404, "Order not found."))
		return
	case errors.As(err, &ve), errors.Is(err, domain.ErrReturnWindow):
		o, ok := h.loadOrder(w, r)
		if !ok {
			return
		}
		vm := h.detailVM(r, o)
		vm.Error = err.Error()
		httpx.Render(w, r, nethttp.StatusUnprocessableEntity, DetailPage(vm))
		return
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
