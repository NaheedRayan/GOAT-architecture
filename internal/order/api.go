// Package order is the public surface of the order module.
package order

import (
	"context"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/order/domain"
)

type (
	Order = domain.Order
	Item  = domain.Item
)

const (
	StatusAwaitingPayment = domain.StatusAwaitingPayment
	StatusPaid            = domain.StatusPaid
	StatusFulfilling      = domain.StatusFulfilling
	StatusShipped         = domain.StatusShipped
	StatusCancelled       = domain.StatusCancelled
)

var (
	ErrNotFound          = domain.ErrNotFound
	ErrInvalidTransition = domain.ErrInvalidTransition
)

type API interface {
	Get(ctx context.Context, id uuid.UUID) (Order, error)
	List(ctx context.Context, status string, limit, offset int) ([]Order, error)
	CountByStatus(ctx context.Context) (map[string]int, error)
	Ship(ctx context.Context, id uuid.UUID) error
}
