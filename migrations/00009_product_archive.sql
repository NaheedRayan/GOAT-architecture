-- +goose Up
-- Products are archived rather than deleted: orders keep their snapshots, but
-- the product disappears from the store, carts and the default admin list.
ALTER TABLE catalog.products ADD COLUMN archived_at timestamptz;
CREATE INDEX products_archived_idx ON catalog.products (archived_at) WHERE archived_at IS NOT NULL;

-- +goose Down
DROP INDEX catalog.products_archived_idx;
ALTER TABLE catalog.products DROP COLUMN archived_at;
