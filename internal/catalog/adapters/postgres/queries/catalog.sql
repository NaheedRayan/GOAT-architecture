-- name: ListProducts :many
SELECT p.id, p.category_id, p.slug, p.name, p.description, p.price_cents, p.currency,
       p.image_url, p.thumb_url, p.active, p.created_at, p.archived_at, p.option_name,
       COALESCE(vp.min_p, p.price_cents)::bigint AS min_price,
       COALESCE(vp.max_p, p.price_cents)::bigint AS max_price,
       (count(*) OVER ())::bigint AS total
FROM catalog.products p
LEFT JOIN catalog.categories c ON c.id = p.category_id
LEFT JOIN LATERAL (
    SELECT min(COALESCE(v.price_cents, p.price_cents)) AS min_p, max(COALESCE(v.price_cents, p.price_cents)) AS max_p
    FROM catalog.variants v WHERE v.product_id = p.id AND v.active
) vp ON true
WHERE (p.archived_at IS NOT NULL) = sqlc.arg(archived)::bool
  AND (sqlc.arg(include_inactive)::bool OR p.active)
  AND (sqlc.arg(query)::text = ''
       OR to_tsvector('english', p.name || ' ' || p.description) @@ websearch_to_tsquery('english', sqlc.arg(query)::text)
       OR p.name ILIKE sqlc.arg(like_pattern)::text)
  AND (sqlc.arg(category_slug)::text = '' OR c.slug = sqlc.arg(category_slug)::text)
  AND (sqlc.arg(min_price)::bigint <= 0 OR COALESCE(vp.min_p, p.price_cents) >= sqlc.arg(min_price)::bigint)
  AND (sqlc.arg(max_price)::bigint <= 0 OR COALESCE(vp.min_p, p.price_cents) <= sqlc.arg(max_price)::bigint)
ORDER BY
  CASE WHEN sqlc.arg(sort)::text = 'price_asc' THEN COALESCE(vp.min_p, p.price_cents) END ASC NULLS LAST,
  CASE WHEN sqlc.arg(sort)::text = 'price_desc' THEN COALESCE(vp.min_p, p.price_cents) END DESC NULLS LAST,
  CASE WHEN sqlc.arg(sort)::text = 'name' THEN lower(p.name) END ASC NULLS LAST,
  CASE WHEN sqlc.arg(sort)::text = '' AND sqlc.arg(query)::text <> ''
       THEN ts_rank(to_tsvector('english', p.name || ' ' || p.description), websearch_to_tsquery('english', sqlc.arg(query)::text))
  END DESC NULLS LAST,
  p.created_at DESC, p.id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: RelatedProducts :many
-- Other live products from the same category, newest first.
SELECT p.* FROM catalog.products p
WHERE p.id <> sqlc.arg(product_id)::uuid AND p.active AND p.archived_at IS NULL
  AND p.category_id IS NOT NULL
  AND p.category_id = (SELECT category_id FROM catalog.products WHERE id = sqlc.arg(product_id)::uuid)
ORDER BY p.created_at DESC, p.id DESC
LIMIT sqlc.arg(max_rows);

-- name: SitemapProducts :many
SELECT slug, created_at FROM catalog.products WHERE active AND archived_at IS NULL ORDER BY created_at DESC, id LIMIT 50000;

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

-- name: ListImagesByProduct :many
SELECT * FROM catalog.product_images WHERE product_id = $1 ORDER BY position, created_at;

-- name: ListImagesByProducts :many
SELECT * FROM catalog.product_images WHERE product_id = ANY(sqlc.arg(ids)::uuid[]) ORDER BY product_id, position, created_at;

-- name: GetImage :one
SELECT * FROM catalog.product_images WHERE id = $1 AND product_id = $2;

-- name: InsertImage :exec
INSERT INTO catalog.product_images (id, product_id, url, thumb_url, alt, position) VALUES ($1, $2, $3, $4, $5, $6);

-- name: DeleteImage :exec
DELETE FROM catalog.product_images WHERE id = $1;

-- name: SetImagePosition :exec
UPDATE catalog.product_images SET position = $2 WHERE id = $1;

-- name: CountImageReferences :one
SELECT count(*)::bigint FROM catalog.product_images WHERE url = $1 OR thumb_url = $1;

-- name: SetProductCover :exec
UPDATE catalog.products SET image_url = $2, thumb_url = $3 WHERE id = $1;
