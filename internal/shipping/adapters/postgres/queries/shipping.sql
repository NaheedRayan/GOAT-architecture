-- name: ListMethods :many
SELECT * FROM shipping.methods ORDER BY position, created_at;

-- name: ListActiveMethods :many
SELECT * FROM shipping.methods WHERE active ORDER BY position, price_cents, created_at;

-- name: GetMethod :one
SELECT * FROM shipping.methods WHERE id = $1;

-- name: InsertMethod :exec
INSERT INTO shipping.methods (id, name, description, price_cents, free_over_cents, min_days, max_days, active, position)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: UpdateMethod :execrows
UPDATE shipping.methods
SET name = $2, description = $3, price_cents = $4, free_over_cents = $5, min_days = $6, max_days = $7, active = $8, position = $9
WHERE id = $1;

-- name: DeleteMethod :execrows
DELETE FROM shipping.methods WHERE id = $1;

-- name: NextMethodPosition :one
SELECT COALESCE(max(position) + 1, 0)::int FROM shipping.methods;
