-- name: AddWish :exec
INSERT INTO wishlist.items (user_id, product_id) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: RemoveWish :execrows
DELETE FROM wishlist.items WHERE user_id = $1 AND product_id = $2;

-- name: HasWish :one
SELECT EXISTS (SELECT 1 FROM wishlist.items WHERE user_id = $1 AND product_id = $2) AS wished;

-- name: ListWishes :many
SELECT product_id FROM wishlist.items WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2;

-- name: CountWishes :one
SELECT count(*)::bigint FROM wishlist.items WHERE user_id = $1;

-- name: DeleteUserWishes :execrows
DELETE FROM wishlist.items WHERE user_id = $1;
