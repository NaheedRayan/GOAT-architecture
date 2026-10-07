-- +goose Up
CREATE SCHEMA alerts;
-- Remembers when each recurring alert was last sent, so a restart or a second instance does not repeat it.
CREATE TABLE alerts.sent (
    key     text PRIMARY KEY,
    sent_at timestamptz NOT NULL
);

-- A cart is reminded about once per state: reminded_at is compared with updated_at.
ALTER TABLE cart.carts ADD COLUMN reminded_at timestamptz;

-- Customers can opt out of "you left something in your cart" emails.
ALTER TABLE identity.users ADD COLUMN cart_reminders boolean NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE identity.users DROP COLUMN cart_reminders;
ALTER TABLE cart.carts DROP COLUMN reminded_at;
DROP SCHEMA alerts CASCADE;
