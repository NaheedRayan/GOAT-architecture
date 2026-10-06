-- +goose Up
CREATE SCHEMA payment;

CREATE TABLE payment.payments (
    id           uuid PRIMARY KEY,
    order_id     uuid        NOT NULL UNIQUE,
    user_id      uuid        NOT NULL,
    amount_cents bigint      NOT NULL CHECK (amount_cents >= 0),
    currency     text        NOT NULL,
    provider     text        NOT NULL,
    provider_ref text        NOT NULL DEFAULT '',
    status       text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','succeeded','failed')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP SCHEMA payment CASCADE;
