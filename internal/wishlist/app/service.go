// Package app contains the wishlist use cases.
package app

import (
	"context"

	"github.com/google/uuid"
)

// MaxItems caps a wishlist so one account cannot grow it without bound.
const MaxItems = 200

type Repository interface {
	Add(ctx context.Context, userID, productID uuid.UUID) error
	Remove(ctx context.Context, userID, productID uuid.UUID) error
	Has(ctx context.Context, userID, productID uuid.UUID) (bool, error)
	List(ctx context.Context, userID uuid.UUID, limit int) ([]uuid.UUID, error)
	Count(ctx context.Context, userID uuid.UUID) (int, error)
	DeleteUser(ctx context.Context, userID uuid.UUID) error
}

type Service struct{ repo Repository }

func NewService(r Repository) *Service { return &Service{repo: r} }

// ErrFull is returned when the wishlist has reached MaxItems.
type ErrFull struct{}

func (ErrFull) Error() string { return "your wishlist is full" }

func (s *Service) Add(ctx context.Context, userID, productID uuid.UUID) error {
	if has, err := s.repo.Has(ctx, userID, productID); err != nil || has {
		return err
	}
	if n, err := s.repo.Count(ctx, userID); err != nil {
		return err
	} else if n >= MaxItems {
		return ErrFull{}
	}
	return s.repo.Add(ctx, userID, productID)
}

func (s *Service) Remove(ctx context.Context, userID, productID uuid.UUID) error {
	return s.repo.Remove(ctx, userID, productID)
}

func (s *Service) Has(ctx context.Context, userID, productID uuid.UUID) (bool, error) {
	return s.repo.Has(ctx, userID, productID)
}

func (s *Service) List(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	return s.repo.List(ctx, userID, MaxItems)
}

func (s *Service) DeleteUser(ctx context.Context, userID uuid.UUID) error {
	return s.repo.DeleteUser(ctx, userID)
}
