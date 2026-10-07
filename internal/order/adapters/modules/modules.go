// Package modules adapts other modules' public APIs to the order module's ports.
package modules

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/cart"
	"github.com/NaheedRayan/goat-architecture/internal/identity"
	"github.com/NaheedRayan/goat-architecture/internal/inventory"
	"github.com/NaheedRayan/goat-architecture/internal/order/app"
	"github.com/NaheedRayan/goat-architecture/internal/order/domain"
	"github.com/NaheedRayan/goat-architecture/internal/payment"
	"github.com/NaheedRayan/goat-architecture/internal/promotion"
	"github.com/NaheedRayan/goat-architecture/internal/shipping"
)

type Carts struct{ API cart.API }

func (c Carts) View(ctx context.Context, owner string) (app.CartView, error) {
	v, err := c.API.View(ctx, owner)
	if err != nil {
		return app.CartView{}, err
	}
	out := app.CartView{TotalCents: v.TotalCents, Currency: v.Currency}
	for _, l := range v.Lines {
		out.Lines = append(out.Lines, app.CartLine{VariantID: l.VariantID, ProductID: l.ProductID, Name: l.Name, Label: l.Label, SKU: l.SKU,
			UnitPriceCents: l.UnitPriceCents, Quantity: l.Quantity})
	}
	return out, nil
}

func (c Carts) Clear(ctx context.Context, owner string) error { return c.API.Clear(ctx, owner) }

type Inventory struct{ API inventory.API }

func (i Inventory) Reserve(ctx context.Context, orderID uuid.UUID, items []domain.Item) error {
	in := make([]inventory.Item, len(items))
	for k, it := range items {
		in[k] = inventory.Item{VariantID: it.VariantID, Quantity: it.Quantity}
	}
	err := i.API.Reserve(ctx, orderID, in)
	var oos inventory.InsufficientStockError
	if errors.As(err, &oos) {
		return domain.InsufficientStockError{VariantID: oos.VariantID}
	}
	return err
}

func (i Inventory) Commit(ctx context.Context, orderID uuid.UUID) error {
	err := i.API.Commit(ctx, orderID)
	if errors.Is(err, inventory.ErrReservationLost) {
		return domain.ErrStockLost
	}
	return err
}

func (i Inventory) Release(ctx context.Context, orderID uuid.UUID) error {
	return i.API.Release(ctx, orderID)
}

func (i Inventory) Return(ctx context.Context, orderID uuid.UUID) error {
	return i.API.Return(ctx, orderID)
}

type Payments struct{ API payment.API }

func (p Payments) CreateIntent(ctx context.Context, orderID, userID uuid.UUID, amountCents int64, currency, method string) (uuid.UUID, error) {
	pay, err := p.API.CreateIntent(ctx, orderID, userID, amountCents, currency, method)
	return pay.ID, err
}

func (p Payments) Fail(ctx context.Context, paymentID uuid.UUID) (string, error) {
	pay, err := p.API.Fail(ctx, paymentID)
	return pay.Status, err
}

func (p Payments) CollectCOD(ctx context.Context, paymentID uuid.UUID) error {
	_, err := p.API.CollectCOD(ctx, paymentID)
	return err
}

func (p Payments) Refund(ctx context.Context, paymentID uuid.UUID) error {
	_, err := p.API.Refund(ctx, paymentID)
	return err
}

type Shipping struct{ API shipping.API }

func (s Shipping) Options(ctx context.Context, subtotalCents int64) ([]app.ShippingOption, error) {
	opts, err := s.API.Options(ctx, subtotalCents)
	if err != nil {
		return nil, err
	}
	out := make([]app.ShippingOption, len(opts))
	for i, o := range opts {
		out[i] = app.ShippingOption{ID: o.ID, Name: o.Name, Description: o.Description, PriceCents: o.PriceCents, Free: o.Free, Eta: o.Eta}
	}
	return out, nil
}

type Promotions struct{ API promotion.API }

// rejected converts the promotion module's shopper-facing error to the order module's.
func rejected(err error) error {
	var ce promotion.CouponError
	if errors.As(err, &ce) {
		return domain.CouponRejectedError{Msg: ce.Msg}
	}
	return err
}

func (p Promotions) Evaluate(ctx context.Context, code string, userID uuid.UUID, subtotalCents, shippingCents int64) (app.AppliedCoupon, error) {
	a, err := p.API.Evaluate(ctx, code, userID, subtotalCents, shippingCents)
	if err != nil {
		return app.AppliedCoupon{}, rejected(err)
	}
	return app.AppliedCoupon{CouponID: a.CouponID, Code: a.Code, DiscountCents: a.DiscountCents, FreeShipping: a.FreeShipping}, nil
}

func (p Promotions) Redeem(ctx context.Context, a app.AppliedCoupon, orderID, userID uuid.UUID, subtotalCents, shippingCents int64) error {
	return rejected(p.API.Redeem(ctx, promotion.Applied{CouponID: a.CouponID, Code: a.Code, DiscountCents: a.DiscountCents, FreeShipping: a.FreeShipping},
		orderID, userID, subtotalCents, shippingCents))
}

func (p Promotions) Release(ctx context.Context, orderID uuid.UUID) error {
	return p.API.Release(ctx, orderID)
}

type Customers struct{ API identity.API }

func (c Customers) Contact(ctx context.Context, userID uuid.UUID) (app.Contact, error) {
	u, err := c.API.User(ctx, userID)
	if err != nil {
		return app.Contact{}, err
	}
	return app.Contact{Email: u.Email, Name: u.Name}, nil
}

type Addresses struct{ API identity.API }

func (a Addresses) List(ctx context.Context, userID uuid.UUID) ([]app.SavedAddress, error) {
	as, err := a.API.ListAddresses(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]app.SavedAddress, len(as))
	for i, x := range as {
		out[i] = app.SavedAddress{FullName: x.FullName, Phone: x.Phone, Line1: x.Line1, Line2: x.Line2,
			City: x.City, PostalCode: x.PostalCode, Country: x.Country, IsDefault: x.IsDefault}
	}
	return out, nil
}
