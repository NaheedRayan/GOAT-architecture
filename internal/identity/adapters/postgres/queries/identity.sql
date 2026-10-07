-- name: CreateUser :exec
INSERT INTO identity.users (id, email, password_hash, name, role, guest, email_verified_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: GetUserByEmail :one
SELECT * FROM identity.users WHERE lower(email) = lower($1);

-- name: GetUserByID :one
SELECT * FROM identity.users WHERE id = $1;

-- name: InsertRefreshToken :exec
INSERT INTO identity.refresh_tokens (id, user_id, token_hash, expires_at)
VALUES ($1, $2, $3, $4);

-- name: GetRefreshTokenByHash :one
-- Ages are computed here, with the database's own clock, so the reuse-detection grace window
-- never depends on the app and the database agreeing about the time.
SELECT id, user_id, token_hash, expires_at, revoked_at, created_at, rotated_at,
       (expires_at <= now())::boolean AS expired,
       COALESCE(EXTRACT(EPOCH FROM (now() - rotated_at)), -1)::float8 AS rotated_age_secs -- -1: never rotated
FROM identity.refresh_tokens WHERE token_hash = $1;

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

-- name: MarkEmailVerified :exec
UPDATE identity.users SET email_verified_at = COALESCE(email_verified_at, now()) WHERE id = $1;

-- name: SetPassword :exec
-- Setting a password also proves control of the mailbox it was emailed to (reset) and ends guest status.
UPDATE identity.users SET password_hash = $2, guest = false WHERE id = $1;

-- name: UpdateUserName :exec
UPDATE identity.users SET name = $2 WHERE id = $1;

-- name: SetUserDisabled :exec
UPDATE identity.users SET disabled_at = $2 WHERE id = $1;

-- name: SetUserRole :exec
UPDATE identity.users SET role = $2 WHERE id = $1;

-- name: CountActiveAdmins :one
SELECT count(*)::bigint FROM identity.users WHERE role = 'admin' AND disabled_at IS NULL;

-- name: ListUsers :many
SELECT * FROM identity.users
WHERE (sqlc.arg(query)::text = '' OR email ILIKE sqlc.arg(like_pattern)::text OR name ILIKE sqlc.arg(like_pattern)::text)
  AND (sqlc.arg(role)::text = '' OR role = sqlc.arg(role)::text)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: DeleteUser :exec
DELETE FROM identity.users WHERE id = $1;

-- name: InsertActionToken :exec
INSERT INTO identity.action_tokens (id, user_id, kind, token_hash, expires_at) VALUES ($1, $2, $3, $4, $5);

-- name: ConsumeActionToken :one
UPDATE identity.action_tokens SET used_at = now()
WHERE token_hash = $1 AND kind = $2 AND used_at IS NULL AND expires_at > now()
RETURNING *;

-- name: InvalidateActionTokens :exec
UPDATE identity.action_tokens SET used_at = now() WHERE user_id = $1 AND kind = $2 AND used_at IS NULL;

-- name: PurgeActionTokens :execrows
DELETE FROM identity.action_tokens WHERE expires_at < $1 OR (used_at IS NOT NULL AND used_at < $1);

-- name: SetCartReminders :exec
UPDATE identity.users SET cart_reminders = $2 WHERE id = $1;
