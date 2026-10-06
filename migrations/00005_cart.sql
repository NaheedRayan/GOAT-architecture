-- +goose Up
CREATE SCHEMA cart;

-- owner is "user:<uuid>" or "guest:<uuid>" so guest and signed-in carts share one table.
CREATE TABLE cart.carts (
    id         uuid PRIMARY KEY,
    owner      text        NOT NULL UNIQUE,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE cart.cart_items (
    cart_id    uuid NOT NULL REFERENCES cart.carts(id) ON DELETE CASCADE,
    product_id uuid NOT NULL,
    quantity   int  NOT NULL CHECK (quantity > 0),
    PRIMARY KEY (cart_id, product_id)
);

-- +goose Down
DROP SCHEMA cart CASCADE;
