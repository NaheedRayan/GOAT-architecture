-- name: InsertOrder :exec
INSERT INTO orders.orders (
    id, user_id, status, total_cents, currency, idempotency_key, shipping,
    subtotal_cents, discount_cents, shipping_cents, tax_cents, tax_inclusive,
    shipping_method, coupon_code, payment_method, contact_email
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16);

-- name: InsertOrderItem :exec
INSERT INTO orders.order_items (order_id, variant_id, product_id, name, variant_label, sku, unit_price_cents, quantity)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: GetOrder :one
SELECT * FROM orders.orders WHERE id = $1;

-- name: GetOrderForUpdate :one
SELECT * FROM orders.orders WHERE id = $1 FOR UPDATE;

-- name: GetOrderByIdempotencyKey :one
SELECT * FROM orders.orders WHERE user_id = $1 AND idempotency_key = $2;

-- name: ListOrderItems :many
SELECT * FROM orders.order_items WHERE order_id = $1 ORDER BY name, variant_label;

-- name: ListOrdersByUser :many
SELECT * FROM orders.orders WHERE user_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2;

-- name: SearchOrders :many
SELECT * FROM orders.orders
WHERE (sqlc.arg(status)::text = '' OR status = sqlc.arg(status)::text)
  AND (sqlc.arg(return_status)::text = '' OR return_status = sqlc.arg(return_status)::text)
  AND (sqlc.arg(query)::text = ''
       OR id::text ILIKE sqlc.arg(like_pattern)::text
       OR contact_email ILIKE sqlc.arg(like_pattern)::text
       OR shipping->>'full_name' ILIKE sqlc.arg(like_pattern)::text
       OR shipping->>'phone' ILIKE sqlc.arg(like_pattern)::text
       OR tracking_number ILIKE sqlc.arg(like_pattern)::text)
  AND (sqlc.narg(created_from)::timestamptz IS NULL OR created_at >= sqlc.narg(created_from)::timestamptz)
  AND (sqlc.narg(created_to)::timestamptz IS NULL OR created_at < sqlc.narg(created_to)::timestamptz)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountOrdersByStatus :many
SELECT status, count(*)::bigint AS n FROM orders.orders GROUP BY status;

-- name: CountReturnRequests :one
SELECT count(*)::bigint FROM orders.orders WHERE return_status = 'requested';

-- name: SaveOrder :execrows
-- Persists every field that changes after creation. Callers hold the row lock (GetOrderForUpdate).
UPDATE orders.orders SET
    status = $2, payment_id = $3,
    tracking_carrier = $4, tracking_number = $5, tracking_url = $6,
    shipped_at = $7, delivered_at = $8, cancelled_at = $9, refunded_at = $10, refund_cents = $11,
    admin_note = $12, return_status = $13, return_reason = $14, return_note = $15, return_requested_at = $16,
    updated_at = now()
WHERE id = $1;

-- name: InsertOrderEvent :exec
INSERT INTO orders.order_events (id, order_id, kind, detail, actor_id) VALUES ($1, $2, $3, $4, $5);

-- name: ListOrderEvents :many
SELECT * FROM orders.order_events WHERE order_id = $1 ORDER BY at, id;

-- name: ClaimPaidOrders :many
-- Fulfillment workers each take a disjoint batch; SKIP LOCKED avoids contention.
SELECT id FROM orders.orders WHERE status = 'paid' ORDER BY created_at LIMIT $1 FOR UPDATE SKIP LOCKED;

-- name: AnonymizeUserOrders :execrows
-- Account deletion: keep the financial record, drop everything that identifies the person.
UPDATE orders.orders
SET shipping = jsonb_build_object('full_name', 'Deleted customer', 'phone', '', 'line1', '', 'line2', '',
                                  'city', '', 'postal_code', '', 'country', COALESCE(shipping->>'country', '')),
    contact_email = '', admin_note = '', return_reason = ''
WHERE user_id = $1;

-- name: SalesTotals :one
-- Orders that count as sales: confirmed and beyond, not cancelled or refunded.
SELECT count(*)::bigint AS orders, COALESCE(sum(total_cents), 0)::bigint AS revenue_cents
FROM orders.orders
WHERE created_at >= sqlc.arg(from_at) AND created_at < sqlc.arg(to_at)
  AND status IN ('paid', 'fulfilling', 'shipped', 'delivered');

-- name: RefundTotals :one
SELECT count(*)::bigint AS orders, COALESCE(sum(refund_cents), 0)::bigint AS refunded_cents
FROM orders.orders
WHERE refund_cents > 0 AND COALESCE(refunded_at, cancelled_at) >= sqlc.arg(from_at) AND COALESCE(refunded_at, cancelled_at) < sqlc.arg(to_at);

-- name: SalesByDay :many
SELECT (created_at AT TIME ZONE 'UTC')::date AS day, count(*)::bigint AS orders, COALESCE(sum(total_cents), 0)::bigint AS revenue_cents
FROM orders.orders
WHERE created_at >= sqlc.arg(from_at) AND created_at < sqlc.arg(to_at)
  AND status IN ('paid', 'fulfilling', 'shipped', 'delivered')
GROUP BY 1 ORDER BY 1;

-- name: TopProducts :many
SELECT i.product_id, max(i.name)::text AS name, sum(i.quantity)::bigint AS units, sum(i.unit_price_cents * i.quantity)::bigint AS revenue_cents
FROM orders.order_items i
JOIN orders.orders o ON o.id = i.order_id
WHERE o.created_at >= sqlc.arg(from_at) AND o.created_at < sqlc.arg(to_at)
  AND o.status IN ('paid', 'fulfilling', 'shipped', 'delivered')
GROUP BY i.product_id
ORDER BY revenue_cents DESC, units DESC
LIMIT sqlc.arg(top_limit);

-- name: ExportOrders :many
SELECT * FROM orders.orders
WHERE (sqlc.arg(status)::text = '' OR status = sqlc.arg(status)::text)
  AND (sqlc.narg(created_from)::timestamptz IS NULL OR created_at >= sqlc.narg(created_from)::timestamptz)
  AND (sqlc.narg(created_to)::timestamptz IS NULL OR created_at < sqlc.narg(created_to)::timestamptz)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(max_rows);

-- name: HasPurchased :one
-- Has this user received (or been sent) this product? Only then may they review it.
SELECT EXISTS (
    SELECT 1 FROM orders.order_items i JOIN orders.orders o ON o.id = i.order_id
    WHERE o.user_id = $1 AND i.product_id = $2 AND o.status IN ('shipped', 'delivered')
) AS purchased;
