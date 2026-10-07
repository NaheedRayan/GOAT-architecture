// Package postgres implements the shipping repository with sqlc-generated queries.
package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/platform/db"
	"github.com/NaheedRayan/goat-architecture/internal/shipping/adapters/postgres/sqlcgen"
	"github.com/NaheedRayan/goat-architecture/internal/shipping/domain"
)

type Repo struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) q(ctx context.Context) *sqlcgen.Queries { return sqlcgen.New(db.Q(ctx, r.pool)) }

func toMethod(m sqlcgen.ShippingMethod) domain.Method {
	return domain.Method{ID: m.ID, Name: m.Name, Description: m.Description, PriceCents: m.PriceCents, FreeOverCents: m.FreeOverCents,
		MinDays: int(m.MinDays), MaxDays: int(m.MaxDays), Active: m.Active, Position: int(m.Position)}
}

func (r *Repo) List(ctx context.Context, activeOnly bool) ([]domain.Method, error) {
	var rows []sqlcgen.ShippingMethod
	var err error
	if activeOnly {
		rows, err = r.q(ctx).ListActiveMethods(ctx)
	} else {
		rows, err = r.q(ctx).ListMethods(ctx)
	}
	if err != nil {
		return nil, err
	}
	out := make([]domain.Method, len(rows))
	for i, m := range rows {
		out[i] = toMethod(m)
	}
	return out, nil
}

func (r *Repo) Get(ctx context.Context, id uuid.UUID) (domain.Method, error) {
	m, err := r.q(ctx).GetMethod(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Method{}, domain.ErrNotFound
	}
	return toMethod(m), err
}

func (r *Repo) Insert(ctx context.Context, m domain.Method) error {
	return r.q(ctx).InsertMethod(ctx, sqlcgen.InsertMethodParams{
		ID: m.ID, Name: m.Name, Description: m.Description, PriceCents: m.PriceCents, FreeOverCents: m.FreeOverCents,
		MinDays: int32(m.MinDays), MaxDays: int32(m.MaxDays), Active: m.Active, Position: int32(m.Position),
	})
}

func (r *Repo) Update(ctx context.Context, m domain.Method) error {
	n, err := r.q(ctx).UpdateMethod(ctx, sqlcgen.UpdateMethodParams{
		ID: m.ID, Name: m.Name, Description: m.Description, PriceCents: m.PriceCents, FreeOverCents: m.FreeOverCents,
		MinDays: int32(m.MinDays), MaxDays: int32(m.MaxDays), Active: m.Active, Position: int32(m.Position),
	})
	if err == nil && n == 0 {
		return domain.ErrNotFound
	}
	return err
}

func (r *Repo) Delete(ctx context.Context, id uuid.UUID) error {
	n, err := r.q(ctx).DeleteMethod(ctx, id)
	if err == nil && n == 0 {
		return domain.ErrNotFound
	}
	return err
}

func (r *Repo) NextPosition(ctx context.Context) (int, error) {
	n, err := r.q(ctx).NextMethodPosition(ctx)
	return int(n), err
}
