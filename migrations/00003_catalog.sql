-- +goose Up
CREATE SCHEMA catalog;

CREATE TABLE catalog.categories (
    id   uuid PRIMARY KEY,
    slug text NOT NULL UNIQUE,
    name text NOT NULL
);

CREATE TABLE catalog.products (
    id          uuid PRIMARY KEY,
    category_id uuid        REFERENCES catalog.categories(id) ON DELETE SET NULL,
    slug        text        NOT NULL UNIQUE,
    name        text        NOT NULL,
    description text        NOT NULL DEFAULT '',
    price_cents bigint      NOT NULL CHECK (price_cents >= 0),
    currency    text        NOT NULL DEFAULT 'USD',
    image_url   text        NOT NULL DEFAULT '',
    active      boolean     NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now()
);
-- Full-text search: queries must use the same expression to hit this index.
CREATE INDEX products_search_idx ON catalog.products
    USING gin (to_tsvector('english', name || ' ' || description));
CREATE INDEX products_category_idx ON catalog.products (category_id);
CREATE INDEX products_created_idx ON catalog.products (created_at DESC, id DESC);

-- +goose Down
DROP SCHEMA catalog CASCADE;
