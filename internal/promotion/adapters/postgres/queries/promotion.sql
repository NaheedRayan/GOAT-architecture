-- name: GetCouponByCode :one
SELECT * FROM promotion.coupons WHERE lower(code) = lower($1);

-- name: GetCoupon :one
SELECT * FROM promotion.coupons WHERE id = $1;

-- name: LockCoupon :one
-- Serialises concurrent redemptions so a usage limit can never be exceeded.
SELECT * FROM promotion.coupons WHERE id = $1 FOR UPDATE;

-- name: CountRedemptions :one
SELECT count(*)::bigint FROM promotion.redemptions WHERE coupon_id = $1;

-- name: CountUserRedemptions :one
SELECT count(*)::bigint FROM promotion.redemptions WHERE coupon_id = $1 AND user_id = $2;

-- name: InsertRedemption :exec
INSERT INTO promotion.redemptions (id, coupon_id, order_id, user_id, discount_cents) VALUES ($1, $2, $3, $4, $5);

-- name: DeleteRedemptionByOrder :execrows
DELETE FROM promotion.redemptions WHERE order_id = $1;

-- name: ListCoupons :many
SELECT c.*, (SELECT count(*) FROM promotion.redemptions r WHERE r.coupon_id = c.id)::bigint AS uses
FROM promotion.coupons c ORDER BY c.created_at DESC, c.id DESC;

-- name: InsertCoupon :exec
INSERT INTO promotion.coupons (id, code, kind, value, min_subtotal_cents, starts_at, ends_at, max_uses, max_uses_per_user, active)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: UpdateCoupon :execrows
-- The code and kind are fixed once created: customers hold the code and reports refer to it.
UPDATE promotion.coupons
SET value = $2, min_subtotal_cents = $3, starts_at = $4, ends_at = $5, max_uses = $6, max_uses_per_user = $7, active = $8
WHERE id = $1;

-- name: DeleteCoupon :execrows
DELETE FROM promotion.coupons c WHERE c.id = $1 AND NOT EXISTS (SELECT 1 FROM promotion.redemptions r WHERE r.coupon_id = c.id);
