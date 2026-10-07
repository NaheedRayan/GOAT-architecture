// Package app contains the coupon use cases.
package app

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/platform/id"
	"github.com/NaheedRayan/goat-architecture/internal/platform/money"
	"github.com/NaheedRayan/goat-architecture/internal/promotion/domain"
)

type Repository interface {
	ByCode(ctx context.Context, code string) (domain.Coupon, error)
	Get(ctx context.Context, id uuid.UUID) (domain.Coupon, error)
	Lock(ctx context.Context, id uuid.UUID) (domain.Coupon, error) // SELECT ... FOR UPDATE
	Redemptions(ctx context.Context, couponID uuid.UUID) (int, error)
	UserRedemptions(ctx context.Context, couponID, userID uuid.UUID) (int, error)
	InsertRedemption(ctx context.Context, couponID, orderID, userID uuid.UUID, discountCents int64) error
	DeleteRedemption(ctx context.Context, orderID uuid.UUID) error
	List(ctx context.Context) ([]domain.Coupon, error)
	Insert(ctx context.Context, c domain.Coupon) error // domain.ValidationError when the code exists
	Update(ctx context.Context, c domain.Coupon) error
	Delete(ctx context.Context, id uuid.UUID) error // domain.ErrInUse when redeemed
}

type Service struct {
	repo     Repository
	currency string
	now      func() time.Time
}

func NewService(repo Repository, currency string) *Service {
	return &Service{repo: repo, currency: currency, now: time.Now}
}

func (s *Service) fmtMoney(c int64) string { return money.Format(c, s.currency) }

var errInvalid = domain.CouponError{Msg: "That code isn't valid."}

// Evaluate checks a code against a basket and reports the discount. It does not
// consume a use: that happens in Redeem, when the order is created. userID may be
// uuid.Nil for a visitor who has not signed in yet (per-user limits are enforced at Redeem).
func (s *Service) Evaluate(ctx context.Context, code string, userID uuid.UUID, subtotalCents, shippingCents int64) (domain.Applied, error) {
	code = domain.NormalizeCode(code)
	if code == "" {
		return domain.Applied{}, errInvalid
	}
	c, err := s.repo.ByCode(ctx, code)
	if err != nil {
		if err == domain.ErrNotFound {
			return domain.Applied{}, errInvalid
		}
		return domain.Applied{}, err
	}
	return s.apply(ctx, c, userID, subtotalCents, shippingCents)
}

func (s *Service) apply(ctx context.Context, c domain.Coupon, userID uuid.UUID, subtotalCents, shippingCents int64) (domain.Applied, error) {
	if err := c.Check(s.now(), subtotalCents, shippingCents, s.fmtMoney); err != nil {
		return domain.Applied{}, err
	}
	if c.MaxUses != nil {
		n, err := s.repo.Redemptions(ctx, c.ID)
		if err != nil {
			return domain.Applied{}, err
		}
		if n >= *c.MaxUses {
			return domain.Applied{}, domain.CouponError{Msg: "That code has been fully redeemed."}
		}
	}
	if c.MaxUsesPerUser != nil && userID != uuid.Nil {
		n, err := s.repo.UserRedemptions(ctx, c.ID, userID)
		if err != nil {
			return domain.Applied{}, err
		}
		if n >= *c.MaxUsesPerUser {
			return domain.Applied{}, domain.CouponError{Msg: "You have already used that code."}
		}
	}
	return domain.Applied{CouponID: c.ID, Code: c.Code, DiscountCents: c.Discount(subtotalCents), FreeShipping: c.Kind == domain.KindFreeShipping}, nil
}

// Redeem consumes one use for an order. It locks the coupon row, so inside the
// checkout transaction two shoppers cannot both take the last use.
func (s *Service) Redeem(ctx context.Context, a domain.Applied, orderID, userID uuid.UUID, subtotalCents, shippingCents int64) error {
	c, err := s.repo.Lock(ctx, a.CouponID)
	if err != nil {
		return errInvalid
	}
	again, err := s.apply(ctx, c, userID, subtotalCents, shippingCents)
	if err != nil {
		return err
	}
	if again.DiscountCents != a.DiscountCents || again.FreeShipping != a.FreeShipping {
		return domain.CouponError{Msg: "That code changed while you were checking out. Please review your total."}
	}
	return s.repo.InsertRedemption(ctx, c.ID, orderID, userID, a.DiscountCents)
}

// Release gives a use back when an order is cancelled before it was fulfilled.
func (s *Service) Release(ctx context.Context, orderID uuid.UUID) error {
	return s.repo.DeleteRedemption(ctx, orderID)
}

// ---- administration ----

func (s *Service) List(ctx context.Context) ([]domain.Coupon, error) { return s.repo.List(ctx) }

func (s *Service) Get(ctx context.Context, couponID uuid.UUID) (domain.Coupon, error) {
	return s.repo.Get(ctx, couponID)
}

func (s *Service) Create(ctx context.Context, c domain.Coupon) (domain.Coupon, error) {
	c.Code = domain.NormalizeCode(c.Code)
	if err := c.Validate(); err != nil {
		return c, err
	}
	c.ID = id.New()
	return c, s.repo.Insert(ctx, c)
}

func (s *Service) Update(ctx context.Context, c domain.Coupon) error {
	cur, err := s.repo.Get(ctx, c.ID)
	if err != nil {
		return err
	}
	c.Code, c.Kind = cur.Code, cur.Kind // immutable
	if err := c.Validate(); err != nil {
		return err
	}
	return s.repo.Update(ctx, c)
}

func (s *Service) Delete(ctx context.Context, couponID uuid.UUID) error {
	return s.repo.Delete(ctx, couponID)
}
