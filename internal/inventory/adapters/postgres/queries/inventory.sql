-- name: LockLotsForVariant :many
-- SKIP LOCKED: lots held by another in-flight checkout are skipped, never waited on.
SELECT id, quantity FROM inventory.stock_lots
WHERE variant_id = $1 AND quantity > 0
ORDER BY quantity DESC, id
FOR UPDATE SKIP LOCKED;

-- name: DecrementLot :exec
UPDATE inventory.stock_lots SET quantity = quantity - $2 WHERE id = $1;

-- name: IncrementLot :exec
UPDATE inventory.stock_lots SET quantity = quantity + $2 WHERE id = $1;

-- name: InsertLot :exec
INSERT INTO inventory.stock_lots (id, variant_id, label, quantity) VALUES ($1, $2, $3, $4);

-- name: SetLotQuantity :execrows
UPDATE inventory.stock_lots SET quantity = $3 WHERE id = $1 AND variant_id = $2;

-- name: InsertReservation :exec
INSERT INTO inventory.reservations (id, order_id, variant_id, lot_id, quantity, expires_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListReservedByOrder :many
SELECT id, lot_id, quantity FROM inventory.reservations
WHERE order_id = $1 AND status = 'reserved'
FOR UPDATE;

-- name: SetReservationStatus :exec
UPDATE inventory.reservations SET status = $2 WHERE id = $1;

-- name: CommitOrderReservations :execrows
UPDATE inventory.reservations SET status = 'committed' WHERE order_id = $1 AND status = 'reserved';

-- name: ClaimExpiredReservations :many
SELECT id, order_id, lot_id, quantity FROM inventory.reservations
WHERE status = 'reserved' AND expires_at < now()
ORDER BY expires_at
LIMIT $1
FOR UPDATE SKIP LOCKED;

-- name: PurgeReservations :execrows
-- Finished holds are history, not state; stock itself lives in the lots.
DELETE FROM inventory.reservations WHERE status IN ('committed', 'released') AND created_at < $1;

-- name: AvailableByVariants :many
SELECT variant_id, (sum(quantity))::bigint AS available
FROM inventory.stock_lots
WHERE variant_id = ANY(sqlc.arg(ids)::uuid[])
GROUP BY variant_id;

-- name: ListLotsByVariant :many
SELECT * FROM inventory.stock_lots WHERE variant_id = $1 ORDER BY created_at DESC;

-- name: ListCommittedByOrder :many
SELECT id, variant_id, lot_id, quantity FROM inventory.reservations
WHERE order_id = $1 AND status = 'committed'
FOR UPDATE;

-- name: LowStock :many
-- Variants that have had stock but are at or below the threshold, lowest first.
SELECT variant_id, (sum(quantity))::bigint AS available
FROM inventory.stock_lots
GROUP BY variant_id
HAVING sum(quantity) <= sqlc.arg(threshold)::bigint
ORDER BY available, variant_id
LIMIT sqlc.arg(max_rows);
