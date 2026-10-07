// Package domain holds payment entities and rules.
package domain

import (
	"errors"

	"github.com/google/uuid"
)

const (
	StatusPending   = "pending"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusRefunded  = "refunded"

	MethodCard = "card"
	MethodCOD  = "cod"
)

type Payment struct {
	ID          uuid.UUID
	OrderID     uuid.UUID
	UserID      uuid.UUID
	AmountCents int64
	Currency    string
	Provider    string
	Method      string // MethodCard or MethodCOD
	ProviderRef string
	Status      string
}

// Event is the payload published when a payment settles. Consumers (the order
// module) subscribe by kind through the outbox, so payment never imports them.
type Event struct {
	PaymentID uuid.UUID `json:"payment_id"`
	OrderID   uuid.UUID `json:"order_id"`
}

// Session tells the browser where to complete the payment.
type Session struct{ URL string }

// WebhookEvent is a verified provider notification.
type WebhookEvent struct {
	PaymentID   uuid.UUID
	Succeeded   bool
	ProviderRef string
}

var (
	ErrNotFound         = errors.New("payment not found")
	ErrNotRefundable    = errors.New("only a payment that succeeded can be refunded")
	ErrNotCOD           = errors.New("not a cash-on-delivery payment")
	ErrAlreadySettled   = errors.New("payment is no longer pending")
	ErrInvalidSignature = errors.New("invalid webhook signature")
)
