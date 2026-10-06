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
)

type Carts struct{ API cart.API }

func (c Carts) View(ctx context.Context, owner string) (app.CartView, error) {
	v, err := c.API.View(ctx, owner)
	if err != nil {
		return app.CartView{}, err
	}
	out := app.CartView{TotalCents: v.TotalCents, Currency: v.Currency}
	for _, l := range v.Lines {
		out.Lines = append(out.Lines, app.CartLine{ProductID: l.ProductID, Name: l.Name, UnitPriceCents: l.UnitPriceCents, Quantity: l.Quantity})
	}
	return out, nil
}

func (c Carts) Clear(ctx context.Context, owner string) error { return c.API.Clear(ctx, owner) }

type Inventory struct{ API inventory.API }

func (i Inventory) Reserve(ctx context.Context, orderID uuid.UUID, items []domain.Item) error {
	in := make([]inventory.Item, len(items))
	for k, it := range items {
		in[k] = inventory.Item{ProductID: it.ProductID, Quantity: it.Quantity}
	}
	err := i.API.Reserve(ctx, orderID, in)
	var oos inventory.InsufficientStockError
	if errors.As(err, &oos) {
		return domain.InsufficientStockError{ProductID: oos.ProductID}
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

type Payments struct{ API payment.API }

func (p Payments) CreateIntent(ctx context.Context, orderID, userID uuid.UUID, amountCents int64, currency string) (uuid.UUID, error) {
	pay, err := p.API.CreateIntent(ctx, orderID, userID, amountCents, currency)
	return pay.ID, err
}

func (p Payments) Fail(ctx context.Context, paymentID uuid.UUID) (string, error) {
	pay, err := p.API.Fail(ctx, paymentID)
	return pay.Status, err
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
