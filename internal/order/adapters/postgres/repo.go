// Package postgres implements the order repository with sqlc-generated queries.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/order/adapters/postgres/sqlcgen"
	"github.com/NaheedRayan/goat-architecture/internal/order/app"
	"github.com/NaheedRayan/goat-architecture/internal/order/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/db"
	"github.com/NaheedRayan/goat-architecture/internal/platform/id"
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
		IdempotencyKey: o.IdempotencyKey, Shipping: ship, SubtotalCents: o.SubtotalCents, DiscountCents: o.DiscountCents,
		ShippingCents: o.ShippingCents, TaxCents: o.TaxCents, TaxInclusive: o.TaxInclusive, ShippingMethod: o.ShippingMethod,
		CouponCode: o.CouponCode, PaymentMethod: o.PaymentMethod, ContactEmail: o.ContactEmail,
	})
	if db.IsUniqueViolation(err) {
		return domain.ErrDuplicate
	}
	if err != nil {
		return err
	}
	for _, it := range o.Items {
		if err := q.InsertOrderItem(ctx, sqlcgen.InsertOrderItemParams{
			OrderID: o.ID, VariantID: it.VariantID, ProductID: it.ProductID, Name: it.Name, VariantLabel: it.VariantLabel, Sku: it.SKU,
			UnitPriceCents: it.UnitPriceCents, Quantity: int32(it.Quantity),
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
	return domain.Order{
		ID: o.ID, UserID: o.UserID, Status: o.Status,
		SubtotalCents: o.SubtotalCents, DiscountCents: o.DiscountCents, ShippingCents: o.ShippingCents, TaxCents: o.TaxCents,
		TotalCents: o.TotalCents, TaxInclusive: o.TaxInclusive, Currency: o.Currency,
		IdempotencyKey: o.IdempotencyKey, Shipping: ship, ShippingMethod: o.ShippingMethod, CouponCode: o.CouponCode,
		PaymentMethod: o.PaymentMethod, PaymentID: o.PaymentID, ContactEmail: o.ContactEmail,
		Tracking:  domain.Tracking{Carrier: o.TrackingCarrier, Number: o.TrackingNumber, URL: o.TrackingUrl},
		ShippedAt: o.ShippedAt, DeliveredAt: o.DeliveredAt, CancelledAt: o.CancelledAt, RefundedAt: o.RefundedAt, RefundCents: o.RefundCents,
		AdminNote: o.AdminNote,
		Return:    domain.Return{Status: o.ReturnStatus, Reason: o.ReturnReason, Note: o.ReturnNote, RequestedAt: o.ReturnRequestedAt},
		CreatedAt: o.CreatedAt,
	}, nil
}

// full loads items and the event timeline for an order row.
func (r *Repo) full(ctx context.Context, row sqlcgen.OrdersOrder, err error) (domain.Order, error) {
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
		o.Items = append(o.Items, domain.Item{VariantID: it.VariantID, ProductID: it.ProductID, Name: it.Name, VariantLabel: it.VariantLabel,
			SKU: it.Sku, UnitPriceCents: it.UnitPriceCents, Quantity: int(it.Quantity)})
	}
	evs, err := r.q(ctx).ListOrderEvents(ctx, o.ID)
	if err != nil {
		return domain.Order{}, err
	}
	for _, e := range evs {
		o.Events = append(o.Events, domain.Event{At: e.At, Kind: e.Kind, Detail: e.Detail})
	}
	return o, nil
}

func (r *Repo) Get(ctx context.Context, orderID uuid.UUID) (domain.Order, error) {
	row, err := r.q(ctx).GetOrder(ctx, orderID)
	return r.full(ctx, row, err)
}

func (r *Repo) GetForUpdate(ctx context.Context, orderID uuid.UUID) (domain.Order, error) {
	row, err := r.q(ctx).GetOrderForUpdate(ctx, orderID)
	return r.full(ctx, row, err)
}

func (r *Repo) ByIdempotencyKey(ctx context.Context, userID uuid.UUID, key string) (domain.Order, error) {
	row, err := r.q(ctx).GetOrderByIdempotencyKey(ctx, sqlcgen.GetOrderByIdempotencyKeyParams{UserID: userID, IdempotencyKey: key})
	return r.full(ctx, row, err)
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

func (r *Repo) Search(ctx context.Context, f app.ListFilter, limit, offset int) ([]domain.Order, error) {
	return r.list(r.q(ctx).SearchOrders(ctx, sqlcgen.SearchOrdersParams{
		Status: f.Status, ReturnStatus: f.ReturnStatus, Query: f.Query, LikePattern: likePattern(f.Query),
		CreatedFrom: f.From, CreatedTo: f.To, PageLimit: int32(limit), PageOffset: int32(offset),
	}))
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

func (r *Repo) CountReturnRequests(ctx context.Context) (int, error) {
	n, err := r.q(ctx).CountReturnRequests(ctx)
	return int(n), err
}

func (r *Repo) Save(ctx context.Context, o domain.Order) error {
	n, err := r.q(ctx).SaveOrder(ctx, sqlcgen.SaveOrderParams{
		ID: o.ID, Status: o.Status, PaymentID: o.PaymentID,
		TrackingCarrier: o.Tracking.Carrier, TrackingNumber: o.Tracking.Number, TrackingUrl: o.Tracking.URL,
		ShippedAt: o.ShippedAt, DeliveredAt: o.DeliveredAt, CancelledAt: o.CancelledAt, RefundedAt: o.RefundedAt, RefundCents: o.RefundCents,
		AdminNote: o.AdminNote, ReturnStatus: o.Return.Status, ReturnReason: o.Return.Reason, ReturnNote: o.Return.Note,
		ReturnRequestedAt: o.Return.RequestedAt,
	})
	if err == nil && n == 0 {
		return domain.ErrNotFound
	}
	return err
}

func (r *Repo) AddEvent(ctx context.Context, orderID uuid.UUID, kind, detail string, actor *uuid.UUID) error {
	return r.q(ctx).InsertOrderEvent(ctx, sqlcgen.InsertOrderEventParams{ID: id.New(), OrderID: orderID, Kind: kind, Detail: detail, ActorID: actor})
}

func (r *Repo) ClaimPaid(ctx context.Context, limit int) ([]uuid.UUID, error) {
	return r.q(ctx).ClaimPaidOrders(ctx, int32(limit))
}

func (r *Repo) AnonymizeUser(ctx context.Context, userID uuid.UUID) (int64, error) {
	return r.q(ctx).AnonymizeUserOrders(ctx, userID)
}

// likePattern escapes LIKE wildcards so searches are literal.
func likePattern(q string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
}

func (r *Repo) Report(ctx context.Context, from, to time.Time) (domain.Report, error) {
	q := r.q(ctx)
	rep := domain.Report{From: from, To: to}
	t, err := q.SalesTotals(ctx, sqlcgen.SalesTotalsParams{FromAt: from, ToAt: to})
	if err != nil {
		return rep, err
	}
	rep.Orders, rep.RevenueCents = int(t.Orders), t.RevenueCents
	if rep.Orders > 0 {
		rep.AvgOrderCents = (rep.RevenueCents + int64(rep.Orders)/2) / int64(rep.Orders)
	}
	rf, err := q.RefundTotals(ctx, sqlcgen.RefundTotalsParams{FromAt: &from, ToAt: &to})
	if err != nil {
		return rep, err
	}
	rep.Refunds, rep.RefundedCents = int(rf.Orders), rf.RefundedCents
	days, err := q.SalesByDay(ctx, sqlcgen.SalesByDayParams{FromAt: from, ToAt: to})
	if err != nil {
		return rep, err
	}
	for _, d := range days {
		rep.ByDay = append(rep.ByDay, domain.DayStat{Day: d.Day.Time, Orders: int(d.Orders), RevenueCents: d.RevenueCents})
	}
	top, err := q.TopProducts(ctx, sqlcgen.TopProductsParams{FromAt: from, ToAt: to, TopLimit: 10})
	if err != nil {
		return rep, err
	}
	for _, p := range top {
		rep.Top = append(rep.Top, domain.ProductStat{ProductID: p.ProductID, Name: p.Name, Units: int(p.Units), RevenueCents: p.RevenueCents})
	}
	return rep, nil
}

func (r *Repo) HasPurchased(ctx context.Context, userID, productID uuid.UUID) (bool, error) {
	return r.q(ctx).HasPurchased(ctx, sqlcgen.HasPurchasedParams{UserID: userID, ProductID: productID})
}
