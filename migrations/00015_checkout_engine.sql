-- +goose Up

-- ---------- shipping methods (admin-managed) ----------
CREATE SCHEMA shipping;
CREATE TABLE shipping.methods (
    id              uuid PRIMARY KEY,
    name            text        NOT NULL,
    description     text        NOT NULL DEFAULT '',
    price_cents     bigint      NOT NULL CHECK (price_cents >= 0),
    free_over_cents bigint      CHECK (free_over_cents IS NULL OR free_over_cents > 0), -- free when the subtotal reaches this
    min_days        int         NOT NULL DEFAULT 0 CHECK (min_days >= 0),
    max_days        int         NOT NULL DEFAULT 0 CHECK (max_days >= min_days),
    active          boolean     NOT NULL DEFAULT true,
    position        int         NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT now()
);
-- A sensible starting point; the admin edits or replaces it.
INSERT INTO shipping.methods (id, name, description, price_cents, free_over_cents, min_days, max_days, position)
VALUES ('01a00000-0000-7000-8000-000000000001', 'Standard delivery', 'Tracked delivery to your door', 500, 10000, 3, 5, 0);

-- ---------- coupons ----------
CREATE SCHEMA promotion;
CREATE TABLE promotion.coupons (
    id                 uuid PRIMARY KEY,
    code               text        NOT NULL,
    kind               text        NOT NULL CHECK (kind IN ('percent', 'fixed', 'free_shipping')),
    value              bigint      NOT NULL DEFAULT 0 CHECK (value >= 0),   -- percent (1-100) or amount in cents
    min_subtotal_cents bigint      NOT NULL DEFAULT 0 CHECK (min_subtotal_cents >= 0),
    starts_at          timestamptz,
    ends_at            timestamptz,
    max_uses           int         CHECK (max_uses IS NULL OR max_uses > 0),
    max_uses_per_user  int         CHECK (max_uses_per_user IS NULL OR max_uses_per_user > 0),
    active             boolean     NOT NULL DEFAULT true,
    created_at         timestamptz NOT NULL DEFAULT now(),
    CHECK (kind <> 'percent' OR value BETWEEN 1 AND 100),
    CHECK (kind <> 'fixed' OR value > 0)
);
CREATE UNIQUE INDEX coupons_code_key ON promotion.coupons (lower(code));
CREATE TABLE promotion.redemptions (
    id             uuid PRIMARY KEY,
    coupon_id      uuid        NOT NULL REFERENCES promotion.coupons(id),
    order_id       uuid        NOT NULL UNIQUE,
    user_id        uuid        NOT NULL,
    discount_cents bigint      NOT NULL CHECK (discount_cents >= 0),
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX redemptions_coupon_idx ON promotion.redemptions (coupon_id);
CREATE INDEX redemptions_user_idx ON promotion.redemptions (coupon_id, user_id);

-- ---------- payments: method and refunds ----------
ALTER TABLE payment.payments ADD COLUMN method text NOT NULL DEFAULT 'card' CHECK (method IN ('card', 'cod'));
ALTER TABLE payment.payments DROP CONSTRAINT payments_status_check;
ALTER TABLE payment.payments ADD CONSTRAINT payments_status_check CHECK (status IN ('pending', 'succeeded', 'failed', 'refunded'));

-- ---------- inventory: stock coming back ----------
ALTER TABLE inventory.reservations DROP CONSTRAINT reservations_status_check;
ALTER TABLE inventory.reservations ADD CONSTRAINT reservations_status_check CHECK (status IN ('reserved', 'committed', 'released', 'returned'));

-- ---------- orders ----------
ALTER TABLE orders.orders DROP CONSTRAINT orders_status_check;
ALTER TABLE orders.orders ADD CONSTRAINT orders_status_check
    CHECK (status IN ('awaiting_payment', 'paid', 'fulfilling', 'shipped', 'delivered', 'cancelled', 'refunded'));

ALTER TABLE orders.orders
    ADD COLUMN subtotal_cents     bigint NOT NULL DEFAULT 0,
    ADD COLUMN discount_cents     bigint NOT NULL DEFAULT 0,
    ADD COLUMN shipping_cents     bigint NOT NULL DEFAULT 0,
    ADD COLUMN tax_cents          bigint NOT NULL DEFAULT 0,
    ADD COLUMN tax_inclusive      boolean NOT NULL DEFAULT false,
    ADD COLUMN shipping_method    text   NOT NULL DEFAULT '',
    ADD COLUMN coupon_code        text   NOT NULL DEFAULT '',
    ADD COLUMN payment_method     text   NOT NULL DEFAULT 'card',
    ADD COLUMN contact_email      text   NOT NULL DEFAULT '',
    ADD COLUMN tracking_carrier   text   NOT NULL DEFAULT '',
    ADD COLUMN tracking_number    text   NOT NULL DEFAULT '',
    ADD COLUMN tracking_url       text   NOT NULL DEFAULT '',
    ADD COLUMN shipped_at         timestamptz,
    ADD COLUMN delivered_at       timestamptz,
    ADD COLUMN cancelled_at       timestamptz,
    ADD COLUMN refunded_at        timestamptz,
    ADD COLUMN refund_cents       bigint NOT NULL DEFAULT 0,
    ADD COLUMN admin_note         text   NOT NULL DEFAULT '',
    ADD COLUMN return_status      text   NOT NULL DEFAULT '' CHECK (return_status IN ('', 'requested', 'approved', 'rejected')),
    ADD COLUMN return_reason      text   NOT NULL DEFAULT '',
    ADD COLUMN return_note        text   NOT NULL DEFAULT '', -- staff's reply when approving/rejecting
    ADD COLUMN return_requested_at timestamptz;
-- Orders placed before this migration had no breakdown: subtotal equals total.
UPDATE orders.orders SET subtotal_cents = total_cents;
CREATE INDEX orders_email_idx ON orders.orders (lower(contact_email));
CREATE INDEX orders_return_idx ON orders.orders (return_status) WHERE return_status <> '';

-- A timeline of what happened to an order, shown to the customer and used by staff.
CREATE TABLE orders.order_events (
    id       uuid PRIMARY KEY,
    order_id uuid        NOT NULL REFERENCES orders.orders(id) ON DELETE CASCADE,
    at       timestamptz NOT NULL DEFAULT now(),
    kind     text        NOT NULL,
    detail   text        NOT NULL DEFAULT '',
    actor_id uuid
);
CREATE INDEX order_events_order_idx ON orders.order_events (order_id, at);

-- +goose Down
DROP TABLE orders.order_events;
DROP INDEX orders.orders_return_idx;
DROP INDEX orders.orders_email_idx;
ALTER TABLE orders.orders
    DROP COLUMN subtotal_cents, DROP COLUMN discount_cents, DROP COLUMN shipping_cents, DROP COLUMN tax_cents,
    DROP COLUMN tax_inclusive, DROP COLUMN shipping_method, DROP COLUMN coupon_code, DROP COLUMN payment_method,
    DROP COLUMN contact_email, DROP COLUMN tracking_carrier, DROP COLUMN tracking_number, DROP COLUMN tracking_url,
    DROP COLUMN shipped_at, DROP COLUMN delivered_at, DROP COLUMN cancelled_at, DROP COLUMN refunded_at,
    DROP COLUMN refund_cents, DROP COLUMN admin_note, DROP COLUMN return_status, DROP COLUMN return_reason,
    DROP COLUMN return_note, DROP COLUMN return_requested_at;
ALTER TABLE orders.orders DROP CONSTRAINT orders_status_check;
ALTER TABLE orders.orders ADD CONSTRAINT orders_status_check CHECK (status IN ('awaiting_payment','paid','fulfilling','shipped','cancelled'));
ALTER TABLE inventory.reservations DROP CONSTRAINT reservations_status_check;
ALTER TABLE inventory.reservations ADD CONSTRAINT reservations_status_check CHECK (status IN ('reserved','committed','released'));
ALTER TABLE payment.payments DROP CONSTRAINT payments_status_check;
ALTER TABLE payment.payments ADD CONSTRAINT payments_status_check CHECK (status IN ('pending','succeeded','failed'));
ALTER TABLE payment.payments DROP COLUMN method;
DROP SCHEMA promotion CASCADE;
DROP SCHEMA shipping CASCADE;
