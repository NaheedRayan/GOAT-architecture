// Package domain holds the order aggregate and its state machine.
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	StatusAwaitingPayment = "awaiting_payment"
	StatusPaid            = "paid"
	StatusFulfilling      = "fulfilling"
	StatusShipped         = "shipped"
	StatusCancelled       = "cancelled"
)

// transitions is the complete set of legal status changes.
var transitions = map[string][]string{
	StatusAwaitingPayment: {StatusPaid, StatusCancelled},
	StatusPaid:            {StatusFulfilling},
	StatusFulfilling:      {StatusShipped},
}

type Item struct {
	ProductID      uuid.UUID
	Name           string
	UnitPriceCents int64
	Quantity       int
}

func (i Item) SubtotalCents() int64 { return i.UnitPriceCents * int64(i.Quantity) }

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

type Order struct {
	ID             uuid.UUID
	UserID         uuid.UUID
	Status         string
	TotalCents     int64
	Currency       string
	IdempotencyKey string
	Shipping       ShippingAddress
	PaymentID      *uuid.UUID
	Items          []Item
	CreatedAt      time.Time
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
)

// InsufficientStockError names the product that could not be reserved.
type InsufficientStockError struct{ ProductID uuid.UUID }

func (e InsufficientStockError) Error() string {
	return "some items in your cart are no longer in stock"
}

// PriceChangedError means the cart total differs from the total the shopper saw.
type PriceChangedError struct{ NewTotalCents int64 }

func (e PriceChangedError) Error() string {
	return "prices in your cart changed since you opened this page"
}
