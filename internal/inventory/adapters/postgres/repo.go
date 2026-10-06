// Package postgres implements the inventory repository with sqlc-generated queries.
package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/inventory/adapters/postgres/sqlcgen"
	"github.com/NaheedRayan/goat-architecture/internal/inventory/app"
	"github.com/NaheedRayan/goat-architecture/internal/inventory/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/db"
)

type Repo struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) q(ctx context.Context) *sqlcgen.Queries { return sqlcgen.New(db.Q(ctx, r.pool)) }

func (r *Repo) LockLots(ctx context.Context, variantID uuid.UUID) ([]app.LotRef, error) {
	rows, err := r.q(ctx).LockLotsForVariant(ctx, variantID)
	if err != nil {
		return nil, err
	}
	out := make([]app.LotRef, len(rows))
	for i, l := range rows {
		out[i] = app.LotRef{ID: l.ID, Quantity: int(l.Quantity)}
	}
	return out, nil
}

func (r *Repo) AdjustLot(ctx context.Context, lotID uuid.UUID, delta int) error {
	if delta < 0 {
		return r.q(ctx).DecrementLot(ctx, sqlcgen.DecrementLotParams{ID: lotID, Quantity: int32(-delta)})
	}
	return r.q(ctx).IncrementLot(ctx, sqlcgen.IncrementLotParams{ID: lotID, Quantity: int32(delta)})
}

func (r *Repo) AddLot(ctx context.Context, l domain.Lot) error {
	return r.q(ctx).InsertLot(ctx, sqlcgen.InsertLotParams{ID: l.ID, VariantID: l.VariantID, Label: l.Label, Quantity: int32(l.Quantity)})
}

func (r *Repo) InsertReservation(ctx context.Context, res domain.Reservation) error {
	return r.q(ctx).InsertReservation(ctx, sqlcgen.InsertReservationParams{
		ID: res.ID, OrderID: res.OrderID, VariantID: res.VariantID, LotID: res.LotID,
		Quantity: int32(res.Quantity), ExpiresAt: res.ExpiresAt,
	})
}

func (r *Repo) ReservedByOrder(ctx context.Context, orderID uuid.UUID) ([]domain.Reservation, error) {
	rows, err := r.q(ctx).ListReservedByOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Reservation, len(rows))
	for i, x := range rows {
		out[i] = domain.Reservation{ID: x.ID, OrderID: orderID, LotID: x.LotID, Quantity: int(x.Quantity)}
	}
	return out, nil
}

func (r *Repo) SetReservationStatus(ctx context.Context, reservationID uuid.UUID, status string) error {
	return r.q(ctx).SetReservationStatus(ctx, sqlcgen.SetReservationStatusParams{ID: reservationID, Status: status})
}

func (r *Repo) CommitOrder(ctx context.Context, orderID uuid.UUID) (int, error) {
	n, err := r.q(ctx).CommitOrderReservations(ctx, orderID)
	return int(n), err
}

// ClaimExpired locks expired reservations (SKIP LOCKED) and then every other
// still-reserved line of the same orders, so an order is always released as a
// whole. Releasing only some lines would leave an order half-held, and a later
// payment could commit stock that was already resold.
func (r *Repo) ClaimExpired(ctx context.Context, limit int) ([]domain.Reservation, error) {
	q := r.q(ctx)
	rows, err := q.ClaimExpiredReservations(ctx, int32(limit))
	if err != nil {
		return nil, err
	}
	var out []domain.Reservation
	seen := map[uuid.UUID]bool{}
	var orders []uuid.UUID
	seenOrder := map[uuid.UUID]bool{}
	for _, x := range rows {
		seen[x.ID] = true
		out = append(out, domain.Reservation{ID: x.ID, OrderID: x.OrderID, LotID: x.LotID, Quantity: int(x.Quantity)})
		if !seenOrder[x.OrderID] {
			seenOrder[x.OrderID] = true
			orders = append(orders, x.OrderID)
		}
	}
	for _, oid := range orders {
		rest, err := q.ListReservedByOrder(ctx, oid)
		if err != nil {
			return nil, err
		}
		for _, x := range rest {
			if !seen[x.ID] {
				seen[x.ID] = true
				out = append(out, domain.Reservation{ID: x.ID, OrderID: oid, LotID: x.LotID, Quantity: int(x.Quantity)})
			}
		}
	}
	return out, nil
}

func (r *Repo) Available(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]int, error) {
	rows, err := r.q(ctx).AvailableByVariants(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]int, len(rows))
	for _, x := range rows {
		out[x.VariantID] = int(x.Available)
	}
	return out, nil
}

func (r *Repo) Lots(ctx context.Context, variantID uuid.UUID) ([]domain.Lot, error) {
	rows, err := r.q(ctx).ListLotsByVariant(ctx, variantID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Lot, len(rows))
	for i, x := range rows {
		out[i] = domain.Lot{ID: x.ID, VariantID: x.VariantID, Label: x.Label, Quantity: int(x.Quantity), CreatedAt: x.CreatedAt}
	}
	return out, nil
}

func (r *Repo) PurgeReservations(ctx context.Context, before time.Time) (int64, error) {
	return r.q(ctx).PurgeReservations(ctx, before)
}

func (r *Repo) SetLotQuantity(ctx context.Context, variantID, lotID uuid.UUID, qty int) error {
	n, err := r.q(ctx).SetLotQuantity(ctx, sqlcgen.SetLotQuantityParams{ID: lotID, VariantID: variantID, Quantity: int32(qty)})
	if err == nil && n == 0 {
		return domain.ErrLotNotFound
	}
	return err
}
