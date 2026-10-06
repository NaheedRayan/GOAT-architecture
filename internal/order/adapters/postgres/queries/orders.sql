-- name: InsertOrder :exec
INSERT INTO orders.orders (id, user_id, status, total_cents, currency, idempotency_key, shipping)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: InsertOrderItem :exec
INSERT INTO orders.order_items (order_id, product_id, name, unit_price_cents, quantity)
VALUES ($1, $2, $3, $4, $5);

-- name: GetOrder :one
SELECT * FROM orders.orders WHERE id = $1;

-- name: GetOrderForUpdate :one
SELECT * FROM orders.orders WHERE id = $1 FOR UPDATE;

-- name: GetOrderByIdempotencyKey :one
SELECT * FROM orders.orders WHERE user_id = $1 AND idempotency_key = $2;

-- name: ListOrderItems :many
SELECT * FROM orders.order_items WHERE order_id = $1 ORDER BY name;

-- name: ListOrdersByUser :many
SELECT * FROM orders.orders WHERE user_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2;

-- name: ListOrders :many
SELECT * FROM orders.orders
WHERE (sqlc.arg(status)::text = '' OR status = sqlc.arg(status)::text)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: SetOrderStatus :execrows
UPDATE orders.orders SET status = $2, updated_at = now() WHERE id = $1;

-- name: SetOrderPayment :exec
UPDATE orders.orders SET payment_id = $2, updated_at = now() WHERE id = $1;

-- name: ClaimPaidOrders :many
-- Fulfillment workers each take a disjoint batch; SKIP LOCKED avoids contention.
SELECT id FROM orders.orders WHERE status = 'paid' ORDER BY created_at LIMIT $1 FOR UPDATE SKIP LOCKED;

-- name: CountOrdersByStatus :many
SELECT status, count(*)::bigint AS n FROM orders.orders GROUP BY status;
