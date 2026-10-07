// Package payment is the public surface of the payment module.
package payment

import (
	"context"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/payment/app"
	"github.com/NaheedRayan/goat-architecture/internal/payment/domain"
)

type (
	Payment = domain.Payment
	Event   = domain.Event
)

// Outbox event kinds published when a payment settles; payload is Event.
const (
	EventSucceeded = app.EventSucceeded
	EventFailed    = app.EventFailed
)

const (
	MethodCard = domain.MethodCard
	MethodCOD  = domain.MethodCOD
)

var ErrNotRefundable = domain.ErrNotRefundable

type API interface {
	// CreateIntent records a pending payment. method is "card" or "cod".
	CreateIntent(ctx context.Context, orderID, userID uuid.UUID, amountCents int64, currency, method string) (Payment, error)
	// CollectCOD records cash collected by the courier (cash-on-delivery orders).
	CollectCOD(ctx context.Context, paymentID uuid.UUID) (Payment, error)
	// Refund returns a succeeded payment (idempotent).
	Refund(ctx context.Context, paymentID uuid.UUID) (Payment, error)
	// Fail marks a still-pending payment as failed (e.g. its order was cancelled)
	// and returns the payment as it stands: Status is "succeeded" if it was already paid.
	Fail(ctx context.Context, paymentID uuid.UUID) (Payment, error)
}
