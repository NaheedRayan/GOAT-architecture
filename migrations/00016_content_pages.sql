-- +goose Up
CREATE SCHEMA content;
CREATE TABLE content.pages (
    id         uuid PRIMARY KEY,
    slug       text        NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,48}$'),
    title      text        NOT NULL,
    body       text        NOT NULL DEFAULT '',
    published  boolean     NOT NULL DEFAULT false,
    position   int         NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Starting-point drafts. They are NOT published and contain [[PLACEHOLDERS]] that must be
-- replaced before the admin can publish them. They are templates, not legal advice:
-- have them reviewed for the countries you sell in.
INSERT INTO content.pages (id, slug, title, body, position) VALUES
('01a00000-0000-7000-8000-0000000000a1', 'terms', 'Terms and conditions', $md$
# Terms and conditions

*Draft template: replace everything in [[double brackets]] and have it reviewed before publishing.*

These terms apply to purchases from [[SHOP NAME]] ("we", "us"), operated by [[LEGAL ENTITY NAME]], [[REGISTERED ADDRESS]].

## Ordering

Placing an order is an offer to buy. We confirm it by email, and the contract is made when we confirm. We may decline an order, for example if an item is unavailable or a price was shown in error; if we do, we will refund anything you paid.

## Prices and payment

Prices are shown in [[CURRENCY]] and [[include / exclude]] tax. Delivery costs are shown at checkout before you pay. You can pay online or, where offered, in cash on delivery.

## Delivery

See our [delivery information](/pages/shipping). Delivery times are estimates, not guarantees.

## Returns and refunds

See our [returns policy](/pages/returns). Nothing in these terms limits rights you have under consumer law.

## Liability

We are responsible for loss that is a foreseeable result of our breaking these terms, and nothing excludes liability that cannot lawfully be excluded. [[ADD ANY FURTHER LIMITS YOUR LAWYER ADVISES]]

## Governing law

These terms are governed by the laws of [[COUNTRY / STATE]]. Disputes are decided by the courts of [[COURTS]].

## Contact

Questions about these terms: [[CONTACT EMAIL]].
$md$, 1),
('01a00000-0000-7000-8000-0000000000a2', 'privacy', 'Privacy policy', $md$
# Privacy policy

*Draft template: replace everything in [[double brackets]] and have it reviewed before publishing.*

[[SHOP NAME]] ([[LEGAL ENTITY NAME]]) is responsible for your personal data. Contact: [[CONTACT EMAIL]].

## What we collect

- **Account details:** name, email address and an encrypted password.
- **Orders:** the items, delivery address, phone number and payment method. We do not store card numbers; online payments are handled by [[PAYMENT PROVIDER]].
- **Technical data:** we use only the cookies needed to keep you signed in and to remember your cart. We do not use advertising or tracking cookies.

## Why we use it

To take and deliver your orders, to email you about them, to prevent fraud and to meet legal and accounting duties. Our legal bases are contract, legal obligation and our legitimate interest in running a safe shop.

## Who receives it

Delivery carriers, our payment provider ([[PAYMENT PROVIDER]]), our email provider ([[EMAIL PROVIDER]]) and our hosting provider ([[HOSTING PROVIDER]]), each only as far as needed.

## How long we keep it

Account data until you delete your account. Order records for [[RETENTION PERIOD, e.g. 7 years]] for accounting, with your name and address removed when you delete your account.

## Your rights

You can download a copy of your data and delete your account at any time from your account page. You can also ask us to correct your data or to stop processing it, and you can complain to your data-protection authority.

## Changes

We will post changes here and update the date below. Last updated: [[DATE]].
$md$, 2),
('01a00000-0000-7000-8000-0000000000a3', 'returns', 'Returns and refunds', $md$
# Returns and refunds

*Draft template: replace everything in [[double brackets]] and have it reviewed before publishing.*

## Cancelling an order

You can cancel an order yourself from your account until we start preparing it. If you already paid online you are refunded to the original payment method.

## Returning goods

You can request a return from your order page within [[RETURN DAYS]] days of delivery. Tell us why, and we will reply by email.

- Items should be unused and in their original packaging.
- [[WHO PAYS FOR RETURN SHIPPING]]
- [[ITEMS THAT CANNOT BE RETURNED, e.g. hygiene products]]

## Refunds

Once we approve a return and receive the goods we refund the price you paid for them to your original payment method, usually within [[REFUND DAYS]] days. For cash on delivery orders we will arrange the refund with you.

## Faulty or wrong items

If something arrives damaged or is not what you ordered, contact [[CONTACT EMAIL]] and we will put it right.
$md$, 3),
('01a00000-0000-7000-8000-0000000000a4', 'shipping', 'Delivery information', $md$
# Delivery information

*Draft template: replace everything in [[double brackets]] and have it reviewed before publishing.*

## Where we deliver

[[COUNTRIES / REGIONS]]

## Delivery options

The delivery methods, prices and estimated times are shown at checkout before you pay. Free delivery applies to orders over [[FREE DELIVERY THRESHOLD]].

## Tracking

When your order ships we email you the carrier and tracking number, and you can see them on your order page.

## Delays and failed deliveries

Delivery estimates are not guaranteed. If a delivery fails, the carrier will contact you; [[WHAT HAPPENS TO RETURNED PARCELS]].
$md$, 4),
('01a00000-0000-7000-8000-0000000000a5', 'contact', 'Contact us', $md$
# Contact us

*Draft template: replace everything in [[double brackets]] before publishing.*

- **Email:** [[CONTACT EMAIL]]
- **Phone:** [[PHONE]] ([[OPENING HOURS]])
- **Address:** [[ADDRESS]]

When you write about an order, please include your order number (shown on your order page and in your confirmation email).
$md$, 5);

-- +goose Down
DROP SCHEMA content CASCADE;
