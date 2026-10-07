// Package postgres implements the content repository with sqlc-generated queries.
package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/content/adapters/postgres/sqlcgen"
	"github.com/NaheedRayan/goat-architecture/internal/content/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/db"
)

type Repo struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) q(ctx context.Context) *sqlcgen.Queries { return sqlcgen.New(db.Q(ctx, r.pool)) }

func toPage(p sqlcgen.ContentPage) domain.Page {
	return domain.Page{ID: p.ID, Slug: p.Slug, Title: p.Title, Body: p.Body, Published: p.Published, Position: int(p.Position), UpdatedAt: p.UpdatedAt}
}

func (r *Repo) List(ctx context.Context, publishedOnly bool) ([]domain.Page, error) {
	var rows []sqlcgen.ContentPage
	var err error
	if publishedOnly {
		rows, err = r.q(ctx).ListPublishedPages(ctx)
	} else {
		rows, err = r.q(ctx).ListPages(ctx)
	}
	if err != nil {
		return nil, err
	}
	out := make([]domain.Page, len(rows))
	for i, p := range rows {
		out[i] = toPage(p)
	}
	return out, nil
}

func (r *Repo) BySlug(ctx context.Context, slug string) (domain.Page, error) {
	p, err := r.q(ctx).GetPageBySlug(ctx, slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Page{}, domain.ErrNotFound
	}
	return toPage(p), err
}

func (r *Repo) Get(ctx context.Context, id uuid.UUID) (domain.Page, error) {
	p, err := r.q(ctx).GetPage(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Page{}, domain.ErrNotFound
	}
	return toPage(p), err
}

func (r *Repo) Insert(ctx context.Context, p domain.Page) error {
	err := r.q(ctx).InsertPage(ctx, sqlcgen.InsertPageParams{ID: p.ID, Slug: p.Slug, Title: p.Title, Body: p.Body, Published: p.Published, Position: int32(p.Position)})
	if db.IsUniqueViolation(err) {
		return domain.ErrSlugTaken
	}
	return err
}

func (r *Repo) Update(ctx context.Context, p domain.Page) error {
	n, err := r.q(ctx).UpdatePage(ctx, sqlcgen.UpdatePageParams{ID: p.ID, Slug: p.Slug, Title: p.Title, Body: p.Body, Published: p.Published})
	if db.IsUniqueViolation(err) {
		return domain.ErrSlugTaken
	}
	if err == nil && n == 0 {
		return domain.ErrNotFound
	}
	return err
}

func (r *Repo) Delete(ctx context.Context, id uuid.UUID) error {
	n, err := r.q(ctx).DeletePage(ctx, id)
	if err == nil && n == 0 {
		return domain.ErrNotFound
	}
	return err
}

func (r *Repo) NextPosition(ctx context.Context) (int, error) {
	n, err := r.q(ctx).NextPagePosition(ctx)
	return int(n), err
}
