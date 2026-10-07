// Package postgres implements the review repository with sqlc-generated queries.
package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/platform/db"
	"github.com/NaheedRayan/goat-architecture/internal/review/adapters/postgres/sqlcgen"
	"github.com/NaheedRayan/goat-architecture/internal/review/domain"
)

type Repo struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) q(ctx context.Context) *sqlcgen.Queries { return sqlcgen.New(db.Q(ctx, r.pool)) }

func toReview(x sqlcgen.ReviewReview) domain.Review {
	return domain.Review{ID: x.ID, ProductID: x.ProductID, UserID: x.UserID, Rating: int(x.Rating), Title: x.Title, Body: x.Body,
		Author: x.Author, Status: x.Status, CreatedAt: x.CreatedAt, UpdatedAt: x.UpdatedAt}
}

func many(rows []sqlcgen.ReviewReview, err error) ([]domain.Review, error) {
	if err != nil {
		return nil, err
	}
	out := make([]domain.Review, len(rows))
	for i, x := range rows {
		out[i] = toReview(x)
	}
	return out, nil
}

func (r *Repo) Upsert(ctx context.Context, rv domain.Review) (domain.Review, error) {
	x, err := r.q(ctx).UpsertReview(ctx, sqlcgen.UpsertReviewParams{
		ID: rv.ID, ProductID: rv.ProductID, UserID: rv.UserID, Rating: int16(rv.Rating), Title: rv.Title, Body: rv.Body, Author: rv.Author,
	})
	return toReview(x), err
}

func (r *Repo) ByUser(ctx context.Context, productID, userID uuid.UUID) (domain.Review, error) {
	x, err := r.q(ctx).GetReviewByUser(ctx, sqlcgen.GetReviewByUserParams{ProductID: productID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Review{}, domain.ErrNotFound
	}
	return toReview(x), err
}

func (r *Repo) Published(ctx context.Context, productID uuid.UUID, limit int) ([]domain.Review, error) {
	return many(r.q(ctx).ListPublishedReviews(ctx, sqlcgen.ListPublishedReviewsParams{ProductID: productID, Limit: int32(limit)}))
}

func (r *Repo) Summaries(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]domain.Summary, error) {
	rows, err := r.q(ctx).RatingSummaries(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]domain.Summary, len(rows))
	for _, x := range rows {
		out[x.ProductID] = domain.Summary{Count: int(x.N), Average: x.AvgRating}
	}
	return out, nil
}

func (r *Repo) Distribution(ctx context.Context, productID uuid.UUID) ([6]int, error) {
	var d [6]int
	rows, err := r.q(ctx).RatingDistribution(ctx, productID)
	if err != nil {
		return d, err
	}
	for _, x := range rows {
		if x.Stars >= 1 && x.Stars <= 5 {
			d[x.Stars] = int(x.N)
		}
	}
	return d, nil
}

func (r *Repo) All(ctx context.Context, status string, limit, offset int) ([]domain.Review, error) {
	return many(r.q(ctx).ListAllReviews(ctx, sqlcgen.ListAllReviewsParams{Status: status, PageLimit: int32(limit), PageOffset: int32(offset)}))
}

func (r *Repo) SetStatus(ctx context.Context, id uuid.UUID, status string) error {
	n, err := r.q(ctx).SetReviewStatus(ctx, sqlcgen.SetReviewStatusParams{ID: id, Status: status})
	if err == nil && n == 0 {
		return domain.ErrNotFound
	}
	return err
}

func (r *Repo) Delete(ctx context.Context, id uuid.UUID) error {
	n, err := r.q(ctx).DeleteReview(ctx, id)
	if err == nil && n == 0 {
		return domain.ErrNotFound
	}
	return err
}

func (r *Repo) DeleteByUser(ctx context.Context, userID uuid.UUID) (int64, error) {
	return r.q(ctx).DeleteUserReviews(ctx, userID)
}

func (r *Repo) ListByUser(ctx context.Context, userID uuid.UUID) ([]domain.Review, error) {
	return many(r.q(ctx).ListUserReviews(ctx, userID))
}
