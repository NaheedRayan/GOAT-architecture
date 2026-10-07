-- +goose Up
CREATE TABLE catalog.product_images (
    id         uuid PRIMARY KEY,
    product_id uuid        NOT NULL REFERENCES catalog.products(id) ON DELETE CASCADE,
    url        text        NOT NULL,
    thumb_url  text        NOT NULL,
    alt        text        NOT NULL DEFAULT '',
    position   int         NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX product_images_product_idx ON catalog.product_images (product_id, position);
CREATE INDEX product_images_url_idx ON catalog.product_images (url);

-- Cover image for cards: the first uploaded image (image_url keeps the full size).
ALTER TABLE catalog.products ADD COLUMN thumb_url text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE catalog.products DROP COLUMN thumb_url;
DROP TABLE catalog.product_images;
