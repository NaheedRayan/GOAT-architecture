-- name: UpsertReview :one
-- One review per customer per product: writing again edits it (and re-publishes it unless staff hid it).
INSERT INTO review.reviews (id, product_id, user_id, rating, title, body, author)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (product_id, user_id) DO UPDATE
    SET rating = EXCLUDED.rating, title = EXCLUDED.title, body = EXCLUDED.body, author = EXCLUDED.author, updated_at = now()
RETURNING *;

-- name: GetReviewByUser :one
SELECT * FROM review.reviews WHERE product_id = $1 AND user_id = $2;

-- name: ListPublishedReviews :many
SELECT * FROM review.reviews WHERE product_id = $1 AND status = 'published' ORDER BY created_at DESC, id DESC LIMIT $2;

-- name: RatingSummaries :many
SELECT product_id, count(*)::bigint AS n, avg(rating)::float8 AS avg_rating
FROM review.reviews
WHERE product_id = ANY(sqlc.arg(ids)::uuid[]) AND status = 'published'
GROUP BY product_id;

-- name: RatingDistribution :many
SELECT rating::int AS stars, count(*)::bigint AS n FROM review.reviews WHERE product_id = $1 AND status = 'published' GROUP BY rating;

-- name: ListAllReviews :many
SELECT * FROM review.reviews
WHERE (sqlc.arg(status)::text = '' OR status = sqlc.arg(status)::text)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: SetReviewStatus :execrows
UPDATE review.reviews SET status = $2, updated_at = now() WHERE id = $1;

-- name: DeleteReview :execrows
DELETE FROM review.reviews WHERE id = $1;

-- name: DeleteUserReviews :execrows
DELETE FROM review.reviews WHERE user_id = $1;

-- name: ListUserReviews :many
SELECT * FROM review.reviews WHERE user_id = $1 ORDER BY created_at DESC;
