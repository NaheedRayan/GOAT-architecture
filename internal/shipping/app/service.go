// Package app contains the shipping use cases.
package app

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/platform/id"
	"github.com/NaheedRayan/goat-architecture/internal/shipping/domain"
)

type Repository interface {
	List(ctx context.Context, activeOnly bool) ([]domain.Method, error)
	Get(ctx context.Context, id uuid.UUID) (domain.Method, error)
	Insert(ctx context.Context, m domain.Method) error
	Update(ctx context.Context, m domain.Method) error
	Delete(ctx context.Context, id uuid.UUID) error
	NextPosition(ctx context.Context) (int, error)
}

type Service struct{ repo Repository }

func NewService(repo Repository) *Service { return &Service{repo: repo} }

// Options lists the active delivery methods priced for a basket subtotal.
func (s *Service) Options(ctx context.Context, subtotalCents int64) ([]domain.Option, error) {
	ms, err := s.repo.List(ctx, true)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Option, len(ms))
	for i, m := range ms {
		out[i] = m.OptionFor(subtotalCents)
	}
	return out, nil
}

// Resolve prices one method; it must be active.
func (s *Service) Resolve(ctx context.Context, methodID uuid.UUID, subtotalCents int64) (domain.Option, error) {
	m, err := s.repo.Get(ctx, methodID)
	if err != nil || !m.Active {
		return domain.Option{}, domain.ErrNotAvailable
	}
	return m.OptionFor(subtotalCents), nil
}

func (s *Service) All(ctx context.Context) ([]domain.Method, error) { return s.repo.List(ctx, false) }

func (s *Service) Get(ctx context.Context, methodID uuid.UUID) (domain.Method, error) {
	return s.repo.Get(ctx, methodID)
}

func clean(m domain.Method) domain.Method {
	m.Name = strings.Join(strings.Fields(m.Name), " ")
	m.Description = strings.TrimSpace(m.Description)
	return m
}

func (s *Service) Create(ctx context.Context, m domain.Method) (domain.Method, error) {
	m = clean(m)
	if err := m.Validate(); err != nil {
		return m, err
	}
	pos, err := s.repo.NextPosition(ctx)
	if err != nil {
		return m, err
	}
	m.ID, m.Position = id.New(), pos
	return m, s.repo.Insert(ctx, m)
}

func (s *Service) Update(ctx context.Context, m domain.Method) error {
	m = clean(m)
	if err := m.Validate(); err != nil {
		return err
	}
	cur, err := s.repo.Get(ctx, m.ID)
	if err != nil {
		return err
	}
	m.Position = cur.Position
	return s.repo.Update(ctx, m)
}

func (s *Service) Delete(ctx context.Context, methodID uuid.UUID) error {
	return s.repo.Delete(ctx, methodID)
}
