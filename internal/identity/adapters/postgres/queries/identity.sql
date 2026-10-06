-- name: CreateUser :exec
INSERT INTO identity.users (id, email, password_hash, name, role)
VALUES ($1, $2, $3, $4, $5);

-- name: GetUserByEmail :one
SELECT * FROM identity.users WHERE lower(email) = lower($1);

-- name: GetUserByID :one
SELECT * FROM identity.users WHERE id = $1;

-- name: InsertRefreshToken :exec
INSERT INTO identity.refresh_tokens (id, user_id, token_hash, expires_at)
VALUES ($1, $2, $3, $4);

-- name: GetRefreshTokenByHash :one
SELECT * FROM identity.refresh_tokens WHERE token_hash = $1;

-- name: ConsumeRefreshToken :one
UPDATE identity.refresh_tokens SET revoked_at = now(), rotated_at = now()
WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()
RETURNING *;

-- name: RevokeRefreshToken :exec
UPDATE identity.refresh_tokens SET revoked_at = now()
WHERE token_hash = $1 AND revoked_at IS NULL;

-- name: RevokeAllRefreshTokens :exec
UPDATE identity.refresh_tokens SET revoked_at = now()
WHERE user_id = $1 AND revoked_at IS NULL;

-- name: PurgeRefreshTokens :execrows
-- Expired or revoked tokens have no further use; rotated ones are kept a week for replay (theft) detection.
DELETE FROM identity.refresh_tokens
WHERE expires_at < $1 OR (revoked_at IS NOT NULL AND revoked_at < $1);

-- name: ListAddresses :many
SELECT * FROM identity.addresses WHERE user_id = $1 ORDER BY is_default DESC, created_at;

-- name: InsertAddress :exec
INSERT INTO identity.addresses (id, user_id, full_name, phone, line1, line2, city, postal_code, country, is_default)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: ClearDefaultAddress :exec
UPDATE identity.addresses SET is_default = false WHERE user_id = $1;

-- name: DeleteAddress :exec
DELETE FROM identity.addresses WHERE id = $1 AND user_id = $2;
