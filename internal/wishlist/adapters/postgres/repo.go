// Package postgres implements the wishlist repository with sqlc-generated queries.
package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/platform/db"
	"github.com/NaheedRayan/goat-architecture/internal/wishlist/adapters/postgres/sqlcgen"
)

type Repo struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) q(ctx context.Context) *sqlcgen.Queries { return sqlcgen.New(db.Q(ctx, r.pool)) }

func (r *Repo) Add(ctx context.Context, userID, productID uuid.UUID) error {
	return r.q(ctx).AddWish(ctx, sqlcgen.AddWishParams{UserID: userID, ProductID: productID})
}

func (r *Repo) Remove(ctx context.Context, userID, productID uuid.UUID) error {
	_, err := r.q(ctx).RemoveWish(ctx, sqlcgen.RemoveWishParams{UserID: userID, ProductID: productID})
	return err
}

func (r *Repo) Has(ctx context.Context, userID, productID uuid.UUID) (bool, error) {
	return r.q(ctx).HasWish(ctx, sqlcgen.HasWishParams{UserID: userID, ProductID: productID})
}

func (r *Repo) List(ctx context.Context, userID uuid.UUID, limit int) ([]uuid.UUID, error) {
	return r.q(ctx).ListWishes(ctx, sqlcgen.ListWishesParams{UserID: userID, Limit: int32(limit)})
}

func (r *Repo) Count(ctx context.Context, userID uuid.UUID) (int, error) {
	n, err := r.q(ctx).CountWishes(ctx, userID)
	return int(n), err
}

func (r *Repo) DeleteUser(ctx context.Context, userID uuid.UUID) error {
	_, err := r.q(ctx).DeleteUserWishes(ctx, userID)
	return err
}
