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
