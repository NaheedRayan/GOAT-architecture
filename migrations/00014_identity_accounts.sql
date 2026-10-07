-- +goose Up
ALTER TABLE identity.users
    ADD COLUMN email_verified_at timestamptz,
    ADD COLUMN guest             boolean NOT NULL DEFAULT false, -- created at checkout; has no password until the owner sets one
    ADD COLUMN disabled_at       timestamptz;                    -- set by an admin; blocks sign-in

UPDATE identity.users SET email_verified_at = created_at WHERE role = 'admin';

ALTER TABLE identity.users DROP CONSTRAINT users_role_check;
ALTER TABLE identity.users ADD CONSTRAINT users_role_check CHECK (role IN ('customer', 'staff', 'admin'));

-- One-time, expiring tokens for emailed links (password reset, email verification).
CREATE TABLE identity.action_tokens (
    id         uuid PRIMARY KEY,
    user_id    uuid        NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
    kind       text        NOT NULL CHECK (kind IN ('password_reset', 'verify_email')),
    token_hash bytea       NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX action_tokens_user_idx ON identity.action_tokens (user_id, kind);

-- +goose Down
DROP TABLE identity.action_tokens;
ALTER TABLE identity.users DROP CONSTRAINT users_role_check;
ALTER TABLE identity.users ADD CONSTRAINT users_role_check CHECK (role IN ('customer', 'admin'));
ALTER TABLE identity.users DROP COLUMN email_verified_at, DROP COLUMN guest, DROP COLUMN disabled_at;
