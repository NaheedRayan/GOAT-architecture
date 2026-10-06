-- name: InsertPayment :exec
INSERT INTO payment.payments (id, order_id, user_id, amount_cents, currency, provider)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: GetPayment :one
SELECT * FROM payment.payments WHERE id = $1;

-- name: SettlePayment :one
-- Only a pending payment can settle, which makes webhook redelivery idempotent.
UPDATE payment.payments
SET status = sqlc.arg(status)::text,
    provider_ref = CASE WHEN sqlc.arg(provider_ref)::text <> '' THEN sqlc.arg(provider_ref)::text ELSE provider_ref END,
    updated_at = now()
WHERE id = sqlc.arg(id) AND status = 'pending'
RETURNING *;
