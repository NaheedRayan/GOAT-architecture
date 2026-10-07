// Package content is the public surface of the content-pages module.
package content

import (
	"context"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/content/domain"
)

type (
	Page = domain.Page
	Link = domain.Link
)

var (
	ErrNotFound  = domain.ErrNotFound
	ErrSlugTaken = domain.ErrSlugTaken
)

type ValidationError = domain.ValidationError

// Placeholders lists the [[TEMPLATE MARKERS]] still present in text.
func Placeholders(text string) []string { return domain.Placeholders(text) }

type API interface {
	All(ctx context.Context) ([]Page, error)
	Get(ctx context.Context, id uuid.UUID) (Page, error)
	Published(ctx context.Context, slug string) (Page, error)
	PublishedLinks(ctx context.Context) ([]Link, error)
	Create(ctx context.Context, p Page) (Page, error)
	Update(ctx context.Context, p Page) error
	Delete(ctx context.Context, id uuid.UUID) error
	UnpublishedCount(ctx context.Context) (int, error)
}
