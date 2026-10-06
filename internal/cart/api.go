// Package cart is the public surface of the cart module.
package cart

import (
	"context"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/cart/domain"
)

type (
	View = domain.View
	Line = domain.Line
)

// UserOwner returns the cart owner key for a signed-in user.
func UserOwner(id uuid.UUID) string { return domain.UserOwner(id) }

type API interface {
	View(ctx context.Context, owner string) (View, error)
	Clear(ctx context.Context, owner string) error
}
