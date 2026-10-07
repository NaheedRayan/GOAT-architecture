-- name: ListPages :many
SELECT * FROM content.pages ORDER BY position, title;

-- name: ListPublishedPages :many
SELECT * FROM content.pages WHERE published ORDER BY position, title;

-- name: GetPageBySlug :one
SELECT * FROM content.pages WHERE slug = $1;

-- name: GetPage :one
SELECT * FROM content.pages WHERE id = $1;

-- name: InsertPage :exec
INSERT INTO content.pages (id, slug, title, body, published, position) VALUES ($1, $2, $3, $4, $5, $6);

-- name: UpdatePage :execrows
UPDATE content.pages SET slug = $2, title = $3, body = $4, published = $5, updated_at = now() WHERE id = $1;

-- name: DeletePage :execrows
DELETE FROM content.pages WHERE id = $1;

-- name: NextPagePosition :one
SELECT COALESCE(max(position) + 1, 0)::int FROM content.pages;
