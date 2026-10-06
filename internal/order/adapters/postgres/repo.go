// Package postgres implements the order repository with sqlc-generated queries.
package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/order/adapters/postgres/sqlcgen"
	"github.com/NaheedRayan/goat-architecture/internal/order/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/db"
)

type Repo struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) q(ctx context.Context) *sqlcgen.Queries { return sqlcgen.New(db.Q(ctx, r.pool)) }

func (r *Repo) Insert(ctx context.Context, o domain.Order) error {
	ship, err := json.Marshal(o.Shipping)
	if err != nil {
		return err
	}
	q := r.q(ctx)
	err = q.InsertOrder(ctx, sqlcgen.InsertOrderParams{
		ID: o.ID, UserID: o.UserID, Status: o.Status, TotalCents: o.TotalCents, Currency: o.Currency,
		IdempotencyKey: o.IdempotencyKey, Shipping: ship,
	})
	if db.IsUniqueViolation(err) {
		return domain.ErrDuplicate
	}
	if err != nil {
		return err
	}
	for _, it := range o.Items {
		if err := q.InsertOrderItem(ctx, sqlcgen.InsertOrderItemParams{
			OrderID: o.ID, ProductID: it.ProductID, Name: it.Name, UnitPriceCents: it.UnitPriceCents, Quantity: int32(it.Quantity),
		}); err != nil {
			return err
		}
	}
	return nil
}

func toOrder(o sqlcgen.OrdersOrder) (domain.Order, error) {
	var ship domain.ShippingAddress
	if err := json.Unmarshal(o.Shipping, &ship); err != nil {
		return domain.Order{}, err
	}
	return domain.Order{ID: o.ID, UserID: o.UserID, Status: o.Status, TotalCents: o.TotalCents, Currency: o.Currency,
		IdempotencyKey: o.IdempotencyKey, Shipping: ship, PaymentID: o.PaymentID, CreatedAt: o.CreatedAt}, nil
}

func (r *Repo) withItems(ctx context.Context, row sqlcgen.OrdersOrder, err error) (domain.Order, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Order{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Order{}, err
	}
	o, err := toOrder(row)
	if err != nil {
		return domain.Order{}, err
	}
	items, err := r.q(ctx).ListOrderItems(ctx, o.ID)
	if err != nil {
		return domain.Order{}, err
	}
	for _, it := range items {
		o.Items = append(o.Items, domain.Item{ProductID: it.ProductID, Name: it.Name, UnitPriceCents: it.UnitPriceCents, Quantity: int(it.Quantity)})
	}
	return o, nil
}

func (r *Repo) Get(ctx context.Context, orderID uuid.UUID) (domain.Order, error) {
	row, err := r.q(ctx).GetOrder(ctx, orderID)
	return r.withItems(ctx, row, err)
}

func (r *Repo) GetForUpdate(ctx context.Context, orderID uuid.UUID) (domain.Order, error) {
	row, err := r.q(ctx).GetOrderForUpdate(ctx, orderID)
	return r.withItems(ctx, row, err)
}

func (r *Repo) ByIdempotencyKey(ctx context.Context, userID uuid.UUID, key string) (domain.Order, error) {
	row, err := r.q(ctx).GetOrderByIdempotencyKey(ctx, sqlcgen.GetOrderByIdempotencyKeyParams{UserID: userID, IdempotencyKey: key})
	return r.withItems(ctx, row, err)
}

func (r *Repo) list(rows []sqlcgen.OrdersOrder, err error) ([]domain.Order, error) {
	if err != nil {
		return nil, err
	}
	out := make([]domain.Order, 0, len(rows))
	for _, row := range rows {
		o, err := toOrder(row)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

func (r *Repo) ListByUser(ctx context.Context, userID uuid.UUID, limit int) ([]domain.Order, error) {
	return r.list(r.q(ctx).ListOrdersByUser(ctx, sqlcgen.ListOrdersByUserParams{UserID: userID, Limit: int32(limit)}))
}

func (r *Repo) List(ctx context.Context, status string, limit, offset int) ([]domain.Order, error) {
	return r.list(r.q(ctx).ListOrders(ctx, sqlcgen.ListOrdersParams{Status: status, PageLimit: int32(limit), PageOffset: int32(offset)}))
}

func (r *Repo) CountByStatus(ctx context.Context) (map[string]int, error) {
	rows, err := r.q(ctx).CountOrdersByStatus(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int, len(rows))
	for _, x := range rows {
		out[x.Status] = int(x.N)
	}
	return out, nil
}

func (r *Repo) SetStatus(ctx context.Context, orderID uuid.UUID, status string) error {
	n, err := r.q(ctx).SetOrderStatus(ctx, sqlcgen.SetOrderStatusParams{ID: orderID, Status: status})
	if err == nil && n == 0 {
		return domain.ErrNotFound
	}
	return err
}

func (r *Repo) SetPayment(ctx context.Context, orderID, paymentID uuid.UUID) error {
	return r.q(ctx).SetOrderPayment(ctx, sqlcgen.SetOrderPaymentParams{ID: orderID, PaymentID: &paymentID})
}

func (r *Repo) ClaimPaid(ctx context.Context, limit int) ([]uuid.UUID, error) {
	return r.q(ctx).ClaimPaidOrders(ctx, int32(limit))
}
