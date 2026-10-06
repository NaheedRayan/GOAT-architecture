// Package app contains order use cases, the checkout saga and event handlers.
package app

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/order/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/id"
)

type Repository interface {
	Insert(ctx context.Context, o domain.Order) error // domain.ErrDuplicate on (user, key) clash
	Get(ctx context.Context, id uuid.UUID) (domain.Order, error)
	GetForUpdate(ctx context.Context, id uuid.UUID) (domain.Order, error)
	ByIdempotencyKey(ctx context.Context, userID uuid.UUID, key string) (domain.Order, error)
	ListByUser(ctx context.Context, userID uuid.UUID, limit int) ([]domain.Order, error)
	List(ctx context.Context, status string, limit, offset int) ([]domain.Order, error)
	CountByStatus(ctx context.Context) (map[string]int, error)
	SetStatus(ctx context.Context, id uuid.UUID, status string) error
	SetPayment(ctx context.Context, id, paymentID uuid.UUID) error
	ClaimPaid(ctx context.Context, limit int) ([]uuid.UUID, error) // FOR UPDATE SKIP LOCKED
}

// Ports to other modules, expressed in the order module's own terms.
type (
	CartLine struct {
		ProductID      uuid.UUID
		Name           string
		UnitPriceCents int64
		Quantity       int
	}
	CartView struct {
		Lines      []CartLine
		TotalCents int64
		Currency   string
	}
	SavedAddress struct {
		FullName, Phone, Line1, Line2, City, PostalCode, Country string
		IsDefault                                                bool
	}
)

type Carts interface {
	View(ctx context.Context, owner string) (CartView, error)
	Clear(ctx context.Context, owner string) error
}

type Inventory interface {
	// Reserve returns domain.InsufficientStockError when stock cannot cover the items.
	Reserve(ctx context.Context, orderID uuid.UUID, items []domain.Item) error
	Commit(ctx context.Context, orderID uuid.UUID) error
	Release(ctx context.Context, orderID uuid.UUID) error
}

type Payments interface {
	CreateIntent(ctx context.Context, orderID, userID uuid.UUID, amountCents int64, currency string) (uuid.UUID, error)
	// Fail settles a still-pending payment as failed and returns its final status
	// ("succeeded" when the shopper already paid).
	Fail(ctx context.Context, paymentID uuid.UUID) (status string, err error)
}

type Addresses interface {
	List(ctx context.Context, userID uuid.UUID) ([]SavedAddress, error)
}

type Tx interface {
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error
}

type Service struct {
	repo      Repository
	carts     Carts
	inventory Inventory
	payments  Payments
	addresses Addresses
	tx        Tx
	log       *slog.Logger
}

func NewService(repo Repository, carts Carts, inv Inventory, pay Payments, addrs Addresses, tx Tx, log *slog.Logger) *Service {
	return &Service{repo: repo, carts: carts, inventory: inv, payments: pay, addresses: addrs, tx: tx, log: log}
}

type CheckoutInput struct {
	UserID         uuid.UUID
	CartOwner      string
	IdempotencyKey string
	// ExpectedTotalCents is the total the shopper was shown; checkout refuses to
	// charge anything else.
	ExpectedTotalCents int64
	Shipping           domain.ShippingAddress
}

// Checkout turns the cart into an order, reserves stock and opens a payment,
// all in one transaction. Repeating a request with the same idempotency key
// returns the original order instead of creating another.
func (s *Service) Checkout(ctx context.Context, in CheckoutInput) (domain.Order, error) {
	in.IdempotencyKey = strings.TrimSpace(in.IdempotencyKey)
	if in.IdempotencyKey == "" || len(in.IdempotencyKey) > 100 {
		return domain.Order{}, domain.ValidationError("invalid checkout request, please reload the page")
	}
	if err := in.Shipping.Validate(); err != nil {
		return domain.Order{}, err
	}
	if o, err := s.repo.ByIdempotencyKey(ctx, in.UserID, in.IdempotencyKey); err == nil {
		if o.Status == domain.StatusCancelled {
			// A stale form (Back button) must not resurrect an order that no longer exists.
			return domain.Order{}, domain.ErrOrderCancelled
		}
		return o, nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return domain.Order{}, err
	}

	cart, err := s.carts.View(ctx, in.CartOwner)
	if err != nil {
		return domain.Order{}, err
	}
	if len(cart.Lines) == 0 {
		return domain.Order{}, domain.ErrEmptyCart
	}
	if cart.TotalCents != in.ExpectedTotalCents {
		return domain.Order{}, domain.PriceChangedError{NewTotalCents: cart.TotalCents}
	}
	o := domain.Order{
		ID: id.New(), UserID: in.UserID, Status: domain.StatusAwaitingPayment, TotalCents: cart.TotalCents,
		Currency: cart.Currency, IdempotencyKey: in.IdempotencyKey, Shipping: in.Shipping,
	}
	for _, l := range cart.Lines {
		o.Items = append(o.Items, domain.Item{ProductID: l.ProductID, Name: l.Name, UnitPriceCents: l.UnitPriceCents, Quantity: l.Quantity})
	}

	err = s.tx.WithTx(ctx, func(ctx context.Context) error {
		if err := s.repo.Insert(ctx, o); err != nil {
			return err
		}
		if err := s.inventory.Reserve(ctx, o.ID, o.Items); err != nil {
			return err
		}
		pid, err := s.payments.CreateIntent(ctx, o.ID, o.UserID, o.TotalCents, o.Currency)
		if err != nil {
			return err
		}
		o.PaymentID = &pid
		if err := s.repo.SetPayment(ctx, o.ID, pid); err != nil {
			return err
		}
		return s.carts.Clear(ctx, in.CartOwner)
	})
	if errors.Is(err, domain.ErrDuplicate) { // lost a race with a concurrent identical request
		return s.repo.ByIdempotencyKey(ctx, in.UserID, in.IdempotencyKey)
	}
	if err != nil {
		return domain.Order{}, err
	}
	return o, nil
}

func (s *Service) Get(ctx context.Context, orderID uuid.UUID) (domain.Order, error) {
	return s.repo.Get(ctx, orderID)
}

// GetForUser returns the order only if userID owns it.
func (s *Service) GetForUser(ctx context.Context, userID, orderID uuid.UUID) (domain.Order, error) {
	o, err := s.repo.Get(ctx, orderID)
	if err == nil && o.UserID != userID {
		return domain.Order{}, domain.ErrNotFound
	}
	return o, err
}

func (s *Service) ListByUser(ctx context.Context, userID uuid.UUID) ([]domain.Order, error) {
	return s.repo.ListByUser(ctx, userID, 100)
}

func (s *Service) List(ctx context.Context, status string, limit, offset int) ([]domain.Order, error) {
	return s.repo.List(ctx, status, limit, max(offset, 0))
}

func (s *Service) CountByStatus(ctx context.Context) (map[string]int, error) {
	return s.repo.CountByStatus(ctx)
}

// Cart returns the caller's priced cart for the checkout summary.
func (s *Service) Cart(ctx context.Context, owner string) (CartView, error) {
	return s.carts.View(ctx, owner)
}

func (s *Service) SavedAddresses(ctx context.Context, userID uuid.UUID) ([]SavedAddress, error) {
	return s.addresses.List(ctx, userID)
}

// Cancel lets a customer abandon an unpaid order, releasing its stock.
func (s *Service) Cancel(ctx context.Context, userID, orderID uuid.UUID) error {
	if _, err := s.GetForUser(ctx, userID, orderID); err != nil {
		return err
	}
	return s.cancelUnpaid(ctx, orderID, true)
}

// Ship completes fulfillment (admin action).
func (s *Service) Ship(ctx context.Context, orderID uuid.UUID) error {
	return s.transition(ctx, orderID, domain.StatusShipped)
}

func (s *Service) transition(ctx context.Context, orderID uuid.UUID, to string) error {
	return s.tx.WithTx(ctx, func(ctx context.Context) error {
		o, err := s.repo.GetForUpdate(ctx, orderID)
		if err != nil {
			return err
		}
		if err := o.Transition(to); err != nil {
			return err
		}
		return s.repo.SetStatus(ctx, orderID, to)
	})
}

// cancelUnpaid cancels an awaiting_payment order and frees its stock. It
// returns ErrInvalidTransition for orders already past that state, and
// ErrPaymentReceived when the shopper has paid but the payment event has not
// been processed yet (the order must then be completed, not cancelled).
func (s *Service) cancelUnpaid(ctx context.Context, orderID uuid.UUID, failPayment bool) error {
	return s.tx.WithTx(ctx, func(ctx context.Context) error {
		o, err := s.repo.GetForUpdate(ctx, orderID)
		if err != nil {
			return err
		}
		if err := o.Transition(domain.StatusCancelled); err != nil {
			if o.Status == domain.StatusCancelled {
				return nil
			}
			return err
		}
		if failPayment && o.PaymentID != nil {
			status, err := s.payments.Fail(ctx, *o.PaymentID)
			if err != nil {
				return err
			}
			if status == "succeeded" {
				return domain.ErrPaymentReceived
			}
		}
		if err := s.repo.SetStatus(ctx, orderID, domain.StatusCancelled); err != nil {
			return err
		}
		return s.inventory.Release(ctx, orderID)
	})
}

// OnPaymentSucceeded moves the order to paid and makes its stock permanent.
func (s *Service) OnPaymentSucceeded(ctx context.Context, orderID uuid.UUID) error {
	return s.ignoreMissing("payment.succeeded", orderID, s.onPaymentSucceeded(ctx, orderID))
}

func (s *Service) onPaymentSucceeded(ctx context.Context, orderID uuid.UUID) error {
	return s.tx.WithTx(ctx, func(ctx context.Context) error {
		o, err := s.repo.GetForUpdate(ctx, orderID)
		if err != nil {
			return err
		}
		switch o.Status {
		case domain.StatusAwaitingPayment:
			if err := s.inventory.Commit(ctx, orderID); errors.Is(err, domain.ErrStockLost) {
				// The hold expired just before the money arrived. Do not sell stock we
				// no longer hold: cancel and flag for a manual refund.
				s.log.Error("payment succeeded but stock hold was lost; refund required", "order_id", orderID)
				return s.repo.SetStatus(ctx, orderID, domain.StatusCancelled)
			} else if err != nil {
				return err
			}
			return s.repo.SetStatus(ctx, orderID, domain.StatusPaid)
		case domain.StatusCancelled:
			// Money arrived for an order we already cancelled: needs a manual refund.
			s.log.Error("payment succeeded for cancelled order; refund required", "order_id", orderID)
		}
		return nil // already paid or beyond: redelivered event
	})
}

func (s *Service) OnPaymentFailed(ctx context.Context, orderID uuid.UUID) error {
	return s.ignoreMissing("payment.failed", orderID, ignoreSettled(s.cancelUnpaid(ctx, orderID, false)))
}

func (s *Service) OnReservationExpired(ctx context.Context, orderID uuid.UUID) error {
	return s.ignoreMissing("inventory.reservation_expired", orderID, ignoreSettled(s.cancelUnpaid(ctx, orderID, true)))
}

// ignoreSettled makes cancellation events idempotent: an order that is already
// paid, shipped, or whose payment just arrived has nothing left to cancel.
func ignoreSettled(err error) error {
	if errors.Is(err, domain.ErrInvalidTransition) || errors.Is(err, domain.ErrPaymentReceived) {
		return nil
	}
	return err
}

// ignoreMissing turns "order not found" into success: retrying an event for an
// order that does not exist can never help, so it must not clog the queue.
func (s *Service) ignoreMissing(event string, orderID uuid.UUID, err error) error {
	if errors.Is(err, domain.ErrNotFound) {
		s.log.Warn("event for unknown order dropped", "event", event, "order_id", orderID)
		return nil
	}
	return err
}

// ClaimForFulfillment picks up paid orders for processing. Concurrent callers
// receive disjoint batches (FOR UPDATE SKIP LOCKED).
func (s *Service) ClaimForFulfillment(ctx context.Context, limit int) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := s.tx.WithTx(ctx, func(ctx context.Context) error {
		claimed, err := s.repo.ClaimPaid(ctx, limit)
		if err != nil {
			return err
		}
		for _, oid := range claimed {
			if err := s.repo.SetStatus(ctx, oid, domain.StatusFulfilling); err != nil {
				return err
			}
		}
		ids = claimed
		return nil
	})
	return ids, err
}
