-- name: ListProducts :many
SELECT p.id, p.category_id, p.slug, p.name, p.description, p.price_cents, p.currency,
       p.image_url, p.active, p.created_at, p.archived_at, p.option_name,
       COALESCE((SELECT min(COALESCE(v.price_cents, p.price_cents)) FROM catalog.variants v WHERE v.product_id = p.id AND v.active), p.price_cents)::bigint AS min_price,
       COALESCE((SELECT max(COALESCE(v.price_cents, p.price_cents)) FROM catalog.variants v WHERE v.product_id = p.id AND v.active), p.price_cents)::bigint AS max_price,
       (count(*) OVER ())::bigint AS total
FROM catalog.products p
LEFT JOIN catalog.categories c ON c.id = p.category_id
WHERE (p.archived_at IS NOT NULL) = sqlc.arg(archived)::bool
  AND (sqlc.arg(include_inactive)::bool OR p.active)
  AND (sqlc.arg(query)::text = ''
       OR to_tsvector('english', p.name || ' ' || p.description) @@ websearch_to_tsquery('english', sqlc.arg(query)::text)
       OR p.name ILIKE sqlc.arg(like_pattern)::text)
  AND (sqlc.arg(category_slug)::text = '' OR c.slug = sqlc.arg(category_slug)::text)
ORDER BY
  CASE WHEN sqlc.arg(query)::text <> ''
       THEN ts_rank(to_tsvector('english', p.name || ' ' || p.description), websearch_to_tsquery('english', sqlc.arg(query)::text))
  END DESC NULLS LAST,
  p.created_at DESC, p.id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: GetProductBySlug :one
SELECT * FROM catalog.products WHERE slug = $1 AND archived_at IS NULL;

-- name: GetProductByID :one
SELECT * FROM catalog.products WHERE id = $1;

-- name: GetProductsByIDs :many
SELECT * FROM catalog.products WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: InsertProduct :exec
INSERT INTO catalog.products (id, category_id, slug, name, description, price_cents, currency, image_url, active, option_name)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: UpdateProduct :execrows
UPDATE catalog.products
SET category_id = $2, slug = $3, name = $4, description = $5, price_cents = $6, image_url = $7, active = $8, option_name = $9
WHERE id = $1 AND archived_at IS NULL;

-- name: ListCategories :many
SELECT * FROM catalog.categories ORDER BY name;

-- name: InsertCategory :exec
INSERT INTO catalog.categories (id, slug, name) VALUES ($1, $2, $3);

-- name: ListActiveCategories :many
-- Only categories a shopper can actually find something in.
SELECT c.* FROM catalog.categories c
WHERE EXISTS (SELECT 1 FROM catalog.products p WHERE p.category_id = c.id AND p.active AND p.archived_at IS NULL)
ORDER BY c.name;

-- name: ListCategoryStats :many
SELECT c.id, c.slug, c.name, count(p.id)::bigint AS product_count
FROM catalog.categories c
LEFT JOIN catalog.products p ON p.category_id = c.id AND p.archived_at IS NULL
GROUP BY c.id
ORDER BY c.name;

-- name: RenameCategory :execrows
UPDATE catalog.categories SET name = $2 WHERE id = $1;

-- name: DeleteCategory :execrows
DELETE FROM catalog.categories WHERE id = $1;

-- name: ArchiveProduct :execrows
-- Hides the product everywhere and frees its slug for reuse.
UPDATE catalog.products
SET active = false, archived_at = now(),
    slug = slug || '-archived-' || substr(replace(id::text, '-', ''), 25, 8)
WHERE id = $1 AND archived_at IS NULL;

-- name: RestoreProduct :execrows
-- Brings the product back as hidden; the admin decides when to publish it.
UPDATE catalog.products SET archived_at = NULL WHERE id = $1 AND archived_at IS NOT NULL;

-- name: ListVariantsByProducts :many
SELECT * FROM catalog.variants WHERE product_id = ANY(sqlc.arg(ids)::uuid[]) ORDER BY product_id, position, created_at;

-- name: GetVariant :one
SELECT * FROM catalog.variants WHERE id = $1;

-- name: GetVariantInfos :many
-- A variant joined with its product: what carts, checkout and orders need.
SELECT v.id AS variant_id, v.product_id, v.sku, v.label,
       COALESCE(v.price_cents, p.price_cents)::bigint AS price_cents,
       p.name AS product_name, p.slug AS product_slug, p.image_url, p.currency,
       (COALESCE(v.active AND p.active AND p.archived_at IS NULL, false))::boolean AS active
FROM catalog.variants v
JOIN catalog.products p ON p.id = v.product_id
WHERE v.id = ANY(sqlc.arg(ids)::uuid[]);

-- name: InsertVariant :exec
INSERT INTO catalog.variants (id, product_id, sku, label, price_cents, position, active)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: UpdateVariant :execrows
UPDATE catalog.variants SET sku = $2, label = $3, price_cents = $4, active = $5 WHERE id = $1;

-- name: RenameVariantLabel :exec
UPDATE catalog.variants SET label = $2 WHERE id = $1;
