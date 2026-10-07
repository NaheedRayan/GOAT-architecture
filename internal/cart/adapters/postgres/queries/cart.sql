-- name: UpsertCart :one
INSERT INTO cart.carts (id, owner) VALUES ($1, $2)
ON CONFLICT (owner) DO UPDATE SET updated_at = now()
RETURNING id;

-- name: GetCartID :one
SELECT id FROM cart.carts WHERE owner = $1;

-- name: ListCartItems :many
SELECT variant_id, quantity FROM cart.cart_items WHERE cart_id = $1 ORDER BY variant_id;

-- name: AddCartItem :exec
INSERT INTO cart.cart_items (cart_id, variant_id, quantity) VALUES ($1, $2, $3)
ON CONFLICT (cart_id, variant_id) DO UPDATE SET quantity = LEAST(20, cart.cart_items.quantity + EXCLUDED.quantity);

-- name: SetCartItem :exec
INSERT INTO cart.cart_items (cart_id, variant_id, quantity) VALUES ($1, $2, $3)
ON CONFLICT (cart_id, variant_id) DO UPDATE SET quantity = EXCLUDED.quantity;

-- name: DeleteCartItem :exec
DELETE FROM cart.cart_items WHERE cart_id = $1 AND variant_id = $2;

-- name: ClearCart :exec
DELETE FROM cart.cart_items WHERE cart_id = $1;

-- name: DeleteCartByOwner :exec
DELETE FROM cart.carts WHERE owner = $1;

-- name: CountCartItems :one
SELECT (COALESCE(sum(ci.quantity), 0))::bigint
FROM cart.cart_items ci JOIN cart.carts c ON c.id = ci.cart_id
WHERE c.owner = $1;

-- name: LockCartByOwner :one
-- Serialises concurrent merges of the same guest cart.
SELECT id FROM cart.carts WHERE owner = $1 FOR UPDATE;

-- name: MergeCartItems :exec
INSERT INTO cart.cart_items (cart_id, variant_id, quantity)
SELECT sqlc.arg(to_cart)::uuid, variant_id, quantity FROM cart.cart_items WHERE cart_id = sqlc.arg(from_cart)::uuid
ON CONFLICT (cart_id, variant_id) DO UPDATE SET quantity = LEAST(20, cart.cart_items.quantity + EXCLUDED.quantity);

-- name: PurgeGuestCarts :execrows
DELETE FROM cart.carts WHERE owner LIKE 'guest:%' AND updated_at < $1;

-- name: ClaimAbandonedCarts :many
-- Signed-in users' carts that went quiet: idle for a while, not too old, with items, and not yet
-- reminded since they last changed. SKIP LOCKED lets several instances share the work.
SELECT c.id, c.owner, c.updated_at,
       (SELECT COALESCE(sum(quantity), 0) FROM cart.cart_items i WHERE i.cart_id = c.id)::bigint AS items
FROM cart.carts c
WHERE c.owner LIKE 'user:%'
  AND c.updated_at < sqlc.arg(idle_before)
  AND c.updated_at > sqlc.arg(not_older_than)
  AND (c.reminded_at IS NULL OR c.reminded_at < c.updated_at)
  AND EXISTS (SELECT 1 FROM cart.cart_items i WHERE i.cart_id = c.id)
ORDER BY c.updated_at
LIMIT sqlc.arg(max_rows)
FOR UPDATE OF c SKIP LOCKED;

-- name: MarkCartReminded :exec
UPDATE cart.carts SET reminded_at = now() WHERE id = $1;
