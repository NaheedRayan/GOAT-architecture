-- +goose Up
CREATE SCHEMA inventory;

-- Stock is held in lots (shipments/bins). Reservations lock candidate lots with
-- FOR UPDATE SKIP LOCKED so concurrent checkouts never block or oversell.
CREATE TABLE inventory.stock_lots (
    id         uuid PRIMARY KEY,
    product_id uuid        NOT NULL,
    label      text        NOT NULL DEFAULT '',
    quantity   int         NOT NULL CHECK (quantity >= 0),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX stock_lots_product_idx ON inventory.stock_lots (product_id) WHERE quantity > 0;

CREATE TABLE inventory.reservations (
    id         uuid PRIMARY KEY,
    order_id   uuid        NOT NULL,
    product_id uuid        NOT NULL,
    lot_id     uuid        NOT NULL REFERENCES inventory.stock_lots(id),
    quantity   int         NOT NULL CHECK (quantity > 0),
    status     text        NOT NULL DEFAULT 'reserved' CHECK (status IN ('reserved','committed','released')),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX reservations_order_idx ON inventory.reservations (order_id);
CREATE INDEX reservations_expiry_idx ON inventory.reservations (expires_at) WHERE status = 'reserved';

-- +goose Down
DROP SCHEMA inventory CASCADE;
