// Package domain holds the order aggregate: its lifecycle, price breakdown and
// the rules for cancelling, refunding and returning.
package domain

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	StatusAwaitingPayment = "awaiting_payment"
	StatusPaid            = "paid" // confirmed: paid (card) or accepted for delivery (cash on delivery)
	StatusFulfilling      = "fulfilling"
	StatusShipped         = "shipped"
	StatusDelivered       = "delivered"
	StatusCancelled       = "cancelled"
	StatusRefunded        = "refunded"
)

// Return request states ("" means none was requested).
const (
	ReturnRequested = "requested"
	ReturnApproved  = "approved"
	ReturnRejected  = "rejected"
)

const (
	MethodCard = "card"
	MethodCOD  = "cod"
)

// transitions is the complete set of legal status changes.
var transitions = map[string][]string{
	StatusAwaitingPayment: {StatusPaid, StatusCancelled},
	StatusPaid:            {StatusFulfilling, StatusCancelled, StatusRefunded},
	StatusFulfilling:      {StatusShipped, StatusRefunded},
	StatusShipped:         {StatusDelivered, StatusRefunded},
	StatusDelivered:       {StatusRefunded},
}

type Item struct {
	VariantID      uuid.UUID
	ProductID      uuid.UUID
	Name           string
	VariantLabel   string
	SKU            string
	UnitPriceCents int64
	Quantity       int
}

func (i Item) SubtotalCents() int64 { return i.UnitPriceCents * int64(i.Quantity) }

// DisplayName is the product name with its variant, e.g. "Rain Jacket — M".
func (i Item) DisplayName() string {
	if i.VariantLabel == "" {
		return i.Name
	}
	return i.Name + " — " + i.VariantLabel
}

type ShippingAddress struct {
	FullName   string `json:"full_name"`
	Phone      string `json:"phone"`
	Line1      string `json:"line1"`
	Line2      string `json:"line2"`
	City       string `json:"city"`
	PostalCode string `json:"postal_code"`
	Country    string `json:"country"`
}

func (a ShippingAddress) Validate() error {
	for _, f := range []struct{ v, n string }{
		{a.FullName, "full name"}, {a.Phone, "phone"}, {a.Line1, "address line 1"},
		{a.City, "city"}, {a.PostalCode, "postal code"}, {a.Country, "country"},
	} {
		if strings.TrimSpace(f.v) == "" {
			return ValidationError(f.n + " is required")
		}
	}
	for _, f := range []struct {
		v   string
		max int
		n   string
	}{{a.FullName, 100, "full name"}, {a.Phone, 30, "phone"}, {a.Line1, 200, "address line 1"}, {a.Line2, 200, "address line 2"},
		{a.City, 100, "city"}, {a.PostalCode, 20, "postal code"}, {a.Country, 100, "country"}} {
		if len([]rune(f.v)) > f.max {
			return ValidationError(f.n + " is too long")
		}
	}
	return nil
}

// Tracking is the carrier information shown to the customer.
type Tracking struct {
	Carrier string
	Number  string
	URL     string
}

func (t Tracking) Validate() error {
	switch {
	case len([]rune(t.Carrier)) > 60:
		return ValidationError("carrier name is too long (60 characters max)")
	case len([]rune(t.Number)) > 100:
		return ValidationError("tracking number is too long (100 characters max)")
	case t.URL != "":
		u, err := url.Parse(t.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(t.URL) > 500 {
			return ValidationError("the tracking link must be an http(s) URL")
		}
	}
	return nil
}

func (t Tracking) Empty() bool { return t.Carrier == "" && t.Number == "" && t.URL == "" }

// Return is a customer's request to send goods back.
type Return struct {
	Status      string
	Reason      string
	Note        string // staff reply
	RequestedAt *time.Time
}

// Event is one entry in the order's timeline.
type Event struct {
	At     time.Time
	Kind   string
	Detail string
}

type Order struct {
	ID     uuid.UUID
	UserID uuid.UUID
	Status string

	SubtotalCents int64
	DiscountCents int64
	ShippingCents int64
	TaxCents      int64
	TotalCents    int64
	TaxInclusive  bool
	Currency      string

	IdempotencyKey string
	Shipping       ShippingAddress
	ShippingMethod string // name snapshot
	CouponCode     string
	PaymentMethod  string
	PaymentID      *uuid.UUID
	ContactEmail   string

	Tracking    Tracking
	ShippedAt   *time.Time
	DeliveredAt *time.Time
	CancelledAt *time.Time
	RefundedAt  *time.Time
	RefundCents int64
	AdminNote   string
	Return      Return

	Items     []Item
	Events    []Event
	CreatedAt time.Time
}

// CanTransition reports whether the order may move to the given status.
func (o Order) CanTransition(to string) bool {
	for _, s := range transitions[o.Status] {
		if s == to {
			return true
		}
	}
	return false
}

// Transition validates and applies a status change.
func (o *Order) Transition(to string) error {
	if !o.CanTransition(to) {
		return fmt.Errorf("%w: %s → %s", ErrInvalidTransition, o.Status, to)
	}
	o.Status = to
	return nil
}

// CanCustomerCancel: a customer may cancel until the warehouse has picked the order up.
func (o Order) CanCustomerCancel() bool {
	return o.Status == StatusAwaitingPayment || o.Status == StatusPaid
}

// CanRequestReturn: goods that have shipped can be returned within the window, once.
func (o Order) CanRequestReturn(now time.Time, windowDays int) bool {
	if o.Return.Status != "" || (o.Status != StatusShipped && o.Status != StatusDelivered) {
		return false
	}
	from := o.ShippedAt
	if o.DeliveredAt != nil {
		from = o.DeliveredAt
	}
	if from == nil {
		return true
	}
	return now.Before(from.AddDate(0, 0, windowDays))
}

// ReturnDeadline is when the return window closes (zero if unknown).
func (o Order) ReturnDeadline(windowDays int) time.Time {
	from := o.ShippedAt
	if o.DeliveredAt != nil {
		from = o.DeliveredAt
	}
	if from == nil {
		return time.Time{}
	}
	return from.AddDate(0, 0, windowDays)
}

// Refundable reports whether staff can refund the order.
func (o Order) Refundable() bool { return o.CanTransition(StatusRefunded) }

// ---- pricing ----

// Pricing is the breakdown the shopper pays.
type Pricing struct {
	SubtotalCents int64
	DiscountCents int64
	ShippingCents int64
	TaxCents      int64
	TotalCents    int64
	TaxInclusive  bool
}

// Price computes tax and total. rateBps is the tax rate in basis points (2000 = 20%).
// Tax applies to goods after discount plus shipping. With inclusive pricing the
// shelf prices already contain tax, so the total is unchanged and the tax is the
// part of it that belongs to the tax authority. Amounts round half up.
func Price(subtotal, discount, shipping int64, rateBps int, inclusive bool) Pricing {
	base := max(subtotal-discount, 0) + shipping
	p := Pricing{SubtotalCents: subtotal, DiscountCents: discount, ShippingCents: shipping, TaxInclusive: inclusive}
	rate := int64(rateBps)
	if inclusive {
		p.TaxCents = (base*rate + (10000+rate)/2) / (10000 + rate)
		p.TotalCents = base
	} else {
		p.TaxCents = (base*rate + 5000) / 10000
		p.TotalCents = base + p.TaxCents
	}
	return p
}

type ValidationError string

func (e ValidationError) Error() string { return string(e) }

var (
	ErrNotFound          = errors.New("order not found")
	ErrEmptyCart         = errors.New("your cart is empty")
	ErrInvalidTransition = errors.New("invalid order status change")
	ErrDuplicate         = errors.New("duplicate idempotency key")
	ErrStockLost         = errors.New("reserved stock was released before payment")
	ErrPaymentReceived   = errors.New("payment was already received for this order")
	ErrOrderCancelled    = errors.New("that order was cancelled")
	ErrPaymentMethod     = errors.New("that payment method is not available")
	ErrShippingMethod    = errors.New("choose a delivery method")
	ErrReturnWindow      = errors.New("this order cannot be returned (it is outside the return window or was already handled)")
)

// InsufficientStockError names the variant that could not be reserved.
type InsufficientStockError struct{ VariantID uuid.UUID }

func (e InsufficientStockError) Error() string {
	return "some items in your cart are no longer in stock"
}

// PriceChangedError means the cart total differs from the total the shopper saw.
type PriceChangedError struct{ NewTotalCents int64 }

func (e PriceChangedError) Error() string {
	return "prices in your cart changed since you opened this page"
}

// CouponRejectedError wraps the shopper-facing reason a coupon cannot be used.
type CouponRejectedError struct{ Msg string }

func (e CouponRejectedError) Error() string { return e.Msg }

// Report summarises sales for a period.
type Report struct {
	From, To      time.Time
	Orders        int
	RevenueCents  int64
	AvgOrderCents int64
	Refunds       int
	RefundedCents int64
	ByDay         []DayStat
	Top           []ProductStat
}

type DayStat struct {
	Day          time.Time
	Orders       int
	RevenueCents int64
}

type ProductStat struct {
	ProductID    uuid.UUID
	Name         string
	Units        int
	RevenueCents int64
}
