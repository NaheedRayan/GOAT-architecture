// Package review is the public surface of the product-reviews module.
package review

import (
	"context"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/review/domain"
)

type (
	Review  = domain.Review
	Summary = domain.Summary
)

var (
	ErrNotPurchased = domain.ErrNotPurchased
	ErrNotFound     = domain.ErrNotFound
)

type ValidationError = domain.ValidationError

const (
	StatusPublished = domain.StatusPublished
	StatusHidden    = domain.StatusHidden
)

type API interface {
	ForProduct(ctx context.Context, productID uuid.UUID, limit int) ([]Review, Summary, error)
	Summaries(ctx context.Context, productIDs []uuid.UUID) (map[uuid.UUID]Summary, error)
	Mine(ctx context.Context, userID, productID uuid.UUID) (*Review, error)
	CanReview(ctx context.Context, userID, productID uuid.UUID) (bool, error)
	All(ctx context.Context, status string, limit, offset int) ([]Review, error)
	SetStatus(ctx context.Context, reviewID uuid.UUID, status string) error
	Delete(ctx context.Context, reviewID uuid.UUID) error
}
