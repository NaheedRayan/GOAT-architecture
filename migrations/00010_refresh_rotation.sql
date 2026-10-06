-- +goose Up
-- Rotation (normal refresh) and revocation (logout, theft response) are different
-- events: only a token rotated seconds ago may be honoured briefly, to survive
-- parallel requests. A logged-out token must never work again.
ALTER TABLE identity.refresh_tokens ADD COLUMN rotated_at timestamptz;

-- +goose Down
ALTER TABLE identity.refresh_tokens DROP COLUMN rotated_at;
