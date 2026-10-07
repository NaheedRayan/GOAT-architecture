// Package postgres implements the cart repository with sqlc-generated queries.
package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/cart/adapters/postgres/sqlcgen"
	"github.com/NaheedRayan/goat-architecture/internal/cart/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/db"
	"github.com/NaheedRayan/goat-architecture/internal/platform/id"
)

type Repo struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) q(ctx context.Context) *sqlcgen.Queries { return sqlcgen.New(db.Q(ctx, r.pool)) }

func (r *Repo) CartID(ctx context.Context, owner string) (uuid.UUID, bool, error) {
	cid, err := r.q(ctx).GetCartID(ctx, owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	return cid, err == nil, err
}

func (r *Repo) GetOrCreate(ctx context.Context, owner string) (uuid.UUID, error) {
	return r.q(ctx).UpsertCart(ctx, sqlcgen.UpsertCartParams{ID: id.New(), Owner: owner})
}

func (r *Repo) Items(ctx context.Context, cartID uuid.UUID) ([]domain.Item, error) {
	rows, err := r.q(ctx).ListCartItems(ctx, cartID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Item, len(rows))
	for i, x := range rows {
		out[i] = domain.Item{VariantID: x.VariantID, Quantity: int(x.Quantity)}
	}
	return out, nil
}

func (r *Repo) SetItem(ctx context.Context, cartID, variantID uuid.UUID, qty int) error {
	return r.q(ctx).SetCartItem(ctx, sqlcgen.SetCartItemParams{CartID: cartID, VariantID: variantID, Quantity: int32(qty)})
}

func (r *Repo) DeleteItem(ctx context.Context, cartID, variantID uuid.UUID) error {
	return r.q(ctx).DeleteCartItem(ctx, sqlcgen.DeleteCartItemParams{CartID: cartID, VariantID: variantID})
}

func (r *Repo) Clear(ctx context.Context, cartID uuid.UUID) error {
	return r.q(ctx).ClearCart(ctx, cartID)
}

func (r *Repo) DeleteCart(ctx context.Context, owner string) error {
	return r.q(ctx).DeleteCartByOwner(ctx, owner)
}

func (r *Repo) Count(ctx context.Context, owner string) (int, error) {
	n, err := r.q(ctx).CountCartItems(ctx, owner)
	return int(n), err
}

func (r *Repo) AddItem(ctx context.Context, cartID, variantID uuid.UUID, qty int) error {
	return r.q(ctx).AddCartItem(ctx, sqlcgen.AddCartItemParams{CartID: cartID, VariantID: variantID, Quantity: int32(qty)})
}

// Merge moves every line of the guest cart into the user's cart in one
// transaction, locking the source cart so parallel requests cannot both apply it.
func (r *Repo) Merge(ctx context.Context, from, to string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := sqlcgen.New(tx)

	fromID, err := q.LockCartByOwner(ctx, from)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // nothing to merge, or a parallel request already did
	}
	if err != nil {
		return err
	}
	toID, err := q.UpsertCart(ctx, sqlcgen.UpsertCartParams{ID: id.New(), Owner: to})
	if err != nil {
		return err
	}
	if err := q.MergeCartItems(ctx, sqlcgen.MergeCartItemsParams{ToCart: toID, FromCart: fromID}); err != nil {
		return err
	}
	if err := q.DeleteCartByOwner(ctx, from); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repo) PurgeGuests(ctx context.Context, before time.Time) (int64, error) {
	return r.q(ctx).PurgeGuestCarts(ctx, before)
}

func (r *Repo) ClaimAbandoned(ctx context.Context, idleBefore, notOlderThan time.Time, limit int) ([]domain.Abandoned, error) {
	q := r.q(ctx)
	rows, err := q.ClaimAbandonedCarts(ctx, sqlcgen.ClaimAbandonedCartsParams{IdleBefore: idleBefore, NotOlderThan: notOlderThan, MaxRows: int32(limit)})
	if err != nil {
		return nil, err
	}
	var out []domain.Abandoned
	for _, c := range rows {
		uid, err := uuid.Parse(strings.TrimPrefix(c.Owner, "user:"))
		if err != nil {
			continue
		}
		if err := q.MarkCartReminded(ctx, c.ID); err != nil {
			return nil, err
		}
		out = append(out, domain.Abandoned{UserID: uid, Owner: c.Owner, Items: int(c.Items)})
	}
	return out, nil
}
