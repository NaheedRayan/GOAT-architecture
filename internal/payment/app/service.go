// Package app contains payment use cases and ports.
package app

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/payment/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/id"
)

const (
	EventSucceeded = "payment.succeeded"
	EventFailed    = "payment.failed"
)

type Repository interface {
	Insert(ctx context.Context, p domain.Payment) error
	Get(ctx context.Context, id uuid.UUID) (domain.Payment, error)
	// Settle moves a pending payment to a final status; ok is false when it was not pending.
	Settle(ctx context.Context, id uuid.UUID, status, providerRef string) (p domain.Payment, ok bool, err error)
}

// Gateway abstracts the payment provider. The mock implementation ships by
// default; a real provider (Stripe, SSLCommerz, bKash…) is another adapter.
type Gateway interface {
	Name() string
	Checkout(ctx context.Context, p domain.Payment) (domain.Session, error)
	ParseWebhook(h http.Header, body []byte) (domain.WebhookEvent, error)
}

type Tx interface {
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error
}

type Publisher interface {
	Publish(ctx context.Context, kind string, payload any) error
}

type Service struct {
	repo Repository
	gw   Gateway
	tx   Tx
	pub  Publisher
}

func NewService(repo Repository, gw Gateway, tx Tx, pub Publisher) *Service {
	return &Service{repo: repo, gw: gw, tx: tx, pub: pub}
}

// CreateIntent records a pending payment. It performs no provider I/O, so it is
// safe inside the checkout transaction; the provider is contacted in Checkout.
func (s *Service) CreateIntent(ctx context.Context, orderID, userID uuid.UUID, amountCents int64, currency string) (domain.Payment, error) {
	p := domain.Payment{
		ID: id.New(), OrderID: orderID, UserID: userID, AmountCents: amountCents,
		Currency: currency, Provider: s.gw.Name(), Status: domain.StatusPending,
	}
	return p, s.repo.Insert(ctx, p)
}

func (s *Service) Get(ctx context.Context, paymentID uuid.UUID) (domain.Payment, error) {
	return s.repo.Get(ctx, paymentID)
}

// Checkout starts the provider flow for a pending payment owned by userID.
func (s *Service) Checkout(ctx context.Context, paymentID, userID uuid.UUID) (domain.Payment, domain.Session, error) {
	p, err := s.repo.Get(ctx, paymentID)
	if err != nil {
		return p, domain.Session{}, err
	}
	if p.UserID != userID {
		return p, domain.Session{}, domain.ErrNotFound
	}
	if p.Status != domain.StatusPending {
		return p, domain.Session{}, domain.ErrAlreadySettled
	}
	sess, err := s.gw.Checkout(ctx, p)
	return p, sess, err
}

// Settle records the outcome and publishes an event, atomically. It is
// idempotent: redelivered webhooks for an already-settled payment are ignored.
func (s *Service) Settle(ctx context.Context, paymentID uuid.UUID, succeeded bool, providerRef string) (domain.Payment, error) {
	var out domain.Payment
	err := s.tx.WithTx(ctx, func(ctx context.Context) error {
		status, kind := domain.StatusFailed, EventFailed
		if succeeded {
			status, kind = domain.StatusSucceeded, EventSucceeded
		}
		p, ok, err := s.repo.Settle(ctx, paymentID, status, providerRef)
		if err != nil {
			return err
		}
		if !ok {
			cur, err := s.repo.Get(ctx, paymentID)
			out = cur
			return err
		}
		out = p
		return s.pub.Publish(ctx, kind, domain.Event{PaymentID: p.ID, OrderID: p.OrderID})
	})
	return out, err
}

func (s *Service) HandleWebhook(ctx context.Context, h http.Header, body []byte) error {
	ev, err := s.gw.ParseWebhook(h, body)
	if err != nil {
		return err
	}
	_, err = s.Settle(ctx, ev.PaymentID, ev.Succeeded, ev.ProviderRef)
	return err
}
