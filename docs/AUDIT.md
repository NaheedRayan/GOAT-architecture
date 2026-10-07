# Engineering audit

Two passes: my own review of the code (each suspicion was confirmed against the code, the live
database or both before being listed) and an independent read-only review by a second reviewer.
Every fixed item has a test, or a verified check where a test is not possible.

`[x]` fixed · `[~]` mitigated or accepted by design (see note)

## High

- [x] **H1 Mock payments worked in production.** `/pay/{id}/mock` let a shopper mark their own order paid. Now refused at startup (config validation) *and* inside the payment module. Real providers require a webhook secret.
- [x] **H2 Price shown was not price charged.** Checkout now carries the total the shopper saw (`expected_total`); a mismatch re-renders with a message and reserves/charges nothing.
- [x] **H3 Products could not be deleted.** Soft-delete (archive): hidden from store, search and carts; orders keep their snapshots; the slug is freed; restore brings it back hidden. Admin has Live / Deleted tabs.
- [x] **H4 Login/register unthrottled.** Per-IP and per-IP+account limits on login, per-IP on register, `429` with `Retry-After`; argon2 concurrency is bounded so a burst cannot exhaust memory.
- [x] **H5 `RealIP` trusted `X-Forwarded-For` from anyone.** Now opt-in (`TRUST_PROXY=true`); a test proves a spoofed header does not bypass the limiter.

## Medium

- [x] **M1 Parallel requests could log the user out.** A request racing a rotation now gets an access token only and never has its cookies wiped. Rotation and revocation are tracked separately (`rotated_at`) so a logged-out token is never honoured. *(My first version of this fix let logged-out tokens work for 30 s; the new tests caught it.)*
- [x] **M2 No request body limit.** 1 MiB cap on every request.
- [x] **M3 Tables grew forever.** Hourly purge of done/dead jobs, expired/revoked refresh tokens, finished reservations and abandoned guest carts.
- [x] **M4 Dead jobs were silent.** Logged at error level with kind, id and last error.
- [x] **M5 Shutdown cancelled in-flight jobs.** Handlers run detached from the shutdown signal, with a timeout below the lease.
- [x] **M6 Admin counts/lists capped.** Dashboard uses a `GROUP BY` count; orders and products paginate.
- [x] **M7 Search missed partial words.** Substring match added (wildcards escaped, input length capped).
- [x] **M8 Stock mistakes could not be corrected.** Admin can set each batch's available count.
- [x] **M9 Protocol-relative image URLs** (`//host/x`) accepted. Rejected, together with `javascript:`, `data:` and backslashes.
- [x] **M10 No config validation.** Bad currency, TTLs, missing production secrets, example secrets, short admin password, bad `PUBLIC_URL` all stop startup with a clear list.

## Found by the independent review

- [x] **R2 Open redirect on login** (`next=/<TAB>/evil.com`). `SafeNext` rejects control characters and parses the URL.
- [x] **R5 A stock-out error wiped the typed shipping address.**
- [x] **R6 A customer could cancel an order while its payment was landing**, ending up charged with no order. Cancel now settles the payment first and refuses if it already succeeded.
- [x] **R7 The expiry sweep could release only part of an order**, letting a later payment commit stock that was already resold. An order's reservations are now released as a whole.
- [x] **R8 Anonymous clients could create unlimited carts.** Rate-limited; carts also capped at 50 lines; stale guest carts purged.
- [x] **R9 Cart races.** Adds are a single atomic upsert; guest-cart merge is one locked transaction (exactly-once under parallel requests).
- [x] **R12 Poison and slow jobs.** A job whose worker keeps dying is buried instead of reclaimed forever.
- [x] **R14 Connection-pool pressure.** Pool size is configurable; statement, lock and idle-in-transaction timeouts are set.
- [x] **R15 Replaying a stale checkout form resurrected a cancelled order.** Now refused with an explanation.
- [x] **R17 No length limits** on names/addresses/descriptions.
- [x] **R18 Huge `?page=` caused a 500; unbounded search string.** Both capped.
- [x] **R19 Operational basics:** `/static/` directory listings removed; gzip; HEAD; trailing-slash redirect; fingerprinted static URLs with immutable caching; HSTS (production); Tailwind version pinned.
- [x] **R20 `OriginCheck` broke behind a Host-rewriting proxy.** `PUBLIC_URL` adds the public host to the allow-list.

## Low / operations

- [x] **L1** gzip, HEAD, slash redirect · **L2** versioned assets · **L3** favicon, `X-Request-Id` echoed · **L4** `Dockerfile` (multi-stage, distroless, non-root) and GitHub Actions CI (vet, build, race tests on Postgres, generated-code-is-current check) · **L5** refresh rotation / reuse / logout tests · embedded `cmd/migrate`, so deploys need no external tooling.

## Product-gap pass (storefront, checkout, operations)

Built after a product-manager review of what a real shop needs. All have tests unless noted.

- [x] Product **variants** (size/colour) with independent stock and price; **image gallery** upload (re-encoded, no SVG, pixel-bomb limits); admin **CSV import** (formula-injection safe).
- [x] **Checkout engine**: shipping methods, coupons, tax (exclusive/inclusive), price-changed guard, **guest checkout**, **cash on delivery**, order timeline, tracking, **cancel / return / refund** flows with stock return.
- [x] **Accounts**: email verification, password reset, profile, address book, **data export and account deletion** (every module erases its data via the `account.deleted` event).
- [x] **Email** through the transactional outbox (payload redacted once sent), order notifications, **abandoned-cart reminders**, **low-stock and return alerts** to staff.
- [x] **SEO**: sitemap, robots, canonical URLs, Open Graph, JSON-LD (Product + AggregateRating), safe Markdown **content pages** (admin-editable).
- [x] **Reviews** (purchase-gated, moderated) and **wishlist**; related products; sort and price filters.
- [x] **Admin**: audit log, users/roles (last-admin protected), stock correction, dashboards, review moderation.
- [x] **Operations**: `/readyz`, `/metrics`, tested backup/restore, load-test tool, concurrent-checkout oversell test, staging compose, CI.
- [~] Not built (needs your decision or credentials): card gateway, real legal text, SMS, loyalty, multi-currency, Bangla UI. See the README.

## Deliberately not changed

- [~] **Failed payment cancels the order** (R16). A final failure event is terminal, which is correct for the mock. With a real gateway, declines are retried on the gateway's own page and only a final failure reaches us. Revisit when a gateway is chosen.
- [~] **Access JWTs stay valid up to 15 minutes after logout** (R21). The usual stateless-JWT trade-off; the refresh token is revoked immediately. Shorten `ACCESS_TOKEN_TTL` if that matters.
- [~] **No `script-src` CSP.** Alpine evaluates expressions, which needs `unsafe-eval`; a half-open policy adds little. The CSP that is sent covers framing, `<base>` and plugins.
- [~] **Registration reveals whether an email exists.** Standard UX trade-off, now throttled.
- [~] **Rate limiter is per process.** Enough to blunt guessing; use an edge/WAF limit for a multi-instance fleet.
- [~] **Substring search is a sequential scan.** Fine at thousands of products; add a `pg_trgm` index if the catalog grows large.
- [~] **A single hot product waits on one stock-lot lock.** Retries are bounded and now covered by timeouts; split large batches into several lots to parallelise.
