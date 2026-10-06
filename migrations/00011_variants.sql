-- +goose Up
-- Variants (size, colour...). Every product has at least one variant; a
-- product sold in one form has a single default variant with an empty label.
-- Cart lines, stock and order lines are keyed by VARIANT from now on.
CREATE TABLE catalog.variants (
    id          uuid PRIMARY KEY,
    product_id  uuid        NOT NULL REFERENCES catalog.products(id) ON DELETE CASCADE,
    sku         text        NOT NULL DEFAULT '',
    label       text        NOT NULL DEFAULT '',
    price_cents bigint      CHECK (price_cents IS NULL OR price_cents >= 0), -- NULL = use the product price
    position    int         NOT NULL DEFAULT 0,
    active      boolean     NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX variants_product_idx ON catalog.variants (product_id, position);
CREATE UNIQUE INDEX variants_sku_key ON catalog.variants (lower(sku)) WHERE sku <> '';
CREATE UNIQUE INDEX variants_label_key ON catalog.variants (product_id, lower(label));

ALTER TABLE catalog.products ADD COLUMN option_name text NOT NULL DEFAULT '';

-- Existing rows elsewhere reference product ids; reusing the product id as the
-- default variant id keeps all of them valid without rewriting any data.
INSERT INTO catalog.variants (id, product_id, label, position) SELECT id, id, '', 0 FROM catalog.products;

ALTER TABLE cart.cart_items RENAME COLUMN product_id TO variant_id;

ALTER TABLE inventory.stock_lots RENAME COLUMN product_id TO variant_id;
ALTER TABLE inventory.reservations RENAME COLUMN product_id TO variant_id;
ALTER INDEX inventory.stock_lots_product_idx RENAME TO stock_lots_variant_idx;

ALTER TABLE orders.order_items
    ADD COLUMN variant_id    uuid,
    ADD COLUMN variant_label text NOT NULL DEFAULT '',
    ADD COLUMN sku           text NOT NULL DEFAULT '';
UPDATE orders.order_items SET variant_id = product_id;
ALTER TABLE orders.order_items ALTER COLUMN variant_id SET NOT NULL;
ALTER TABLE orders.order_items DROP CONSTRAINT order_items_pkey;
ALTER TABLE orders.order_items ADD PRIMARY KEY (order_id, variant_id);

-- +goose Down
ALTER TABLE orders.order_items DROP CONSTRAINT order_items_pkey;
ALTER TABLE orders.order_items ADD PRIMARY KEY (order_id, product_id);
ALTER TABLE orders.order_items DROP COLUMN variant_id, DROP COLUMN variant_label, DROP COLUMN sku;
ALTER INDEX inventory.stock_lots_variant_idx RENAME TO stock_lots_product_idx;
ALTER TABLE inventory.reservations RENAME COLUMN variant_id TO product_id;
ALTER TABLE inventory.stock_lots RENAME COLUMN variant_id TO product_id;
ALTER TABLE cart.cart_items RENAME COLUMN variant_id TO product_id;
ALTER TABLE catalog.products DROP COLUMN option_name;
DROP TABLE catalog.variants;
