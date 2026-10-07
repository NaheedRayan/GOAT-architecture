// Package order is the public surface of the order module.
package order

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/order/app"
	"github.com/NaheedRayan/goat-architecture/internal/order/domain"
)

type (
	Order       = domain.Order
	Item        = domain.Item
	Tracking    = domain.Tracking
	Event       = domain.Event
	ListFilter  = app.ListFilter
	Report      = domain.Report
	DayStat     = domain.DayStat
	ProductStat = domain.ProductStat
)

const (
	StatusAwaitingPayment = domain.StatusAwaitingPayment
	StatusPaid            = domain.StatusPaid
	StatusFulfilling      = domain.StatusFulfilling
	StatusShipped         = domain.StatusShipped
	StatusDelivered       = domain.StatusDelivered
	StatusCancelled       = domain.StatusCancelled
	StatusRefunded        = domain.StatusRefunded

	ReturnRequested = domain.ReturnRequested
	ReturnApproved  = domain.ReturnApproved
	ReturnRejected  = domain.ReturnRejected

	MethodCard = domain.MethodCard
	MethodCOD  = domain.MethodCOD
)

var (
	ErrNotFound          = domain.ErrNotFound
	ErrInvalidTransition = domain.ErrInvalidTransition
	ErrPaymentReceived   = domain.ErrPaymentReceived
)

type ValidationError = domain.ValidationError

// API is what the back office (and other modules) can do with orders.
type API interface {
	Get(ctx context.Context, id uuid.UUID) (Order, error)
	List(ctx context.Context, f ListFilter, limit, offset int) ([]Order, error)
	CountByStatus(ctx context.Context) (map[string]int, error)
	// Report summarises sales for [from, to) in UTC days.
	Report(ctx context.Context, from, to time.Time) (Report, error)
	PendingReturns(ctx context.Context) (int, error)
	// HasPurchased reports whether the user has had the product shipped to them.
	HasPurchased(ctx context.Context, userID, productID uuid.UUID) (bool, error)

	Ship(ctx context.Context, actor, id uuid.UUID, t Tracking) error
	Deliver(ctx context.Context, actor, id uuid.UUID) error
	CancelByStaff(ctx context.Context, actor, id uuid.UUID, reason string) error
	Refund(ctx context.Context, actor, id uuid.UUID, restock bool, reason string) error
	SetNote(ctx context.Context, actor, id uuid.UUID, note string) error
	ResolveReturn(ctx context.Context, actor, id uuid.UUID, approve bool, note string, restock bool) error

	// ReturnDeadline is when the return window closes for an order (zero if not applicable).
	ReturnDeadline(o Order) time.Time
}
