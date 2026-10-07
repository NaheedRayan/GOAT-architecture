// Package app contains the order use cases: quoting and checkout, the order
// lifecycle (fulfilment, cancel, refund, return) and the event handlers that
// react to payments and stock.
package app

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/order/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/id"
	"github.com/NaheedRayan/goat-architecture/internal/platform/mail"
)

type ListFilter struct {
	Status       string
	ReturnStatus string
	Query        string // order number tail, email, name, phone, tracking number
	From, To     *time.Time
}

type Repository interface {
	Insert(ctx context.Context, o domain.Order) error // domain.ErrDuplicate on (user, key) clash
	Get(ctx context.Context, id uuid.UUID) (domain.Order, error)
	GetForUpdate(ctx context.Context, id uuid.UUID) (domain.Order, error)
	ByIdempotencyKey(ctx context.Context, userID uuid.UUID, key string) (domain.Order, error)
	ListByUser(ctx context.Context, userID uuid.UUID, limit int) ([]domain.Order, error)
	Search(ctx context.Context, f ListFilter, limit, offset int) ([]domain.Order, error)
	CountByStatus(ctx context.Context) (map[string]int, error)
	CountReturnRequests(ctx context.Context) (int, error)
	Save(ctx context.Context, o domain.Order) error
	AddEvent(ctx context.Context, orderID uuid.UUID, kind, detail string, actor *uuid.UUID) error
	ClaimPaid(ctx context.Context, limit int) ([]uuid.UUID, error) // FOR UPDATE SKIP LOCKED
	AnonymizeUser(ctx context.Context, userID uuid.UUID) (int64, error)
	Report(ctx context.Context, from, to time.Time) (domain.Report, error)
	HasPurchased(ctx context.Context, userID, productID uuid.UUID) (bool, error)
}

// Ports to other modules, expressed in the order module's own terms.
type (
	CartLine struct {
		VariantID      uuid.UUID
		ProductID      uuid.UUID
		Name           string
		Label          string
		SKU            string
		UnitPriceCents int64
		Quantity       int
	}
	CartView struct {
		Lines      []CartLine
		TotalCents int64
		Currency   string
	}
	SavedAddress struct {
		FullName, Phone, Line1, Line2, City, PostalCode, Country string
		IsDefault                                                bool
	}
	ShippingOption struct {
		ID          uuid.UUID
		Name        string
		Description string
		PriceCents  int64
		Free        bool
		Eta         string
	}
	AppliedCoupon struct {
		CouponID      uuid.UUID
		Code          string
		DiscountCents int64
		FreeShipping  bool
	}
	Contact struct{ Email, Name string }
)

type Carts interface {
	View(ctx context.Context, owner string) (CartView, error)
	Clear(ctx context.Context, owner string) error
}

type Inventory interface {
	// Reserve returns domain.InsufficientStockError when stock cannot cover the items.
	Reserve(ctx context.Context, orderID uuid.UUID, items []domain.Item) error
	Commit(ctx context.Context, orderID uuid.UUID) error  // domain.ErrStockLost if the hold expired
	Release(ctx context.Context, orderID uuid.UUID) error // unpaid order: give the hold back
	Return(ctx context.Context, orderID uuid.UUID) error  // sold order: restock
}

type Payments interface {
	CreateIntent(ctx context.Context, orderID, userID uuid.UUID, amountCents int64, currency, method string) (uuid.UUID, error)
	// Fail settles a still-pending payment as failed and returns its final status
	// ("succeeded" when the shopper already paid).
	Fail(ctx context.Context, paymentID uuid.UUID) (status string, err error)
	CollectCOD(ctx context.Context, paymentID uuid.UUID) error
	Refund(ctx context.Context, paymentID uuid.UUID) error
}

type Shipping interface {
	Options(ctx context.Context, subtotalCents int64) ([]ShippingOption, error)
}

type Promotions interface {
	// Evaluate returns domain.CouponRejectedError for a code the shopper cannot use.
	Evaluate(ctx context.Context, code string, userID uuid.UUID, subtotalCents, shippingCents int64) (AppliedCoupon, error)
	Redeem(ctx context.Context, a AppliedCoupon, orderID, userID uuid.UUID, subtotalCents, shippingCents int64) error
	Release(ctx context.Context, orderID uuid.UUID) error
}

type Customers interface {
	Contact(ctx context.Context, userID uuid.UUID) (Contact, error)
}

type Addresses interface {
	List(ctx context.Context, userID uuid.UUID) ([]SavedAddress, error)
}

type Mailer interface {
	Send(ctx context.Context, to, subject string, c mail.Content) error
}

type Tx interface {
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// Settings are the store-wide rules the order module applies.
type Settings struct {
	Currency         string
	TaxRateBps       int // 2000 = 20%
	TaxInclusive     bool
	TaxLabel         string
	PaymentMethods   []string // allowed: "card", "cod"
	ReturnWindowDays int
	AlertEmail       string // staff alerts (return requests); empty disables them
	SiteName         string
	Link             func(path string) string
}

// Deps are the collaborators of the order service.
type Deps struct {
	Repo       Repository
	Carts      Carts
	Inventory  Inventory
	Payments   Payments
	Shipping   Shipping
	Promotions Promotions
	Customers  Customers
	Addresses  Addresses
	Mail       Mailer
	Tx         Tx
	Settings   Settings
	Log        *slog.Logger
}

type Service struct{ Deps }

func NewService(d Deps) *Service {
	if d.Settings.TaxLabel == "" {
		d.Settings.TaxLabel = "Tax"
	}
	if d.Settings.ReturnWindowDays <= 0 {
		d.Settings.ReturnWindowDays = 14
	}
	if len(d.Settings.PaymentMethods) == 0 {
		d.Settings.PaymentMethods = []string{domain.MethodCard}
	}
	return &Service{Deps: d}
}

// Event kinds recorded on an order's timeline.
const (
	EvPlaced          = "placed"
	EvPaid            = "paid"
	EvFulfilling      = "fulfilling"
	EvShipped         = "shipped"
	EvDelivered       = "delivered"
	EvCancelled       = "cancelled"
	EvRefunded        = "refunded"
	EvReturnRequested = "return_requested"
	EvReturnApproved  = "return_approved"
	EvReturnRejected  = "return_rejected"
	EvNote            = "note"
)

// ---- quoting ----

type QuoteInput struct {
	UserID           uuid.UUID // uuid.Nil for a visitor who has not signed in
	CartOwner        string
	ShippingMethodID uuid.UUID
	CouponCode       string
	PaymentMethod    string
}

// Quote is a priced view of the cart: what the shopper will pay for the choices made so far.
type Quote struct {
	Cart            CartView
	ShippingOptions []ShippingOption
	Shipping        *ShippingOption // the selected method; nil when the store offers none
	Coupon          *AppliedCoupon
	CouponError     string // why the typed code was refused, if it was
	Pricing         domain.Pricing
	TaxLabel        string
	PaymentMethods  []string
	PaymentMethod   string
}

// Quote prices the cart. It has no side effects, so the checkout page can call it
// on every change (method, coupon) and the server stays the only source of truth.
func (s *Service) Quote(ctx context.Context, in QuoteInput) (Quote, error) {
	cart, err := s.Carts.View(ctx, in.CartOwner)
	if err != nil {
		return Quote{}, err
	}
	if len(cart.Lines) == 0 {
		return Quote{}, domain.ErrEmptyCart
	}
	q := Quote{Cart: cart, TaxLabel: s.Settings.TaxLabel, PaymentMethods: s.Settings.PaymentMethods}

	q.PaymentMethod = in.PaymentMethod
	if !slices.Contains(q.PaymentMethods, q.PaymentMethod) {
		q.PaymentMethod = q.PaymentMethods[0]
	}

	if q.ShippingOptions, err = s.Shipping.Options(ctx, cart.TotalCents); err != nil {
		return Quote{}, err
	}
	if len(q.ShippingOptions) > 0 {
		sel := q.ShippingOptions[0]
		for _, o := range q.ShippingOptions {
			if o.ID == in.ShippingMethodID {
				sel = o
			}
		}
		q.Shipping = &sel
	}
	shipping := int64(0)
	if q.Shipping != nil {
		shipping = q.Shipping.PriceCents
	}

	var discount int64
	if code := strings.TrimSpace(in.CouponCode); code != "" {
		a, err := s.Promotions.Evaluate(ctx, code, in.UserID, cart.TotalCents, shipping)
		var rejected domain.CouponRejectedError
		switch {
		case errors.As(err, &rejected):
			q.CouponError = rejected.Msg
		case err != nil:
			return Quote{}, err
		default:
			q.Coupon = &a
			discount = a.DiscountCents
			if a.FreeShipping {
				shipping = 0
			}
		}
	}
	q.Pricing = domain.Price(cart.TotalCents, discount, shipping, s.Settings.TaxRateBps, s.Settings.TaxInclusive)
	return q, nil
}

// ---- checkout ----

type CheckoutInput struct {
	UserID             uuid.UUID
	CartOwner          string
	IdempotencyKey     string
	Shipping           domain.ShippingAddress
	ShippingMethodID   uuid.UUID
	CouponCode         string
	PaymentMethod      string
	ExpectedTotalCents int64 // the total the shopper was shown; checkout refuses to charge any other
}

// normalize trims the key and fills a missing payment method with the store's first one
// (a client that omits it gets the default; an unknown value is still refused).
func (s *Service) normalize(in CheckoutInput) CheckoutInput {
	in.IdempotencyKey = strings.TrimSpace(in.IdempotencyKey)
	if in.PaymentMethod == "" && len(s.Settings.PaymentMethods) > 0 {
		in.PaymentMethod = s.Settings.PaymentMethods[0]
	}
	return in
}

// Preflight validates a checkout request and prices it, without side effects. The
// HTTP layer runs it before creating a guest account, so nobody gets an account
// for a request that was going to be refused anyway.
func (s *Service) Preflight(ctx context.Context, in CheckoutInput) (Quote, error) {
	in = s.normalize(in)
	if in.IdempotencyKey == "" || len(in.IdempotencyKey) > 100 {
		return Quote{}, domain.ValidationError("invalid checkout request, please reload the page")
	}
	if err := in.Shipping.Validate(); err != nil {
		return Quote{}, err
	}
	if !slices.Contains(s.Settings.PaymentMethods, in.PaymentMethod) {
		return Quote{}, domain.ErrPaymentMethod
	}
	q, err := s.Quote(ctx, QuoteInput{UserID: in.UserID, CartOwner: in.CartOwner, ShippingMethodID: in.ShippingMethodID,
		CouponCode: in.CouponCode, PaymentMethod: in.PaymentMethod})
	if err != nil {
		return Quote{}, err
	}
	if q.CouponError != "" {
		return q, domain.CouponRejectedError{Msg: q.CouponError}
	}
	if len(q.ShippingOptions) > 0 && in.ShippingMethodID != uuid.Nil && q.Shipping.ID != in.ShippingMethodID {
		return q, domain.ErrShippingMethod // the chosen method is no longer offered
	}
	if q.Pricing.TotalCents != in.ExpectedTotalCents {
		return q, domain.PriceChangedError{NewTotalCents: q.Pricing.TotalCents}
	}
	return q, nil
}

// Checkout turns the cart into an order, reserves stock and opens a payment, all
// in one transaction. Repeating a request with the same idempotency key returns
// the original order instead of creating another.
func (s *Service) Checkout(ctx context.Context, in CheckoutInput) (domain.Order, error) {
	in = s.normalize(in)
	if in.IdempotencyKey != "" {
		if o, err := s.Repo.ByIdempotencyKey(ctx, in.UserID, in.IdempotencyKey); err == nil {
			if o.Status == domain.StatusCancelled {
				// A stale form (Back button) must not resurrect an order that no longer exists.
				return domain.Order{}, domain.ErrOrderCancelled
			}
			return o, nil
		} else if !errors.Is(err, domain.ErrNotFound) {
			return domain.Order{}, err
		}
	}
	q, err := s.Preflight(ctx, in)
	if err != nil {
		return domain.Order{}, err
	}
	contact, err := s.Customers.Contact(ctx, in.UserID)
	if err != nil {
		return domain.Order{}, err
	}

	cod := in.PaymentMethod == domain.MethodCOD
	o := domain.Order{
		ID: id.New(), UserID: in.UserID, Status: domain.StatusAwaitingPayment, Currency: q.Cart.Currency,
		SubtotalCents: q.Pricing.SubtotalCents, DiscountCents: q.Pricing.DiscountCents, ShippingCents: q.Pricing.ShippingCents,
		TaxCents: q.Pricing.TaxCents, TotalCents: q.Pricing.TotalCents, TaxInclusive: q.Pricing.TaxInclusive,
		IdempotencyKey: in.IdempotencyKey, Shipping: in.Shipping, PaymentMethod: in.PaymentMethod, ContactEmail: contact.Email,
	}
	if q.Shipping != nil {
		o.ShippingMethod = q.Shipping.Name
	}
	if q.Coupon != nil {
		o.CouponCode = q.Coupon.Code
	}
	if cod {
		o.Status = domain.StatusPaid // accepted for delivery; the cash is collected by the courier
	}
	for _, l := range q.Cart.Lines {
		o.Items = append(o.Items, domain.Item{VariantID: l.VariantID, ProductID: l.ProductID, Name: l.Name, VariantLabel: l.Label,
			SKU: l.SKU, UnitPriceCents: l.UnitPriceCents, Quantity: l.Quantity})
	}

	err = s.Tx.WithTx(ctx, func(ctx context.Context) error {
		if err := s.Repo.Insert(ctx, o); err != nil {
			return err
		}
		if err := s.Inventory.Reserve(ctx, o.ID, o.Items); err != nil {
			return err
		}
		if q.Coupon != nil {
			// The coupon is re-checked against the shipping price before it was waived.
			shippingBefore := int64(0)
			if q.Shipping != nil {
				shippingBefore = q.Shipping.PriceCents
			}
			if err := s.Promotions.Redeem(ctx, *q.Coupon, o.ID, o.UserID, o.SubtotalCents, shippingBefore); err != nil {
				return err
			}
		}
		pid, err := s.Payments.CreateIntent(ctx, o.ID, o.UserID, o.TotalCents, o.Currency, o.PaymentMethod)
		if err != nil {
			return err
		}
		o.PaymentID = &pid
		if err := s.Repo.Save(ctx, o); err != nil {
			return err
		}
		if err := s.Repo.AddEvent(ctx, o.ID, EvPlaced, "Order placed", nil); err != nil {
			return err
		}
		if cod {
			// Cash on delivery is confirmed at once, so its stock hold becomes permanent now.
			if err := s.Inventory.Commit(ctx, o.ID); err != nil {
				return err
			}
			if err := s.Repo.AddEvent(ctx, o.ID, EvPaid, "Order confirmed — pay on delivery", nil); err != nil {
				return err
			}
			s.notifyConfirmed(ctx, o)
		}
		return s.Carts.Clear(ctx, in.CartOwner)
	})
	if errors.Is(err, domain.ErrDuplicate) { // lost a race with a concurrent identical request
		return s.Repo.ByIdempotencyKey(ctx, in.UserID, in.IdempotencyKey)
	}
	if err != nil {
		return domain.Order{}, err
	}
	return o, nil
}

// ---- reads ----

func (s *Service) Get(ctx context.Context, orderID uuid.UUID) (domain.Order, error) {
	return s.Repo.Get(ctx, orderID)
}

// GetForUser returns the order only if userID owns it.
func (s *Service) GetForUser(ctx context.Context, userID, orderID uuid.UUID) (domain.Order, error) {
	o, err := s.Repo.Get(ctx, orderID)
	if err == nil && o.UserID != userID {
		return domain.Order{}, domain.ErrNotFound
	}
	return o, err
}

func (s *Service) ListByUser(ctx context.Context, userID uuid.UUID) ([]domain.Order, error) {
	return s.Repo.ListByUser(ctx, userID, 100)
}

func (s *Service) List(ctx context.Context, f ListFilter, limit, offset int) ([]domain.Order, error) {
	f.Query = strings.TrimSpace(f.Query)
	return s.Repo.Search(ctx, f, limit, max(offset, 0))
}

func (s *Service) CountByStatus(ctx context.Context) (map[string]int, error) {
	return s.Repo.CountByStatus(ctx)
}

func (s *Service) PendingReturns(ctx context.Context) (int, error) {
	return s.Repo.CountReturnRequests(ctx)
}

// Cart returns the caller's priced cart (used by the page chrome and the checkout summary).
func (s *Service) Cart(ctx context.Context, owner string) (CartView, error) {
	return s.Carts.View(ctx, owner)
}

func (s *Service) SavedAddresses(ctx context.Context, userID uuid.UUID) ([]SavedAddress, error) {
	return s.Addresses.List(ctx, userID)
}

// Settings exposes the store rules the UI needs to show (payment methods, return window).
func (s *Service) StoreSettings() Settings { return s.Settings }

// ---- fulfilment pickup ----

// ClaimForFulfillment picks up paid orders for processing. Concurrent callers
// receive disjoint batches (FOR UPDATE SKIP LOCKED).
func (s *Service) ClaimForFulfillment(ctx context.Context, limit int) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := s.Tx.WithTx(ctx, func(ctx context.Context) error {
		claimed, err := s.Repo.ClaimPaid(ctx, limit)
		if err != nil {
			return err
		}
		for _, oid := range claimed {
			o, err := s.Repo.GetForUpdate(ctx, oid)
			if err != nil {
				return err
			}
			if err := o.Transition(domain.StatusFulfilling); err != nil {
				continue // changed under us (cancelled or refunded): skip it
			}
			if err := s.Repo.Save(ctx, o); err != nil {
				return err
			}
			if err := s.Repo.AddEvent(ctx, oid, EvFulfilling, "We are preparing your order", nil); err != nil {
				return err
			}
			ids = append(ids, oid)
		}
		return nil
	})
	return ids, err
}

// AnonymizeUser removes a deleted account's personal details from its orders.
func (s *Service) AnonymizeUser(ctx context.Context, userID uuid.UUID) error {
	n, err := s.Repo.AnonymizeUser(ctx, userID)
	if err == nil && n > 0 {
		s.Log.Info("orders anonymised after account deletion", "orders", n)
	}
	return err
}

// ExportUser returns the user's orders (with items) for a data download.
func (s *Service) ExportUser(ctx context.Context, userID uuid.UUID) ([]domain.Order, error) {
	os, err := s.Repo.ListByUser(ctx, userID, 1000)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Order, 0, len(os))
	for _, o := range os {
		full, err := s.Repo.Get(ctx, o.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, full)
	}
	return out, nil
}

// Report summarises sales between from (inclusive) and to (exclusive), in UTC days.
func (s *Service) Report(ctx context.Context, from, to time.Time) (domain.Report, error) {
	return s.Repo.Report(ctx, from, to)
}

// HasPurchased reports whether the user has had this product shipped to them.
func (s *Service) HasPurchased(ctx context.Context, userID, productID uuid.UUID) (bool, error) {
	return s.Repo.HasPurchased(ctx, userID, productID)
}
