-- +goose Up
CREATE SCHEMA identity;

CREATE TABLE identity.users (
    id            uuid PRIMARY KEY,
    email         text        NOT NULL,
    password_hash text        NOT NULL,
    name          text        NOT NULL,
    role          text        NOT NULL DEFAULT 'customer' CHECK (role IN ('customer','admin')),
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_email_key ON identity.users (lower(email));

CREATE TABLE identity.refresh_tokens (
    id         uuid PRIMARY KEY,
    user_id    uuid        NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
    token_hash bytea       NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX refresh_tokens_user_idx ON identity.refresh_tokens (user_id);

CREATE TABLE identity.addresses (
    id          uuid PRIMARY KEY,
    user_id     uuid        NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
    full_name   text        NOT NULL,
    phone       text        NOT NULL,
    line1       text        NOT NULL,
    line2       text        NOT NULL DEFAULT '',
    city        text        NOT NULL,
    postal_code text        NOT NULL,
    country     text        NOT NULL,
    is_default  boolean     NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX addresses_user_idx ON identity.addresses (user_id);

-- +goose Down
DROP SCHEMA identity CASCADE;
