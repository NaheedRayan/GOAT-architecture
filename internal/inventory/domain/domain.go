// Package domain holds inventory entities and rules.
package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type Item struct {
	VariantID uuid.UUID
	Quantity  int
}

type Lot struct {
	ID        uuid.UUID
	VariantID uuid.UUID
	Label     string
	Quantity  int
	CreatedAt time.Time
}

// StockLevel is a variant's total available units.
type StockLevel struct {
	VariantID uuid.UUID
	Available int
}

type Reservation struct {
	ID        uuid.UUID
	OrderID   uuid.UUID
	VariantID uuid.UUID
	LotID     uuid.UUID
	Quantity  int
	ExpiresAt time.Time
}

const (
	StatusReserved  = "reserved"
	StatusCommitted = "committed"
	StatusReleased  = "released"
	StatusReturned  = "returned"
)

// InsufficientStockError reports which variant could not be fully reserved.
type InsufficientStockError struct{ VariantID uuid.UUID }

func (e InsufficientStockError) Error() string {
	return fmt.Sprintf("insufficient stock for variant %s", e.VariantID)
}

var (
	ErrInvalidQuantity = errors.New("quantity must be between 0 and 1,000,000")
	ErrLotNotFound     = errors.New("stock lot not found")
	// ErrReservationLost means an order's stock hold no longer exists (it expired and was released).
	ErrReservationLost = errors.New("stock reservation is no longer held")
)

// Merge combines duplicate variant lines and rejects non-positive quantities.
func Merge(items []Item) ([]Item, error) {
	idx := map[uuid.UUID]int{}
	var out []Item
	for _, it := range items {
		if it.Quantity <= 0 {
			return nil, ErrInvalidQuantity
		}
		if i, ok := idx[it.VariantID]; ok {
			out[i].Quantity += it.Quantity
			continue
		}
		idx[it.VariantID] = len(out)
		out = append(out, it)
	}
	return out, nil
}
