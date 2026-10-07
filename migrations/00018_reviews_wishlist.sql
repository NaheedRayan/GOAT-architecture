-- +goose Up
CREATE SCHEMA review;
CREATE TABLE review.reviews (
    id         uuid PRIMARY KEY,
    product_id uuid        NOT NULL,
    user_id    uuid        NOT NULL,
    rating     smallint    NOT NULL CHECK (rating BETWEEN 1 AND 5),
    title      text        NOT NULL DEFAULT '' CHECK (char_length(title) <= 100),
    body       text        NOT NULL DEFAULT '' CHECK (char_length(body) <= 2000),
    author     text        NOT NULL,
    status     text        NOT NULL DEFAULT 'published' CHECK (status IN ('published', 'hidden')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (product_id, user_id)
);
CREATE INDEX reviews_product_idx ON review.reviews (product_id, created_at DESC) WHERE status = 'published';
CREATE INDEX reviews_created_idx ON review.reviews (created_at DESC);

CREATE SCHEMA wishlist;
CREATE TABLE wishlist.items (
    user_id    uuid        NOT NULL,
    product_id uuid        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, product_id)
);
CREATE INDEX wishlist_user_idx ON wishlist.items (user_id, created_at DESC);

-- +goose Down
DROP SCHEMA wishlist CASCADE;
DROP SCHEMA review CASCADE;
