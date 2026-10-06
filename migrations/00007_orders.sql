-- +goose Up
CREATE SCHEMA orders;

CREATE TABLE orders.orders (
    id              uuid PRIMARY KEY,
    user_id         uuid        NOT NULL,
    status          text        NOT NULL CHECK (status IN ('awaiting_payment','paid','fulfilling','shipped','cancelled')),
    total_cents     bigint      NOT NULL CHECK (total_cents >= 0),
    currency        text        NOT NULL,
    idempotency_key text        NOT NULL,
    shipping        jsonb       NOT NULL,
    payment_id      uuid,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, idempotency_key)
);
CREATE INDEX orders_user_idx ON orders.orders (user_id, created_at DESC);
CREATE INDEX orders_status_idx ON orders.orders (status, created_at);

CREATE TABLE orders.order_items (
    order_id         uuid   NOT NULL REFERENCES orders.orders(id) ON DELETE CASCADE,
    product_id       uuid   NOT NULL,
    name             text   NOT NULL,
    unit_price_cents bigint NOT NULL CHECK (unit_price_cents >= 0),
    quantity         int    NOT NULL CHECK (quantity > 0),
    PRIMARY KEY (order_id, product_id)
);

-- +goose Down
DROP SCHEMA orders CASCADE;
