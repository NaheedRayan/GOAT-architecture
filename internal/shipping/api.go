// Package shipping is the public surface of the delivery-methods module.
package shipping

import (
	"context"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/shipping/domain"
)

type (
	Method = domain.Method
	Option = domain.Option
)

var (
	ErrNotFound     = domain.ErrNotFound
	ErrNotAvailable = domain.ErrNotAvailable
)

type ValidationError = domain.ValidationError

type API interface {
	// Options lists active delivery methods priced for a basket subtotal.
	Options(ctx context.Context, subtotalCents int64) ([]Option, error)
	// Resolve prices one active method; ErrNotAvailable if it is unknown or inactive.
	Resolve(ctx context.Context, methodID uuid.UUID, subtotalCents int64) (Option, error)
	All(ctx context.Context) ([]Method, error)
	Get(ctx context.Context, id uuid.UUID) (Method, error)
	Create(ctx context.Context, m Method) (Method, error)
	Update(ctx context.Context, m Method) error
	Delete(ctx context.Context, id uuid.UUID) error
}
