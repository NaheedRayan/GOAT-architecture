package bootstrap

import (
	"context"

	"github.com/a-h/templ"
	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/catalog"
	"github.com/NaheedRayan/goat-architecture/internal/platform/auth"
	"github.com/NaheedRayan/goat-architecture/internal/review"
	"github.com/NaheedRayan/goat-architecture/internal/wishlist"
)

// storefrontExtras plugs the reviews and wishlist modules into the catalog's product
// pages. The catalog is built before those modules (they depend on it), so this is
// filled in afterwards.
type storefrontExtras struct {
	reviews  *review.Module
	wishlist wishlist.API
}

func (e *storefrontExtras) Ratings(ctx context.Context, ids []uuid.UUID) map[uuid.UUID]catalog.Rating {
	out := map[uuid.UUID]catalog.Rating{}
	sums, err := e.reviews.API().Summaries(ctx, ids)
	if err != nil {
		return out
	}
	for id, s := range sums {
		out[id] = catalog.Rating{Count: s.Count, Average: s.Average}
	}
	return out
}

func (e *storefrontExtras) Reviews(ctx context.Context, productID uuid.UUID, slug, flash string) templ.Component {
	return e.reviews.Section(ctx, productID, slug, flash)
}

func (e *storefrontExtras) Wished(ctx context.Context, productID uuid.UUID) bool {
	c, ok := auth.FromContext(ctx)
	if !ok {
		return false
	}
	has, _ := e.wishlist.Has(ctx, c.UserID, productID)
	return has
}

func (e *storefrontExtras) WishlistIDs(ctx context.Context) []uuid.UUID {
	c, ok := auth.FromContext(ctx)
	if !ok {
		return nil
	}
	ids, _ := e.wishlist.List(ctx, c.UserID)
	return ids
}
