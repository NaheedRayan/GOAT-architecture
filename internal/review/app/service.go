// Package app contains the review use cases.
package app

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/platform/id"
	"github.com/NaheedRayan/goat-architecture/internal/review/domain"
)

type Repository interface {
	Upsert(ctx context.Context, r domain.Review) (domain.Review, error)
	ByUser(ctx context.Context, productID, userID uuid.UUID) (domain.Review, error) // domain.ErrNotFound
	Published(ctx context.Context, productID uuid.UUID, limit int) ([]domain.Review, error)
	Summaries(ctx context.Context, productIDs []uuid.UUID) (map[uuid.UUID]domain.Summary, error)
	Distribution(ctx context.Context, productID uuid.UUID) ([6]int, error)
	All(ctx context.Context, status string, limit, offset int) ([]domain.Review, error)
	SetStatus(ctx context.Context, id uuid.UUID, status string) error
	Delete(ctx context.Context, id uuid.UUID) error
	DeleteByUser(ctx context.Context, userID uuid.UUID) (int64, error)
	ListByUser(ctx context.Context, userID uuid.UUID) ([]domain.Review, error)
}

// Purchases tells whether a customer has received a product.
type Purchases interface {
	HasPurchased(ctx context.Context, userID, productID uuid.UUID) (bool, error)
}

type Service struct {
	repo      Repository
	purchases Purchases
	now       func() time.Time
}

func NewService(repo Repository, purchases Purchases) *Service {
	return &Service{repo: repo, purchases: purchases, now: time.Now}
}

// Submit writes (or edits) the customer's review of a product they have received.
func (s *Service) Submit(ctx context.Context, userID, productID uuid.UUID, authorFullName string, rating int, title, body string) (domain.Review, error) {
	rv := domain.Review{
		ID: id.New(), ProductID: productID, UserID: userID, Rating: rating, Author: domain.DisplayName(authorFullName),
		Title: strings.Join(strings.Fields(title), " "), Body: strings.TrimSpace(body),
	}
	if err := rv.Validate(); err != nil {
		return rv, err
	}
	ok, err := s.purchases.HasPurchased(ctx, userID, productID)
	if err != nil {
		return rv, err
	}
	if !ok {
		return rv, domain.ErrNotPurchased
	}
	return s.repo.Upsert(ctx, rv)
}

// CanReview reports whether the customer has received the product.
func (s *Service) CanReview(ctx context.Context, userID, productID uuid.UUID) (bool, error) {
	return s.purchases.HasPurchased(ctx, userID, productID)
}

func (s *Service) Mine(ctx context.Context, userID, productID uuid.UUID) (*domain.Review, error) {
	r, err := s.repo.ByUser(ctx, productID, userID)
	if err == domain.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ForProduct returns the newest published reviews and the rating summary.
func (s *Service) ForProduct(ctx context.Context, productID uuid.UUID, limit int) ([]domain.Review, domain.Summary, error) {
	rs, err := s.repo.Published(ctx, productID, limit)
	if err != nil {
		return nil, domain.Summary{}, err
	}
	sums, err := s.repo.Summaries(ctx, []uuid.UUID{productID})
	if err != nil {
		return nil, domain.Summary{}, err
	}
	sum := sums[productID]
	if sum.Stars, err = s.repo.Distribution(ctx, productID); err != nil {
		return nil, sum, err
	}
	return rs, sum, nil
}

func (s *Service) Summaries(ctx context.Context, productIDs []uuid.UUID) (map[uuid.UUID]domain.Summary, error) {
	if len(productIDs) == 0 {
		return map[uuid.UUID]domain.Summary{}, nil
	}
	return s.repo.Summaries(ctx, productIDs)
}

// ---- moderation ----

func (s *Service) All(ctx context.Context, status string, limit, offset int) ([]domain.Review, error) {
	return s.repo.All(ctx, status, limit, max(offset, 0))
}

func (s *Service) SetStatus(ctx context.Context, reviewID uuid.UUID, status string) error {
	if status != domain.StatusPublished && status != domain.StatusHidden {
		return domain.ValidationError("unknown status")
	}
	return s.repo.SetStatus(ctx, reviewID, status)
}

func (s *Service) Delete(ctx context.Context, reviewID uuid.UUID) error {
	return s.repo.Delete(ctx, reviewID)
}

// ---- privacy ----

func (s *Service) DeleteUser(ctx context.Context, userID uuid.UUID) error {
	_, err := s.repo.DeleteByUser(ctx, userID)
	return err
}

func (s *Service) ExportUser(ctx context.Context, userID uuid.UUID) ([]domain.Review, error) {
	return s.repo.ListByUser(ctx, userID)
}
