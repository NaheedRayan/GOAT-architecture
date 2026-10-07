// Package postgres implements the promotion repository with sqlc-generated queries.
package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/platform/db"
	"github.com/NaheedRayan/goat-architecture/internal/platform/id"
	"github.com/NaheedRayan/goat-architecture/internal/promotion/adapters/postgres/sqlcgen"
	"github.com/NaheedRayan/goat-architecture/internal/promotion/domain"
)

type Repo struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) q(ctx context.Context) *sqlcgen.Queries { return sqlcgen.New(db.Q(ctx, r.pool)) }

func intPtr(p *int32) *int {
	if p == nil {
		return nil
	}
	v := int(*p)
	return &v
}

func int32Ptr(p *int) *int32 {
	if p == nil {
		return nil
	}
	v := int32(*p)
	return &v
}

func toCoupon(c sqlcgen.PromotionCoupon) domain.Coupon {
	return domain.Coupon{ID: c.ID, Code: c.Code, Kind: c.Kind, Value: c.Value, MinSubtotal: c.MinSubtotalCents, StartsAt: c.StartsAt,
		EndsAt: c.EndsAt, MaxUses: intPtr(c.MaxUses), MaxUsesPerUser: intPtr(c.MaxUsesPerUser), Active: c.Active}
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	return err
}

func (r *Repo) ByCode(ctx context.Context, code string) (domain.Coupon, error) {
	c, err := r.q(ctx).GetCouponByCode(ctx, code)
	return toCoupon(c), notFound(err)
}

func (r *Repo) Get(ctx context.Context, couponID uuid.UUID) (domain.Coupon, error) {
	c, err := r.q(ctx).GetCoupon(ctx, couponID)
	return toCoupon(c), notFound(err)
}

func (r *Repo) Lock(ctx context.Context, couponID uuid.UUID) (domain.Coupon, error) {
	c, err := r.q(ctx).LockCoupon(ctx, couponID)
	return toCoupon(c), notFound(err)
}

func (r *Repo) Redemptions(ctx context.Context, couponID uuid.UUID) (int, error) {
	n, err := r.q(ctx).CountRedemptions(ctx, couponID)
	return int(n), err
}

func (r *Repo) UserRedemptions(ctx context.Context, couponID, userID uuid.UUID) (int, error) {
	n, err := r.q(ctx).CountUserRedemptions(ctx, sqlcgen.CountUserRedemptionsParams{CouponID: couponID, UserID: userID})
	return int(n), err
}

func (r *Repo) InsertRedemption(ctx context.Context, couponID, orderID, userID uuid.UUID, discountCents int64) error {
	return r.q(ctx).InsertRedemption(ctx, sqlcgen.InsertRedemptionParams{
		ID: id.New(), CouponID: couponID, OrderID: orderID, UserID: userID, DiscountCents: discountCents,
	})
}

func (r *Repo) DeleteRedemption(ctx context.Context, orderID uuid.UUID) error {
	_, err := r.q(ctx).DeleteRedemptionByOrder(ctx, orderID)
	return err
}

func (r *Repo) List(ctx context.Context) ([]domain.Coupon, error) {
	rows, err := r.q(ctx).ListCoupons(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Coupon, len(rows))
	for i, x := range rows {
		out[i] = domain.Coupon{ID: x.ID, Code: x.Code, Kind: x.Kind, Value: x.Value, MinSubtotal: x.MinSubtotalCents, StartsAt: x.StartsAt,
			EndsAt: x.EndsAt, MaxUses: intPtr(x.MaxUses), MaxUsesPerUser: intPtr(x.MaxUsesPerUser), Active: x.Active, Uses: int(x.Uses)}
	}
	return out, nil
}

func (r *Repo) Insert(ctx context.Context, c domain.Coupon) error {
	err := r.q(ctx).InsertCoupon(ctx, sqlcgen.InsertCouponParams{
		ID: c.ID, Code: c.Code, Kind: c.Kind, Value: c.Value, MinSubtotalCents: c.MinSubtotal, StartsAt: c.StartsAt, EndsAt: c.EndsAt,
		MaxUses: int32Ptr(c.MaxUses), MaxUsesPerUser: int32Ptr(c.MaxUsesPerUser), Active: c.Active,
	})
	if db.IsUniqueViolation(err) {
		return domain.ValidationError("a coupon with that code already exists")
	}
	return err
}

func (r *Repo) Update(ctx context.Context, c domain.Coupon) error {
	n, err := r.q(ctx).UpdateCoupon(ctx, sqlcgen.UpdateCouponParams{
		ID: c.ID, Value: c.Value, MinSubtotalCents: c.MinSubtotal, StartsAt: c.StartsAt, EndsAt: c.EndsAt,
		MaxUses: int32Ptr(c.MaxUses), MaxUsesPerUser: int32Ptr(c.MaxUsesPerUser), Active: c.Active,
	})
	if err == nil && n == 0 {
		return domain.ErrNotFound
	}
	return err
}

func (r *Repo) Delete(ctx context.Context, couponID uuid.UUID) error {
	n, err := r.q(ctx).DeleteCoupon(ctx, couponID)
	if err != nil {
		return err
	}
	if n == 0 {
		if _, gerr := r.Get(ctx, couponID); gerr != nil {
			return domain.ErrNotFound
		}
		return domain.ErrInUse
	}
	return nil
}
