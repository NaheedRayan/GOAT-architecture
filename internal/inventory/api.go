// Package inventory is the public surface of the inventory module.
package inventory

import (
	"context"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/inventory/app"
	"github.com/NaheedRayan/goat-architecture/internal/inventory/domain"
)

var (
	ErrReservationLost = domain.ErrReservationLost
	ErrLotNotFound     = domain.ErrLotNotFound
	ErrInvalidQuantity = domain.ErrInvalidQuantity
)

type (
	StockLevel             = domain.StockLevel
	Item                   = domain.Item
	Lot                    = domain.Lot
	InsufficientStockError = domain.InsufficientStockError
)

// EventReservationExpired is published (outbox payload: {"order_id": uuid})
// when an unpaid reservation times out.
const EventReservationExpired = app.EventReservationExpired

type API interface {
	Reserve(ctx context.Context, orderID uuid.UUID, items []Item) error
	Commit(ctx context.Context, orderID uuid.UUID) error
	Release(ctx context.Context, orderID uuid.UUID) error
	// Return restocks a sold order (cancelled after payment, refunded, returned).
	Return(ctx context.Context, orderID uuid.UUID) error
	ReleaseExpired(ctx context.Context, batch int) (int, error)
	AddLot(ctx context.Context, variantID uuid.UUID, label string, qty int) error
	SetLotQuantity(ctx context.Context, variantID, lotID uuid.UUID, qty int) error
	Available(ctx context.Context, variantIDs []uuid.UUID) (map[uuid.UUID]int, error)
	// LowStock lists variants at or below the threshold, lowest first.
	LowStock(ctx context.Context, threshold, limit int) ([]StockLevel, error)
	Lots(ctx context.Context, variantID uuid.UUID) ([]Lot, error)
}
