// Package wishlist is the public surface of the wishlist module.
package wishlist

import (
	"context"

	"github.com/google/uuid"
)

type API interface {
	Has(ctx context.Context, userID, productID uuid.UUID) (bool, error)
	List(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error)
}
