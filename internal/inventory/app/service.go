// Package app contains inventory use cases.
//
// Stock lives in lots. Reserve locks candidate lots with FOR UPDATE SKIP LOCKED
// so concurrent checkouts never block each other and can never oversell: a lot
// being decremented by another transaction is skipped, and if the remaining
// lots cannot cover the request the checkout fails with InsufficientStock.
package app

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/inventory/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/id"
)

type LotRef struct {
	ID       uuid.UUID
	Quantity int
}

type Repository interface {
	LockLots(ctx context.Context, variantID uuid.UUID) ([]LotRef, error) // FOR UPDATE SKIP LOCKED
	AdjustLot(ctx context.Context, lotID uuid.UUID, delta int) error
	AddLot(ctx context.Context, l domain.Lot) error
	SetLotQuantity(ctx context.Context, variantID, lotID uuid.UUID, qty int) error // domain.ErrLotNotFound
	InsertReservation(ctx context.Context, r domain.Reservation) error
	ReservedByOrder(ctx context.Context, orderID uuid.UUID) ([]domain.Reservation, error) // FOR UPDATE
	SetReservationStatus(ctx context.Context, reservationID uuid.UUID, status string) error
	CommitOrder(ctx context.Context, orderID uuid.UUID) (int, error)
	CommittedByOrder(ctx context.Context, orderID uuid.UUID) ([]domain.Reservation, error)
	ClaimExpired(ctx context.Context, limit int) ([]domain.Reservation, error) // FOR UPDATE SKIP LOCKED
	Available(ctx context.Context, variantIDs []uuid.UUID) (map[uuid.UUID]int, error)
	Lots(ctx context.Context, variantID uuid.UUID) ([]domain.Lot, error)
	PurgeReservations(ctx context.Context, before time.Time) (int64, error)
	LowStock(ctx context.Context, threshold, limit int) ([]domain.StockLevel, error)
}

type Tx interface {
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error
}

type Publisher interface {
	Publish(ctx context.Context, kind string, payload any) error
}

// EventReservationExpired is published when an unpaid reservation times out.
const EventReservationExpired = "inventory.reservation_expired"

// A hot variant may sit in one lot that every checkout wants: they serialize on
// its lock. Waiting is cheap compared with failing a paying customer, so retry
// with jittered backoff (about 1-2s in total) before giving up.
const (
	maxLockRetries = 30
	baseRetryDelay = 10 * time.Millisecond
	maxRetryDelay  = 80 * time.Millisecond
)

func retryDelay(attempt int) time.Duration {
	d := min(baseRetryDelay*time.Duration(attempt+1), maxRetryDelay)
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

type Service struct {
	repo Repository
	tx   Tx
	pub  Publisher
	ttl  time.Duration
}

func NewService(repo Repository, tx Tx, pub Publisher, reservationTTL time.Duration) *Service {
	return &Service{repo: repo, tx: tx, pub: pub, ttl: reservationTTL}
}

// Reserve atomically holds stock for an order. It joins the caller's
// transaction when one is in the context.
func (s *Service) Reserve(ctx context.Context, orderID uuid.UUID, items []domain.Item) error {
	items, err := domain.Merge(items)
	if err != nil {
		return err
	}
	expires := time.Now().Add(s.ttl)
	return s.tx.WithTx(ctx, func(ctx context.Context) error {
		for _, it := range items {
			need := it.Quantity
			for attempt := 0; need > 0; attempt++ {
				lots, err := s.repo.LockLots(ctx, it.VariantID)
				if err != nil {
					return err
				}
				for _, lot := range lots {
					if need == 0 {
						break
					}
					take := min(need, lot.Quantity)
					if err := s.repo.AdjustLot(ctx, lot.ID, -take); err != nil {
						return err
					}
					if err := s.repo.InsertReservation(ctx, domain.Reservation{
						ID: id.New(), OrderID: orderID, VariantID: it.VariantID, LotID: lot.ID, Quantity: take, ExpiresAt: expires,
					}); err != nil {
						return err
					}
					need -= take
				}
				if need == 0 {
					break
				}
				// Lots skipped because another checkout holds their lock may still
				// cover us once it commits, so only give up on a real shortage.
				avail, err := s.repo.Available(ctx, []uuid.UUID{it.VariantID})
				if err != nil {
					return err
				}
				if avail[it.VariantID] < need || attempt >= maxLockRetries {
					return domain.InsufficientStockError{VariantID: it.VariantID}
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(retryDelay(attempt)):
				}
			}
		}
		return nil
	})
}

// Commit marks the order's reservations permanent (stock was already deducted).
// It returns ErrReservationLost if the hold expired and was released first.
func (s *Service) Commit(ctx context.Context, orderID uuid.UUID) error {
	n, err := s.repo.CommitOrder(ctx, orderID)
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrReservationLost
	}
	return nil
}

// Release returns the stock of an order's still-reserved holds to its lots.
func (s *Service) Release(ctx context.Context, orderID uuid.UUID) error {
	return s.tx.WithTx(ctx, func(ctx context.Context) error {
		rs, err := s.repo.ReservedByOrder(ctx, orderID)
		if err != nil {
			return err
		}
		return s.release(ctx, rs)
	})
}

func (s *Service) release(ctx context.Context, rs []domain.Reservation) error {
	for _, r := range rs {
		if err := s.repo.AdjustLot(ctx, r.LotID, r.Quantity); err != nil {
			return err
		}
		if err := s.repo.SetReservationStatus(ctx, r.ID, domain.StatusReleased); err != nil {
			return err
		}
	}
	return nil
}

// ReleaseExpired frees timed-out reservations and publishes one event per
// affected order. Multiple workers can run it concurrently (SKIP LOCKED).
func (s *Service) ReleaseExpired(ctx context.Context, batch int) (int, error) {
	n := 0
	err := s.tx.WithTx(ctx, func(ctx context.Context) error {
		rs, err := s.repo.ClaimExpired(ctx, batch)
		if err != nil {
			return err
		}
		if err := s.release(ctx, rs); err != nil {
			return err
		}
		seen := map[uuid.UUID]bool{}
		for _, r := range rs {
			if seen[r.OrderID] {
				continue
			}
			seen[r.OrderID] = true
			if err := s.pub.Publish(ctx, EventReservationExpired, map[string]uuid.UUID{"order_id": r.OrderID}); err != nil {
				return err
			}
		}
		n = len(rs)
		return nil
	})
	return n, err
}

func (s *Service) AddLot(ctx context.Context, variantID uuid.UUID, label string, qty int) error {
	if qty <= 0 {
		return domain.ErrInvalidQuantity
	}
	return s.repo.AddLot(ctx, domain.Lot{ID: id.New(), VariantID: variantID, Label: label, Quantity: qty})
}

func (s *Service) Available(ctx context.Context, variantIDs []uuid.UUID) (map[uuid.UUID]int, error) {
	if len(variantIDs) == 0 {
		return map[uuid.UUID]int{}, nil
	}
	return s.repo.Available(ctx, variantIDs)
}

func (s *Service) Lots(ctx context.Context, variantID uuid.UUID) ([]domain.Lot, error) {
	return s.repo.Lots(ctx, variantID)
}

// PurgeHistory deletes committed/released reservations older than 30 days.
func (s *Service) PurgeHistory(ctx context.Context) (int64, error) {
	return s.repo.PurgeReservations(ctx, time.Now().Add(-30*24*time.Hour))
}

const MaxLotQuantity = 1_000_000

// SetLotQuantity corrects a lot's available count (a miscount, damaged goods,
// a return). Units already reserved by open orders are not part of it.
func (s *Service) SetLotQuantity(ctx context.Context, variantID, lotID uuid.UUID, qty int) error {
	if qty < 0 || qty > MaxLotQuantity {
		return domain.ErrInvalidQuantity
	}
	return s.repo.SetLotQuantity(ctx, variantID, lotID, qty)
}

// Return puts a sold order's stock back on the shelf (cancelled after payment,
// refunded or returned). Idempotent: reservations already returned are skipped.
func (s *Service) Return(ctx context.Context, orderID uuid.UUID) error {
	return s.tx.WithTx(ctx, func(ctx context.Context) error {
		rs, err := s.repo.CommittedByOrder(ctx, orderID)
		if err != nil {
			return err
		}
		for _, r := range rs {
			if err := s.repo.AdjustLot(ctx, r.LotID, r.Quantity); err != nil {
				return err
			}
			if err := s.repo.SetReservationStatus(ctx, r.ID, domain.StatusReturned); err != nil {
				return err
			}
		}
		return nil
	})
}

// LowStock lists variants at or below the threshold (including sold out), lowest first.
func (s *Service) LowStock(ctx context.Context, threshold, limit int) ([]domain.StockLevel, error) {
	return s.repo.LowStock(ctx, threshold, limit)
}
