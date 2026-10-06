# GOAT Store

An e-commerce site on the **GOAT** stack: **Go**, **templ**, **Alpine.js**, **Tailwind**, with PostgreSQL.
It is a *modular monolith*: one binary and one database, but every business domain is a module with a hard boundary,
so a module can later be lifted out into its own service.

## Quick start

```sh
cp .env.example .env     # edit if you like
make up                  # Postgres in Docker
make migrate             # goose migrations
make seed                # demo products + stock
make run                 # templ + sqlc + tailwind, then http://localhost:8080
```

`ADMIN_EMAIL` / `ADMIN_PASSWORD` in `.env` create the back office login at `/admin`.
Payments use a **mock provider** in development: checkout sends you to a fake page with *Pay now* / *Simulate failure*.

| Command | What it does |
| --- | --- |
| `make generate` | templ views and sqlc queries to Go (generated code is committed) |
| `make css` | Tailwind build (downloads the standalone CLI into `bin/` on first use, no Node) |
| `make test` | unit tests plus the architecture test (integration tests skip without a database) |
| `make test-integration` | everything, with `-race`, against an auto-created `goat_test` database |
| `make migrate-new name=add_x` | new SQL migration |
| `make docker` | build the production image (also contains the `migrate` binary) |

## Architecture

```
cmd/server        process entrypoint
cmd/seed          demo data
internal/bootstrap  composition root: builds modules, wires them through public APIs, owns the router
internal/platform   shared infrastructure only (config, db/tx, auth/JWT, outbox+jobs, httpx, ui shell, money, ids)
internal/<module>/
    domain/         entities, rules, errors          (stdlib + uuid only)
    app/            use cases and ports (interfaces)  (own domain + platform only)
    adapters/       postgres (sqlc), http + templ, other-module adapters
    api.go          the ONLY package other modules may import
    module.go       constructor, routes, background loops
```

Modules: `identity`, `catalog`, `inventory`, `cart`, `order`, `payment`, `admin` (composes the others' APIs).

**Rules, enforced by `internal/archtest`:** domain imports nothing internal; app imports only its own domain; a module
reaches another only through that module's root package; platform never imports a business module.

**Data ownership:** each module owns a Postgres schema (`identity`, `catalog`, ...). No cross-schema joins or foreign keys;
modules reference each other by UUID only. This is what makes extraction into a service cheap.

**Transactions across modules:** `db.TxManager.WithTx` puts the transaction in the `context`; every repository uses
`db.Q(ctx, pool)`. Checkout therefore creates the order, reserves stock, opens a payment and clears the cart in **one** transaction
without the modules sharing repository types.

**Events:** a transactional outbox (`platform.jobs`). Modules publish inside their own transaction; subscribers register a handler
per kind. Payment never imports order: `payment.succeeded` flows to the order module through the queue.

### Where `FOR UPDATE SKIP LOCKED` is used

| Use | Where | Why |
| --- | --- | --- |
| Job queue / outbox | `platform/outbox` | workers claim disjoint batches, no double processing, crashed workers are reclaimed after a lease |
| Inventory reservation | `inventory/app` `Reserve` | stock lives in lots; concurrent checkouts skip lots held by others instead of blocking. Never oversells |
| Reservation expiry | `inventory` `ReleaseExpired` | several instances can sweep without fighting |
| Fulfillment pickup | `order` `ClaimForFulfillment` | each worker takes its own batch of paid orders |

A hot product in a single lot makes checkouts queue on that lot's lock; `Reserve` retries with jittered backoff only when total stock
still covers the request, so contention never causes a false "sold out".

### Order lifecycle

`awaiting_payment` to `paid` to `fulfilling` to `shipped`, or `awaiting_payment` to `cancelled`. Illegal transitions are rejected by the domain.
Unpaid orders hold stock for `RESERVATION_TTL` (15 min), then the hold is released and the order cancelled.
If money arrives after the hold was lost, the order is cancelled and an error is logged for a manual refund (no overselling).

### Auth

Ed25519-signed JWT access tokens (15 min) plus rotating, hashed refresh tokens (30 days), in `HttpOnly`, `SameSite=Lax` cookies.
`Authorization: Bearer` is also accepted, and other services can verify tokens with the public key alone.
Replaying a long-revoked refresh token revokes the whole session family. Passwords use argon2id.
CSRF defence is `SameSite=Lax` plus an `Origin`/`Sec-Fetch-Site` check on every state-changing request.

All IDs are **UUIDv7** (time-ordered), generated in Go by `platform/id`.

## Running in production

```sh
docker build -t goat-store .
docker run --rm -e DATABASE_URL=... goat-store /migrate          # apply migrations
docker run -p 8080:8080 --env-file prod.env goat-store           # serve
```

Startup **refuses to run** (with a list of what to fix) if production is misconfigured: mock payments, missing `JWT_SEED` or
webhook secret, example secrets left in, a short admin password, or non-HTTPS `PUBLIC_URL`.
Behind a reverse proxy set `TRUST_PROXY=true` (to trust its forwarding headers) and `PUBLIC_URL=https://your.site`.
CI (`.github/workflows/ci.yml`) vets, builds and runs the whole suite with `-race` against Postgres, and fails if generated code is stale.

## Configuration

See `.env.example` (`TRUST_PROXY`, `PUBLIC_URL`, `DB_MAX_CONNS` are the operational knobs). In production `JWT_SEED` is required (`openssl rand -base64 32`) and cookies become `Secure`.
`PAYMENT_PROVIDER=mock` is the only built-in provider; a real gateway is another adapter implementing `payment/app.Gateway`
(the mock's signed `POST /webhooks/payment` shows the shape).

## Catalog behaviour worth knowing

- **Deleting a product archives it**: it vanishes from the store, search and carts, its URL slug is freed, and past orders keep their
  name/price snapshots. Restore (admin, *Deleted* tab) brings it back hidden. Categories can be renamed (URL stays) or deleted (products become uncategorised).
- Stock is held in batches ("lots"); admin can receive a batch or correct a batch's available count.
- Checkout carries the total the shopper saw; if a price changed meanwhile nothing is reserved or charged and the shopper is asked to confirm.

See `docs/AUDIT.md` for the engineering audit: what was found, what was fixed, and what was deliberately left alone.

## Known gaps / next steps

- No real payment gateway yet (production refuses the mock), and no automated refund when a late payment hits a cancelled order.
- No email verification or password reset.
- No product image upload (admin enters an image URL).
- Single currency per deployment (`CURRENCY`); no tax, shipping rates or discounts.
