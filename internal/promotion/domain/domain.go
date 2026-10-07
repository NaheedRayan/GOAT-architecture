// Package domain holds coupon rules.
package domain

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	KindPercent      = "percent"
	KindFixed        = "fixed"
	KindFreeShipping = "free_shipping"
)

type Coupon struct {
	ID             uuid.UUID
	Code           string
	Kind           string
	Value          int64 // percent (1-100) or cents
	MinSubtotal    int64
	StartsAt       *time.Time
	EndsAt         *time.Time
	MaxUses        *int
	MaxUsesPerUser *int
	Active         bool
	Uses           int // filled by List
}

// Applied is a coupon evaluated against a basket.
type Applied struct {
	CouponID      uuid.UUID
	Code          string
	DiscountCents int64
	FreeShipping  bool
}

// CouponError is a reason a code cannot be used, phrased for the shopper.
type CouponError struct{ Msg string }

func (e CouponError) Error() string { return e.Msg }

var (
	ErrNotFound = errors.New("coupon not found")
	ErrInUse    = errors.New("this coupon has been used, so it cannot be deleted; deactivate it instead")
)

type ValidationError string

func (e ValidationError) Error() string { return string(e) }

var codeRe = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{2,31}$`)

// NormalizeCode upper-cases and trims a code as typed by a shopper or admin.
func NormalizeCode(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

func (c Coupon) Validate() error {
	switch {
	case !codeRe.MatchString(c.Code):
		return ValidationError("the code must be 3-32 characters: letters, digits, - or _")
	case c.Kind != KindPercent && c.Kind != KindFixed && c.Kind != KindFreeShipping:
		return ValidationError("unknown coupon type")
	case c.Kind == KindPercent && (c.Value < 1 || c.Value > 100):
		return ValidationError("a percentage discount must be between 1 and 100")
	case c.Kind == KindFixed && c.Value <= 0:
		return ValidationError("a fixed discount must be more than zero")
	case c.MinSubtotal < 0:
		return ValidationError("the minimum order cannot be negative")
	case c.StartsAt != nil && c.EndsAt != nil && !c.EndsAt.After(*c.StartsAt):
		return ValidationError("the end date must be after the start date")
	case c.MaxUses != nil && *c.MaxUses < 1, c.MaxUsesPerUser != nil && *c.MaxUsesPerUser < 1:
		return ValidationError("usage limits must be at least 1 (or empty for unlimited)")
	}
	return nil
}

// Check applies the rules that depend on time and basket, but not on how often the
// code was used. now is injected for testability.
func (c Coupon) Check(now time.Time, subtotalCents, shippingCents int64, formatMoney func(int64) string) error {
	switch {
	case !c.Active:
		return CouponError{"That code is no longer active."}
	case c.StartsAt != nil && now.Before(*c.StartsAt):
		return CouponError{"That code is not valid yet."}
	case c.EndsAt != nil && now.After(*c.EndsAt):
		return CouponError{"That code has expired."}
	case subtotalCents < c.MinSubtotal:
		return CouponError{"Spend " + formatMoney(c.MinSubtotal) + " or more to use that code."}
	case c.Kind == KindFreeShipping && shippingCents == 0:
		return CouponError{"Delivery is already free on this order."}
	}
	return nil
}

// Discount computes the money off the subtotal.
func (c Coupon) Discount(subtotalCents int64) int64 {
	var d int64
	switch c.Kind {
	case KindPercent:
		d = (subtotalCents*c.Value + 50) / 100 // half up
	case KindFixed:
		d = c.Value
	}
	return min(d, subtotalCents)
}
