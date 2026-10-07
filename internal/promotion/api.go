// Package promotion is the public surface of the coupon module.
package promotion

import (
	"context"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/promotion/domain"
)

type (
	Coupon  = domain.Coupon
	Applied = domain.Applied
	// CouponError carries a shopper-facing reason a code cannot be used.
	CouponError = domain.CouponError
)

const (
	KindPercent      = domain.KindPercent
	KindFixed        = domain.KindFixed
	KindFreeShipping = domain.KindFreeShipping
)

var (
	ErrNotFound = domain.ErrNotFound
	ErrInUse    = domain.ErrInUse
)

type ValidationError = domain.ValidationError

type API interface {
	// Evaluate checks a code against a basket without consuming a use.
	Evaluate(ctx context.Context, code string, userID uuid.UUID, subtotalCents, shippingCents int64) (Applied, error)
	// Redeem consumes a use for an order (call inside the checkout transaction).
	Redeem(ctx context.Context, a Applied, orderID, userID uuid.UUID, subtotalCents, shippingCents int64) error
	// Release returns the use taken by an order that was cancelled.
	Release(ctx context.Context, orderID uuid.UUID) error

	List(ctx context.Context) ([]Coupon, error)
	Get(ctx context.Context, id uuid.UUID) (Coupon, error)
	Create(ctx context.Context, c Coupon) (Coupon, error)
	Update(ctx context.Context, c Coupon) error
	Delete(ctx context.Context, id uuid.UUID) error
}
