// Package app contains the content-page use cases.
package app

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/content/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/id"
)

type Repository interface {
	List(ctx context.Context, publishedOnly bool) ([]domain.Page, error)
	BySlug(ctx context.Context, slug string) (domain.Page, error)
	Get(ctx context.Context, id uuid.UUID) (domain.Page, error)
	Insert(ctx context.Context, p domain.Page) error // domain.ErrSlugTaken
	Update(ctx context.Context, p domain.Page) error // domain.ErrNotFound / ErrSlugTaken
	Delete(ctx context.Context, id uuid.UUID) error
	NextPosition(ctx context.Context) (int, error)
}

type Service struct {
	repo Repository

	mu      sync.Mutex
	links   []domain.Link
	linksAt time.Time
}

func NewService(repo Repository) *Service { return &Service{repo: repo} }

const linkTTL = 30 * time.Second

// PublishedLinks lists the published pages for the footer. The list is cached for
// a few seconds because it is needed on every page view.
func (s *Service) PublishedLinks(ctx context.Context) ([]domain.Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.links != nil && time.Since(s.linksAt) < linkTTL {
		return s.links, nil
	}
	pages, err := s.repo.List(ctx, true)
	if err != nil {
		return nil, err
	}
	links := make([]domain.Link, 0, len(pages))
	for _, p := range pages {
		links = append(links, domain.Link{Slug: p.Slug, Title: p.Title})
	}
	s.links, s.linksAt = links, time.Now()
	return links, nil
}

func (s *Service) forgetLinks() {
	s.mu.Lock()
	s.links = nil
	s.mu.Unlock()
}

// Published returns a page only if it is live.
func (s *Service) Published(ctx context.Context, slug string) (domain.Page, error) {
	p, err := s.repo.BySlug(ctx, slug)
	if err != nil {
		return p, err
	}
	if !p.Published {
		return domain.Page{}, domain.ErrNotFound
	}
	return p, nil
}

func (s *Service) All(ctx context.Context) ([]domain.Page, error) { return s.repo.List(ctx, false) }

func (s *Service) Get(ctx context.Context, pageID uuid.UUID) (domain.Page, error) {
	return s.repo.Get(ctx, pageID)
}

func clean(p domain.Page) domain.Page {
	p.Slug = strings.ToLower(strings.TrimSpace(p.Slug))
	p.Title = strings.Join(strings.Fields(p.Title), " ")
	p.Body = strings.ReplaceAll(strings.TrimSpace(p.Body), "\r\n", "\n")
	return p
}

func (s *Service) Create(ctx context.Context, p domain.Page) (domain.Page, error) {
	p = clean(p)
	if err := p.Validate(); err != nil {
		return p, err
	}
	pos, err := s.repo.NextPosition(ctx)
	if err != nil {
		return p, err
	}
	p.ID, p.Position = id.New(), pos
	defer s.forgetLinks()
	return p, s.repo.Insert(ctx, p)
}

func (s *Service) Update(ctx context.Context, p domain.Page) error {
	p = clean(p)
	if err := p.Validate(); err != nil {
		return err
	}
	defer s.forgetLinks()
	return s.repo.Update(ctx, p)
}

func (s *Service) Delete(ctx context.Context, pageID uuid.UUID) error {
	defer s.forgetLinks()
	return s.repo.Delete(ctx, pageID)
}

// UnpublishedCount is how many pages are still drafts (the admin dashboard nudges about them).
func (s *Service) UnpublishedCount(ctx context.Context) (int, error) {
	pages, err := s.repo.List(ctx, false)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range pages {
		if !p.Published {
			n++
		}
	}
	return n, nil
}
