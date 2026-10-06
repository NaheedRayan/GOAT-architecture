// Package postgres implements the payment repository with sqlc-generated queries.
package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/payment/adapters/postgres/sqlcgen"
	"github.com/NaheedRayan/goat-architecture/internal/payment/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/db"
)

type Repo struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) q(ctx context.Context) *sqlcgen.Queries { return sqlcgen.New(db.Q(ctx, r.pool)) }

func toPayment(p sqlcgen.PaymentPayment) domain.Payment {
	return domain.Payment{ID: p.ID, OrderID: p.OrderID, UserID: p.UserID, AmountCents: p.AmountCents,
		Currency: p.Currency, Provider: p.Provider, ProviderRef: p.ProviderRef, Status: p.Status}
}

func (r *Repo) Insert(ctx context.Context, p domain.Payment) error {
	return r.q(ctx).InsertPayment(ctx, sqlcgen.InsertPaymentParams{
		ID: p.ID, OrderID: p.OrderID, UserID: p.UserID, AmountCents: p.AmountCents, Currency: p.Currency, Provider: p.Provider,
	})
}

func (r *Repo) Get(ctx context.Context, paymentID uuid.UUID) (domain.Payment, error) {
	p, err := r.q(ctx).GetPayment(ctx, paymentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Payment{}, domain.ErrNotFound
	}
	return toPayment(p), err
}

func (r *Repo) Settle(ctx context.Context, paymentID uuid.UUID, status, providerRef string) (domain.Payment, bool, error) {
	p, err := r.q(ctx).SettlePayment(ctx, sqlcgen.SettlePaymentParams{ID: paymentID, Status: status, ProviderRef: providerRef})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Payment{}, false, nil
	}
	return toPayment(p), err == nil, err
}
