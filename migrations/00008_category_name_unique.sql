-- +goose Up
-- Names must be unique case-insensitively, not just their slugs ("Shoes" vs "shoes", or a rename onto an existing name).
CREATE UNIQUE INDEX categories_name_key ON catalog.categories (lower(name));

-- +goose Down
DROP INDEX catalog.categories_name_key;
