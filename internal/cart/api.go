// Package cart is the public surface of the cart module.
package cart

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/cart/domain"
)

type (
	View      = domain.View
	Line      = domain.Line
	Abandoned = domain.Abandoned
)

// UserOwner returns the cart owner key for a signed-in user.
func UserOwner(id uuid.UUID) string { return domain.UserOwner(id) }

type API interface {
	View(ctx context.Context, owner string) (View, error)
	Clear(ctx context.Context, owner string) error
	// ClaimAbandoned returns signed-in users' carts idle for idleFor (but newer than maxAge) and marks
	// them reminded. Use inside the transaction that queues the reminder.
	ClaimAbandoned(ctx context.Context, idleFor, maxAge time.Duration, limit int) ([]Abandoned, error)
}
