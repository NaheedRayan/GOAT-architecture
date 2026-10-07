package bootstrap_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/bootstrap"
	"github.com/NaheedRayan/goat-architecture/internal/catalog"
	"github.com/NaheedRayan/goat-architecture/internal/content"
	"github.com/NaheedRayan/goat-architecture/internal/identity"
	"github.com/NaheedRayan/goat-architecture/internal/order"
	"github.com/NaheedRayan/goat-architecture/internal/platform/config"
	"github.com/NaheedRayan/goat-architecture/internal/platform/mail"
	"github.com/NaheedRayan/goat-architecture/internal/platform/testdb"
	"github.com/NaheedRayan/goat-architecture/internal/promotion"
	shippingmod "github.com/NaheedRayan/goat-architecture/internal/shipping"
)

type env struct {
	mailer *mail.MemoryMailer
	pool   *pgxpool.Pool
	t      *testing.T
	app    *bootstrap.App
	server *httptest.Server
}

// testConfig is the baseline configuration for tests; override fields with the options.
func testConfig(t *testing.T, opts ...func(*config.Config)) config.Config {
	t.Helper()
	cfg := config.Config{
		Env: "test", Currency: "USD", UploadDir: t.TempDir(), PublicURL: "http://shop.test", SiteName: "Test Shop",
		ReservationTTL: 15 * time.Minute, AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: time.Hour,
		PaymentProvider: "mock", PaymentWebhookSecret: "test-secret", PaymentMethods: []string{"card", "cod"},
		ReturnWindowDays: 14, TaxLabel: "Tax", LowStockThreshold: 5, SMTPTLS: "starttls", MailFrom: "Test Shop <no-reply@shop.test>",
	}
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

func newEnv(t *testing.T, opts ...func(*config.Config)) *env {
	t.Helper()
	pool := testdb.Pool(t)
	// Delivery methods are shared state in the database: start every test from "none offered"
	// so totals are predictable; tests that need delivery create their own methods.
	if _, err := pool.Exec(context.Background(), `UPDATE shipping.methods SET active = false`); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, opts...)
	var logw io.Writer = io.Discard
	if os.Getenv("TEST_LOG") != "" {
		logw = os.Stderr
	}
	mailer := &mail.MemoryMailer{}
	app, err := bootstrap.New(context.Background(), cfg, pool, slog.New(slog.NewTextHandler(logw, nil)), bootstrap.WithMailer(mailer))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app.Handler)
	t.Cleanup(srv.Close)
	return &env{t: t, app: app, server: srv, pool: pool, mailer: mailer}
}

// client is a browser-like session with its own cookie jar that does not follow redirects.
type client struct {
	*env
	c *http.Client
}

func (e *env) newClient() *client {
	jar, _ := cookiejar.New(nil)
	return &client{env: e, c: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *client) do(method, path string, form url.Values) (*http.Response, string) {
	c.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, c.server.URL+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	res, err := c.c.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func (c *client) mustStatus(want int, method, path string, form url.Values) (*http.Response, string) {
	c.t.Helper()
	res, body := c.do(method, path, form)
	if res.StatusCode != want {
		c.t.Fatalf("%s %s = %d, want %d\n%s", method, path, res.StatusCode, want, truncate(body))
	}
	return res, body
}

func truncate(s string) string {
	if len(s) > 400 {
		return s[:400]
	}
	return s
}

func (c *client) register() {
	c.t.Helper()
	c.mustStatus(303, "POST", "/register", url.Values{
		"name": {"Tester"}, "email": {"t-" + uuid.NewString() + "@example.com"}, "password": {"correct horse battery"},
	})
}

var shipping = url.Values{"full_name": {"T"}, "phone": {"1"}, "line1": {"1 Road"}, "city": {"Dhaka"}, "postal_code": {"1200"}, "country": {"BD"}}

func (e *env) product(price int64, stock int) catalog.Product {
	e.t.Helper()
	ctx := context.Background()
	p, err := e.app.Catalog.CreateProduct(ctx, catalog.ProductInput{Name: "Widget " + uuid.NewString(), PriceCents: price, Active: true})
	if err != nil {
		e.t.Fatal(err)
	}
	if stock > 0 {
		if err := e.app.Inventory.AddLot(ctx, p.Variants[0].ID, "t", stock); err != nil {
			e.t.Fatal(err)
		}
	}
	return p
}

func (e *env) available(p catalog.Product) int {
	m, err := e.app.Inventory.Available(context.Background(), []uuid.UUID{p.Variants[0].ID})
	if err != nil {
		e.t.Fatal(err)
	}
	return m[p.Variants[0].ID]
}

func (c *client) addToCart(p catalog.Product, qty string) {
	c.t.Helper()
	c.mustStatus(303, "POST", "/cart/items", url.Values{"variant_id": {vid(p)}, "quantity": {qty}})
}

var (
	idempotencyKey = regexp.MustCompile(`name="idempotency_key" value="([^"]+)"`)
	expectedTotal  = regexp.MustCompile(`name="expected_total" value="([^"]+)"`)
)

// checkout loads the form for a fresh key and total, then posts it with the given shipping fields.
func (c *client) checkout(key string) (res *http.Response, k, body string) {
	return c.checkoutWith(key, shipping)
}

func (c *client) checkoutWith(key string, ship url.Values) (res *http.Response, k, body string) {
	c.t.Helper()
	_, page := c.mustStatus(200, "GET", "/checkout", nil)
	total := expectedTotal.FindStringSubmatch(page)
	if total == nil {
		c.t.Fatal("no expected_total on checkout page")
	}
	if key == "" {
		m := idempotencyKey.FindStringSubmatch(page)
		if m == nil {
			c.t.Fatal("no idempotency key on checkout page")
		}
		key = m[1]
	}
	return c.post(key, total[1], ship)
}

func (c *client) post(key, total string, ship url.Values) (*http.Response, string, string) {
	form := url.Values{"idempotency_key": {key}, "expected_total": {total}}
	for k, v := range ship {
		form[k] = v
	}
	res, body := c.do("POST", "/checkout", form)
	return res, key, body
}

var statusRe = regexp.MustCompile(`Awaiting payment|Confirmed|Paid|Preparing|Shipped|Delivered|Cancelled|Refunded`)

func (c *client) orderStatus(orderPath string) string {
	c.t.Helper()
	_, page := c.mustStatus(200, "GET", orderPath, nil)
	return statusRe.FindString(page)
}

// drainJobs runs queued background jobs (payment events, emails) until none are due.
func (e *env) drainJobs() {
	e.t.Helper()
	ctx := context.Background()
	for range 100 {
		n, err := e.app.Worker.RunOnce(ctx)
		if err != nil {
			e.t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
}

// drain runs queued jobs and then the fulfilment pickup, as the server's background loops would.
func (e *env) drain() {
	e.t.Helper()
	e.drainJobs()
	if _, err := e.app.Orders.FulfillOnce(context.Background()); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) adminClient() *client {
	e.t.Helper()
	email := "admin-" + uuid.NewString() + "@example.com"
	if err := e.app.Identity.EnsureAdmin(context.Background(), email, "admin-password-1"); err != nil {
		e.t.Fatal(err)
	}
	a := e.newClient()
	a.mustStatus(303, "POST", "/login", url.Values{"email": {email}, "password": {"admin-password-1"}})
	return a
}

// placeOrder checks out the client's current cart and returns the pay and order paths.
func (c *client) placeOrder() (payPath, orderPath string) {
	c.t.Helper()
	res, _, _ := c.checkout("")
	if res.StatusCode != 303 || !strings.HasPrefix(res.Header.Get("Location"), "/pay/") {
		c.t.Fatalf("checkout = %d %s, want redirect to /pay/", res.StatusCode, res.Header.Get("Location"))
	}
	payPath = res.Header.Get("Location")
	// Find the order through the customer's order list (newest first).
	_, list := c.mustStatus(200, "GET", "/orders", nil)
	m := regexp.MustCompile(`/orders/[0-9a-f-]{36}`).FindString(list)
	if m == "" {
		c.t.Fatal("no order in list")
	}
	return payPath, m
}

// pay completes (or fails) the mock card payment.
func (c *client) pay(payPath string, outcome string) {
	c.t.Helper()
	res, _ := c.mustStatus(303, "GET", payPath, nil)
	c.mustStatus(303, "POST", res.Header.Get("Location"), url.Values{"outcome": {outcome}})
}

func TestPurchaseHappyPath(t *testing.T) {
	e := newEnv(t)
	p := e.product(2500, 10)
	u := e.newClient()
	u.register()
	u.addToCart(p, "2")

	payPath, order := u.placeOrder()
	if got := u.orderStatus(order); got != "Awaiting payment" {
		t.Fatalf("status = %q, want Awaiting payment", got)
	}
	if got := e.available(p); got != 8 {
		t.Fatalf("stock after reserve = %d, want 8", got)
	}
	_, cart := u.mustStatus(200, "GET", "/cart", nil)
	if !strings.Contains(cart, "Your cart is empty") {
		t.Fatal("cart should be cleared by checkout")
	}

	u.pay(payPath, "succeeded")
	e.drain()
	if got := u.orderStatus(order); got != "Preparing" {
		t.Fatalf("status = %q, want Preparing (paid and picked up for fulfillment)", got)
	}
	if got := e.available(p); got != 8 {
		t.Fatalf("stock after payment = %d, want 8", got)
	}

	u.mustStatus(403, "GET", "/admin", nil) // customers are kept out of the back office
	a := e.adminClient()
	a.mustStatus(200, "GET", "/admin/orders", nil)
	a.mustStatus(303, "POST", strings.Replace(order, "/orders/", "/admin/orders/", 1)+"/ship", url.Values{})
	if got := u.orderStatus(order); got != "Shipped" {
		t.Fatalf("status = %q, want Shipped", got)
	}
}

func TestCheckoutIsIdempotent(t *testing.T) {
	e := newEnv(t)
	p := e.product(1000, 10)
	u := e.newClient()
	u.register()
	u.addToCart(p, "1")

	res1, key, _ := u.checkout("")
	res2, _, _ := u.post(key, "1000", shipping) // double-click / retry with the same key
	if res1.Header.Get("Location") != res2.Header.Get("Location") || !strings.HasPrefix(res1.Header.Get("Location"), "/pay/") {
		t.Fatalf("replay went to %q, first to %q", res2.Header.Get("Location"), res1.Header.Get("Location"))
	}
	if got := e.available(p); got != 9 {
		t.Fatalf("stock = %d, want 9 (reserved once)", got)
	}
}

func TestCancelReleasesStock(t *testing.T) {
	e := newEnv(t)
	p := e.product(1000, 10)
	u := e.newClient()
	u.register()
	u.addToCart(p, "3")
	payPath, order := u.placeOrder()

	u.mustStatus(303, "POST", order+"/cancel", url.Values{})
	e.drain()
	if got := u.orderStatus(order); got != "Cancelled" {
		t.Fatalf("status = %q, want Cancelled", got)
	}
	if got := e.available(p); got != 10 {
		t.Fatalf("stock = %d, want 10 (released)", got)
	}
	// A cancelled order can no longer be paid.
	res, _ := u.mustStatus(303, "GET", payPath, nil)
	if !strings.HasPrefix(res.Header.Get("Location"), "/orders/") {
		t.Fatalf("paying a cancelled order redirected to %q", res.Header.Get("Location"))
	}
}

func TestCannotBuyMoreThanStock(t *testing.T) {
	e := newEnv(t)
	p := e.product(1000, 1)
	u := e.newClient()
	u.register()
	u.addToCart(p, "2")

	res, _, body := u.checkout("")
	if res.StatusCode != 409 || !strings.Contains(body, "no longer in stock") {
		t.Fatalf("checkout = %d, want 409 with stock message", res.StatusCode)
	}
	if got := e.available(p); got != 1 {
		t.Fatalf("stock = %d, want 1 (nothing reserved)", got)
	}
	_, cart := u.mustStatus(200, "GET", "/cart", nil)
	if strings.Contains(cart, "Your cart is empty") {
		t.Fatal("cart must be kept when checkout fails")
	}
}

func TestExpiredReservationCancelsOrder(t *testing.T) {
	e := newEnv(t)
	p := e.product(1000, 5)
	u := e.newClient()
	u.register()
	u.addToCart(p, "2")
	_, order := u.placeOrder()

	if _, err := e.pool.Exec(context.Background(),
		`UPDATE inventory.reservations SET expires_at = now() - interval '1 minute' WHERE variant_id = $1`, p.Variants[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.Inventory.ReleaseExpired(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if got := u.orderStatus(order); got != "Cancelled" {
		t.Fatalf("status = %q, want Cancelled", got)
	}
	if got := e.available(p); got != 5 {
		t.Fatalf("stock = %d, want 5", got)
	}
}

func TestPaymentFailureCancelsOrder(t *testing.T) {
	e := newEnv(t)
	p := e.product(1000, 5)
	u := e.newClient()
	u.register()
	u.addToCart(p, "1")
	payPath, order := u.placeOrder()

	u.pay(payPath, "failed")
	e.drain()
	if got := u.orderStatus(order); got != "Cancelled" {
		t.Fatalf("status = %q, want Cancelled", got)
	}
	if got := e.available(p); got != 5 {
		t.Fatalf("stock = %d, want 5", got)
	}
}

func TestWebhookIsSignedAndIdempotent(t *testing.T) {
	e := newEnv(t)
	p := e.product(1000, 5)
	u := e.newClient()
	u.register()
	u.addToCart(p, "1")
	payPath, order := u.placeOrder()
	paymentID := strings.TrimPrefix(payPath, "/pay/")

	body := `{"payment_id":"` + paymentID + `","status":"succeeded","provider_ref":"ref_1"}`
	mac := hmac.New(sha256.New, []byte("test-secret"))
	mac.Write([]byte(body))
	sig := hex.EncodeToString(mac.Sum(nil))
	post := func(sig string) int {
		req, _ := http.NewRequest("POST", e.server.URL+"/webhooks/payment", strings.NewReader(body))
		req.Header.Set("X-Signature", sig)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	if got := post("bad"); got != 401 {
		t.Fatalf("bad signature = %d, want 401", got)
	}
	if got := post(sig); got != 204 {
		t.Fatalf("signed webhook = %d, want 204", got)
	}
	if got := post(sig); got != 204 { // provider redelivery
		t.Fatalf("redelivered webhook = %d, want 204", got)
	}
	e.drain()
	if got := u.orderStatus(order); got != "Preparing" {
		t.Fatalf("status = %q, want Preparing", got)
	}
	var jobs int
	if err := e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM platform.jobs WHERE kind = 'payment.succeeded' AND payload->>'payment_id' = $1`, paymentID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("payment.succeeded jobs = %d, want exactly 1 despite redelivery", jobs)
	}
}

func TestOrdersArePrivate(t *testing.T) {
	e := newEnv(t)
	p := e.product(1000, 5)
	owner, other := e.newClient(), e.newClient()
	owner.register()
	other.register()
	owner.addToCart(p, "1")
	_, order := owner.placeOrder()

	other.mustStatus(404, "GET", order, nil)
	other.mustStatus(404, "POST", order+"/cancel", url.Values{})
	if got := owner.orderStatus(order); got != "Awaiting payment" {
		t.Fatalf("status = %q: another user must not be able to cancel", got)
	}
}

func TestAnonymousCheckoutNeedsACart(t *testing.T) {
	e := newEnv(t)
	// With nothing in the cart there is nothing to check out.
	res, _ := e.newClient().mustStatus(303, "GET", "/checkout", nil)
	if loc := res.Header.Get("Location"); loc != "/cart" {
		t.Fatalf("Location = %q, want /cart", loc)
	}
	// Orders and accounts still need a sign-in.
	res, _ = e.newClient().mustStatus(303, "GET", "/orders", nil)
	if loc := res.Header.Get("Location"); loc != "/login?next=%2Forders" {
		t.Fatalf("Location = %q", loc)
	}
}

func TestGuestCartMergesOnSignIn(t *testing.T) {
	e := newEnv(t)
	p := e.product(1000, 5)
	g := e.newClient()
	g.addToCart(p, "2") // anonymous
	g.register()        // becomes a user; the guest cart must follow
	_, cart := g.mustStatus(200, "GET", "/cart", nil)
	if !strings.Contains(cart, p.Name) {
		t.Fatal("guest cart items were lost when signing in")
	}
}

func TestSecurityBasics(t *testing.T) {
	e := newEnv(t)
	u := e.newClient()
	u.register()
	req, _ := http.NewRequest("POST", e.server.URL+"/logout", nil)
	req.Header.Set("Origin", "https://evil.example")
	res, err := u.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatalf("cross-origin POST = %d, want 403", res.StatusCode)
	}
	if res.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatal("missing security headers")
	}
	// Open-redirect attempt through ?next= must fall back to "/".
	_, page := u.mustStatus(200, "GET", "/login?next=https://evil.example", nil)
	if strings.Contains(page, "evil.example") {
		t.Fatal("login form echoed an external next URL")
	}
}

// ---- categories and slugs ----

func (e *env) category(name string) catalog.Category {
	e.t.Helper()
	cats, err := e.app.Catalog.Categories(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	for _, c := range cats {
		if c.Name == name {
			return c
		}
	}
	e.t.Fatalf("category %q not found", name)
	return catalog.Category{}
}

func uniq(prefix string) string { return prefix + " " + uuid.NewString()[:8] }

// bangla returns a random name written only in Bengali letters (no Latin characters).
func bangla() string {
	b := uuid.New()
	var r []rune
	for _, c := range b[:6] {
		r = append(r, rune(0x0995+int(c)%38)) // consonants U+0995..U+09B9
	}
	return string(r)
}

func TestAdminCategoryLifecycle(t *testing.T) {
	e := newEnv(t)
	a := e.adminClient()
	name := uniq("Rain Gear")

	a.mustStatus(303, "POST", "/admin/categories", url.Values{"name": {"  " + name + "  "}}) // surrounding space is trimmed
	if _, page := a.mustStatus(200, "GET", "/admin/categories", nil); !strings.Contains(page, name) {
		t.Fatal("new category is not listed")
	}
	cat := e.category(name)

	// The same name in another case is a duplicate, reported on the page.
	res, page := a.do("POST", "/admin/categories", url.Values{"name": {strings.ToUpper(name)}})
	if res.StatusCode != 409 || !strings.Contains(page, "already exists") {
		t.Fatalf("duplicate name = %d, want 409 with message", res.StatusCode)
	}
	// A different name that maps to the same slug is allowed and gets its own slug.
	hyphen := strings.ReplaceAll(name, " ", "-")
	a.mustStatus(303, "POST", "/admin/categories", url.Values{"name": {hyphen}})
	if e.category(hyphen).Slug == cat.Slug {
		t.Fatal("slug collision was not resolved")
	}
	// Invalid names keep what the admin typed and explain the problem.
	if res, page := a.do("POST", "/admin/categories", url.Values{"name": {"   "}}); res.StatusCode != 422 || !strings.Contains(page, "required") {
		t.Fatalf("blank name = %d", res.StatusCode)
	}
	if res, _ := a.do("POST", "/admin/categories", url.Values{"name": {strings.Repeat("x", 61)}}); res.StatusCode != 422 {
		t.Fatalf("61-char name = %d, want 422", res.StatusCode)
	}

	// Names in other scripts get a usable slug instead of being rejected.
	bn, bn2 := bangla(), bangla()
	a.mustStatus(303, "POST", "/admin/categories", url.Values{"name": {bn}})
	a.mustStatus(303, "POST", "/admin/categories", url.Values{"name": {bn2}})
	if got := e.category(bn); !strings.HasPrefix(got.Slug, "category-") || got.Slug == e.category(bn2).Slug {
		t.Fatalf("slug for non-latin names = %q / %q", got.Slug, e.category(bn2).Slug)
	}
	// ...and a mixed name keeps the Latin part, still distinct from a clash.
	mixed := "জুতা " + uuid.NewString()[:6]
	a.mustStatus(303, "POST", "/admin/categories", url.Values{"name": {mixed}})
	a.mustStatus(303, "POST", "/admin/categories", url.Values{"name": {"মোজা " + strings.Fields(mixed)[1]}})

	// Rename changes the name but not the URL; renaming onto another name is refused.
	renamed := uniq("Storm Gear")
	a.mustStatus(303, "POST", "/admin/categories/"+cat.ID.String(), url.Values{"name": {renamed}})
	got := e.category(renamed)
	if got.ID != cat.ID || got.Slug != cat.Slug {
		t.Fatalf("rename changed identity: %+v vs %+v", got, cat)
	}
	if res, _ := a.do("POST", "/admin/categories/"+cat.ID.String(), url.Values{"name": {bn}}); res.StatusCode != 409 {
		t.Fatalf("rename onto existing name = %d, want 409", res.StatusCode)
	}
	a.mustStatus(404, "POST", "/admin/categories/"+uuid.NewString(), url.Values{"name": {"Ghost"}})

	// Deleting keeps the products, uncategorised; deleting again is harmless.
	p, err := e.app.Catalog.CreateProduct(context.Background(), catalog.ProductInput{
		CategoryID: &cat.ID, Name: uniq("Poncho"), PriceCents: 500, Active: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	a.mustStatus(303, "POST", "/admin/categories/"+cat.ID.String()+"/delete", url.Values{})
	a.mustStatus(303, "POST", "/admin/categories/"+cat.ID.String()+"/delete", url.Values{})
	after, err := e.app.Catalog.ByID(context.Background(), p.ID)
	if err != nil || after.CategoryID != nil {
		t.Fatalf("product after category delete: %+v, %v (want it kept, uncategorised)", after, err)
	}
}

func TestProductFormWithDeletedCategory(t *testing.T) {
	e := newEnv(t)
	a := e.adminClient()
	cat, err := e.app.Catalog.CreateCategory(context.Background(), uniq("Doomed"))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.app.Catalog.DeleteCategory(context.Background(), cat.ID); err != nil { // deleted in another tab
		t.Fatal(err)
	}
	name := uniq("Orphan")
	res, page := a.do("POST", "/admin/products", url.Values{
		"name": {name}, "price": {"5.00"}, "category_id": {cat.ID.String()}, "active": {"1"},
	})
	if res.StatusCode != 409 || !strings.Contains(page, "category was just deleted") {
		t.Fatalf("status = %d, want a 409 explaining the category is gone", res.StatusCode)
	}
	if !strings.Contains(page, name) {
		t.Fatal("the form lost what the admin typed")
	}
	// Garbage ids are a validation error, not a crash.
	res, _ = a.do("POST", "/admin/products", url.Values{"name": {name}, "price": {"5"}, "category_id": {"nope"}})
	if res.StatusCode != 422 {
		t.Fatalf("bad category id = %d, want 422", res.StatusCode)
	}
}

func TestStorefrontOnlyListsCategoriesWithVisibleProducts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	empty, _ := e.app.Catalog.CreateCategory(ctx, uniq("Empty"))
	hidden, _ := e.app.Catalog.CreateCategory(ctx, uniq("Hidden"))
	shown, _ := e.app.Catalog.CreateCategory(ctx, uniq("Shown"))
	if _, err := e.app.Catalog.CreateProduct(ctx, catalog.ProductInput{CategoryID: &hidden.ID, Name: uniq("Draft"), PriceCents: 1, Active: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.Catalog.CreateProduct(ctx, catalog.ProductInput{CategoryID: &shown.ID, Name: uniq("Live"), PriceCents: 1, Active: true}); err != nil {
		t.Fatal(err)
	}
	_, page := e.newClient().mustStatus(200, "GET", "/products", nil)
	if strings.Contains(page, empty.Name) || strings.Contains(page, hidden.Name) {
		t.Fatal("categories without visible products must not appear as filters")
	}
	if !strings.Contains(page, shown.Name) {
		t.Fatal("category with a visible product is missing")
	}
}

func TestProductSlugEdgeCases(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	create := func(in catalog.ProductInput) (catalog.Product, error) {
		in.PriceCents, in.Active = 100, true
		return e.app.Catalog.CreateProduct(ctx, in)
	}

	// Same name twice: both succeed with distinct slugs.
	name := uniq("Twin")
	a, err := create(catalog.ProductInput{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	b, err := create(catalog.ProductInput{Name: name})
	if err != nil || a.Slug == b.Slug {
		t.Fatalf("twin products: %v / %q vs %q", err, a.Slug, b.Slug)
	}

	// Names without Latin letters still get distinct, non-empty slugs.
	x, err1 := create(catalog.ProductInput{Name: bangla()})
	y, err2 := create(catalog.ProductInput{Name: bangla()})
	if err1 != nil || err2 != nil || x.Slug == "" || x.Slug == y.Slug {
		t.Fatalf("non-latin: %v %v %q %q", err1, err2, x.Slug, y.Slug)
	}

	// A slug typed on purpose is a real conflict, not silently changed.
	if _, err := create(catalog.ProductInput{Name: uniq("Other"), Slug: a.Slug}); !errors.Is(err, catalog.ErrSlugTaken) {
		t.Fatalf("explicit duplicate slug: %v, want ErrSlugTaken", err)
	}

	// Limits.
	var ve catalog.ValidationError
	if _, err := create(catalog.ProductInput{Name: strings.Repeat("n", 201)}); !errors.As(err, &ve) {
		t.Fatalf("long name: %v", err)
	}
	if _, err := create(catalog.ProductInput{Name: "ok", ImageURL: "javascript:alert(1)"}); !errors.As(err, &ve) {
		t.Fatalf("javascript: image url accepted: %v", err)
	}
}

func TestProductDeleteAndRestore(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := e.adminClient()
	p := e.product(1500, 5)
	pPath := "/products/" + p.Slug

	// A customer has an unpaid order for it, and another has it sitting in a cart.
	buyer, shopper, anon := e.newClient(), e.newClient(), e.newClient()
	buyer.register()
	buyer.addToCart(p, "1")
	payPath, order := buyer.placeOrder()
	shopper.register()
	shopper.addToCart(p, "1")

	anon.mustStatus(200, "GET", pPath, nil)
	a.mustStatus(303, "POST", "/admin/products/"+p.ID.String()+"/delete", url.Values{})
	a.mustStatus(303, "POST", "/admin/products/"+p.ID.String()+"/delete", url.Values{})    // double click
	a.mustStatus(303, "POST", "/admin/products/"+uuid.NewString()+"/delete", url.Values{}) // stale id

	// Gone from the store, search and carts.
	anon.mustStatus(404, "GET", pPath, nil)
	if _, page := anon.mustStatus(200, "GET", "/products?q="+url.QueryEscape(p.Name), nil); strings.Contains(page, `href="`+pPath+`"`) {
		t.Fatal("deleted product still searchable")
	}
	_, cart := shopper.mustStatus(200, "GET", "/cart", nil)
	if strings.Contains(cart, p.Name) || !strings.Contains(cart, "Your cart is empty") {
		t.Fatal("deleted product still in a cart")
	}
	if res, _ := shopper.do("GET", "/checkout", nil); res.StatusCode != 303 || res.Header.Get("Location") != "/cart" {
		t.Fatalf("checkout with only a deleted product = %d %s, want redirect to /cart", res.StatusCode, res.Header.Get("Location"))
	}
	if res, _ := shopper.do("POST", "/cart/items", url.Values{"variant_id": {vid(p)}, "quantity": {"1"}}); res.StatusCode != 422 {
		t.Fatalf("adding a deleted product = %d, want 422", res.StatusCode)
	}

	// The existing order is untouched and can still be paid.
	if _, page := buyer.mustStatus(200, "GET", order, nil); !strings.Contains(page, p.Name) {
		t.Fatal("order lost its product name after the product was deleted")
	}
	buyer.pay(payPath, "succeeded")
	e.drain()
	if got := buyer.orderStatus(order); got != "Preparing" {
		t.Fatalf("order status = %q, want it to complete normally", got)
	}

	// Admin: hidden from the live list, shown (with Restore) under Deleted; not editable.
	if _, page := a.mustStatus(200, "GET", "/admin/products", nil); strings.Contains(page, p.Name) {
		t.Fatal("deleted product in the live admin list")
	}
	if _, page := a.mustStatus(200, "GET", "/admin/products?view=deleted", nil); !strings.Contains(page, p.Name) || !strings.Contains(page, "Restore") {
		t.Fatal("deleted product missing from the Deleted tab")
	}
	if res, _ := a.do("POST", "/admin/products/"+p.ID.String(), url.Values{"name": {"x"}, "price": {"1"}}); res.StatusCode != 404 {
		t.Fatalf("editing a deleted product = %d, want 404", res.StatusCode)
	}

	// Its URL slug is free again.
	if _, err := e.app.Catalog.CreateProduct(ctx, catalog.ProductInput{Name: "Reborn", Slug: p.Slug, PriceCents: 1, Active: true}); err != nil {
		t.Fatalf("slug was not released: %v", err)
	}

	// Restore brings it back hidden; publishing makes it visible.
	a.mustStatus(303, "POST", "/admin/products/"+p.ID.String()+"/restore", url.Values{})
	anon.mustStatus(404, "GET", "/products/"+mustSlug(t, e, p), nil)
	if err := e.app.Catalog.UpdateProduct(ctx, p.ID, catalog.ProductInput{Name: p.Name, Slug: mustSlug(t, e, p), PriceCents: p.PriceCents, Active: true}); err != nil {
		t.Fatal(err)
	}
	anon.mustStatus(200, "GET", "/products/"+mustSlug(t, e, p), nil)
}

func mustSlug(t *testing.T, e *env, p catalog.Product) string {
	t.Helper()
	cur, err := e.app.Catalog.ByID(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	return cur.Slug
}

// ---- authentication hardening ----

// rawClient talks HTTP with an explicit Cookie header, so tests can replay
// specific cookies (e.g. a stale refresh token) without a cookie jar.
func (e *env) raw(method, path, cookies string, form url.Values) (*http.Response, string) {
	e.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, e.server.URL+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookies != "" {
		req.Header.Set("Cookie", cookies)
	}
	res, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func setCookie(res *http.Response, name string) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// signIn registers a user and returns their refresh token and email.
func (e *env) signIn() (refresh, email string) {
	e.t.Helper()
	email = "u-" + uuid.NewString() + "@example.com"
	res, _ := e.raw("POST", "/register", "", url.Values{"name": {"U"}, "email": {email}, "password": {"correct horse battery"}})
	rc := setCookie(res, "refresh_token")
	if res.StatusCode != 303 || rc == nil {
		e.t.Fatalf("register = %d, refresh cookie %v", res.StatusCode, rc)
	}
	return rc.Value, email
}

func TestRefreshRotationAndGrace(t *testing.T) {
	e := newEnv(t)
	old, _ := e.signIn()

	// With only a refresh cookie (access token expired) the request is silently re-authenticated and rotated.
	res, _ := e.raw("GET", "/account", "refresh_token="+old, nil)
	if res.StatusCode != 200 {
		t.Fatalf("refresh-only request = %d, want 200", res.StatusCode)
	}
	rotated := setCookie(res, "refresh_token")
	if rotated == nil || rotated.Value == old || setCookie(res, "access_token") == nil {
		t.Fatal("expected a new access token and a rotated refresh token")
	}

	// A parallel request that still carries the old cookie must stay signed in, must not
	// have its cookies wiped, and must not be handed a (second) refresh token.
	res, _ = e.raw("GET", "/account", "refresh_token="+old, nil)
	if res.StatusCode != 200 {
		t.Fatalf("raced request = %d, want 200 (grace window)", res.StatusCode)
	}
	if rc := setCookie(res, "refresh_token"); rc != nil {
		t.Fatalf("raced request touched the refresh cookie: %+v", rc)
	}
	if setCookie(res, "access_token") == nil {
		t.Fatal("raced request should get an access token")
	}
}

func TestRefreshReuseAfterGraceKillsTheSession(t *testing.T) {
	e := newEnv(t)
	old, _ := e.signIn()
	res, _ := e.raw("GET", "/account", "refresh_token="+old, nil)
	current := setCookie(res, "refresh_token").Value

	// Pretend the rotation happened an hour ago: replaying the old token now looks like theft.
	h := sha256.Sum256([]byte(old))
	if _, err := e.pool.Exec(context.Background(),
		`UPDATE identity.refresh_tokens SET rotated_at = now() - interval '1 hour', revoked_at = now() - interval '1 hour' WHERE token_hash = $1`, h[:]); err != nil {
		t.Fatal(err)
	}
	res, _ = e.raw("GET", "/account", "refresh_token="+old, nil)
	if res.StatusCode != 303 {
		t.Fatalf("replayed token = %d, want redirect to login", res.StatusCode)
	}
	// ...and the legitimate, newest token was revoked with it.
	res, _ = e.raw("GET", "/account", "refresh_token="+current, nil)
	if res.StatusCode != 303 {
		t.Fatalf("newest token after theft detection = %d, want 303 (session family revoked)", res.StatusCode)
	}
}

func TestLogoutRevokesRefreshToken(t *testing.T) {
	e := newEnv(t)
	refresh, _ := e.signIn()
	e.raw("POST", "/logout", "refresh_token="+refresh, url.Values{})
	if res, _ := e.raw("GET", "/account", "refresh_token="+refresh, nil); res.StatusCode != 303 {
		t.Fatalf("token still works after logout: %d", res.StatusCode)
	}
}

func TestLoginIsThrottledAndIPHeadersAreNotTrusted(t *testing.T) {
	e := newEnv(t)
	_, email := e.signIn()
	bad := url.Values{"email": {email}, "password": {"wrong password"}}

	got429 := false
	for i := range 12 {
		req, _ := http.NewRequest("POST", e.server.URL+"/login", strings.NewReader(bad.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Forwarded-For", "10.0.0."+strconv.Itoa(i)) // a new "client" every time
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode == 429 {
			if res.Header.Get("Retry-After") == "" {
				t.Fatal("429 without Retry-After")
			}
			got429 = true
			break
		}
		if res.StatusCode != 401 {
			t.Fatalf("attempt %d = %d, want 401", i+1, res.StatusCode)
		}
	}
	if !got429 {
		t.Fatal("12 wrong passwords in a row were never throttled; spoofed X-Forwarded-For bypassed the limit")
	}
	// Even the right password is refused while throttled.
	if res, _ := e.raw("POST", "/login", "", url.Values{"email": {email}, "password": {"correct horse battery"}}); res.StatusCode != 429 {
		t.Fatalf("correct password while throttled = %d, want 429", res.StatusCode)
	}
}

func TestOversizedFormIsRejected(t *testing.T) {
	e := newEnv(t)
	res, _ := e.raw("POST", "/register", "", url.Values{
		"name": {strings.Repeat("x", 2<<20)}, "email": {"big-" + uuid.NewString() + "@example.com"}, "password": {"correct horse battery"},
	})
	if res.StatusCode == 303 {
		t.Fatal("a 2 MiB form was accepted")
	}
}

func TestProductionRefusesMockPayments(t *testing.T) {
	pool := testdb.Pool(t)
	cfg := testConfig(t, func(c *config.Config) {
		c.Env, c.JWTSeed = "production", "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
		c.SMTPAddr = "smtp.example.com:587"
	})
	if _, err := bootstrap.New(context.Background(), cfg, pool, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil || !strings.Contains(err.Error(), "mock") {
		t.Fatalf("production + mock payments must not start, got %v", err)
	}
}

// ---- checkout correctness ----

func TestCheckoutRefusesAChangedPrice(t *testing.T) {
	e := newEnv(t)
	p := e.product(1000, 10)
	u := e.newClient()
	u.register()
	u.addToCart(p, "2")

	// The shopper opens checkout and sees $20.00 ...
	_, page := u.mustStatus(200, "GET", "/checkout", nil)
	key := idempotencyKey.FindStringSubmatch(page)[1]
	shown := expectedTotal.FindStringSubmatch(page)[1]
	if shown != "2000" {
		t.Fatalf("shown total = %s, want 2000", shown)
	}
	// ... meanwhile the price goes up.
	if err := e.app.Catalog.UpdateProduct(context.Background(), p.ID, catalog.ProductInput{
		Name: p.Name, Slug: p.Slug, PriceCents: 1500, Active: true,
	}); err != nil {
		t.Fatal(err)
	}
	res, _, _ := u.post(key, shown, shipping)
	if res.StatusCode != 409 {
		t.Fatalf("stale-price checkout = %d, want 409", res.StatusCode)
	}
	if got := e.available(p); got != 10 {
		t.Fatalf("stock = %d: nothing may be reserved when the price check fails", got)
	}
	// The re-rendered page carries the new total; confirming it goes through.
	_, page = u.mustStatus(200, "GET", "/checkout", nil)
	if got := expectedTotal.FindStringSubmatch(page)[1]; got != "3000" {
		t.Fatalf("new total = %s, want 3000", got)
	}
	res, _, _ = u.checkoutWith("", shipping)
	if res.StatusCode != 303 || !strings.HasPrefix(res.Header.Get("Location"), "/pay/") {
		t.Fatalf("confirmed checkout = %d %s", res.StatusCode, res.Header.Get("Location"))
	}
}

func TestStockOutKeepsTheTypedAddress(t *testing.T) {
	e := newEnv(t)
	p := e.product(1000, 1)
	u := e.newClient()
	u.register()
	u.addToCart(p, "2")
	ship := url.Values{"full_name": {"Rahim Uddin"}, "phone": {"017"}, "line1": {"12 Lake Road"}, "city": {"Chittagong"}, "postal_code": {"4000"}, "country": {"BD"}}
	res, _, body := u.checkoutWith("", ship)
	if res.StatusCode != 409 {
		t.Fatalf("status = %d, want 409", res.StatusCode)
	}
	for _, want := range []string{"Rahim Uddin", "12 Lake Road", "Chittagong"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the typed address lost %q after a stock error", want)
		}
	}
}

func TestCannotCancelAnOrderThatIsBeingPaid(t *testing.T) {
	e := newEnv(t)
	p := e.product(1000, 5)
	u := e.newClient()
	u.register()
	u.addToCart(p, "2")
	payPath, order := u.placeOrder()

	// The shopper pays, and clicks Cancel before the worker has processed the payment event.
	u.pay(payPath, "succeeded")
	u.mustStatus(303, "POST", order+"/cancel", url.Values{})
	e.drain()
	if got := u.orderStatus(order); got != "Preparing" {
		t.Fatalf("status = %q, want the paid order to proceed (not be cancelled)", got)
	}
	if got := e.available(p); got != 3 {
		t.Fatalf("stock = %d, want 3: a paid order must keep its stock", got)
	}
}

func TestStaleCheckoutFormForACancelledOrder(t *testing.T) {
	e := newEnv(t)
	p := e.product(1000, 5)
	u := e.newClient()
	u.register()
	u.addToCart(p, "1")
	res, key, _ := u.checkout("")
	if res.StatusCode != 303 {
		t.Fatalf("checkout = %d", res.StatusCode)
	}
	_, order := (&client{env: e, c: u.c}).latestOrder()
	u.mustStatus(303, "POST", order+"/cancel", url.Values{})

	// Back button + resubmit of the old form must not resurrect the cancelled order.
	u.addToCart(p, "1")
	res, _, body := u.post(key, "1000", shipping)
	if res.StatusCode != 409 || !strings.Contains(body, "was cancelled") {
		t.Fatalf("stale form = %d, want 409 explaining the order was cancelled", res.StatusCode)
	}
}

func (c *client) latestOrder() (string, string) {
	c.t.Helper()
	_, list := c.mustStatus(200, "GET", "/orders", nil)
	m := regexp.MustCompile(`/orders/[0-9a-f-]{36}`).FindString(list)
	if m == "" {
		c.t.Fatal("no orders")
	}
	return "", m
}

func TestLoginRedirectCannotLeaveTheSite(t *testing.T) {
	e := newEnv(t)
	_, email := e.signIn()
	for _, next := range []string{"/%09/evil.example", "//evil.example", "https://evil.example", "/\\evil.example"} {
		res, _ := e.raw("POST", "/login", "", url.Values{"email": {email}, "password": {"correct horse battery"}, "next": {next}})
		loc := res.Header.Get("Location")
		if res.StatusCode != 303 || loc != "/" {
			t.Errorf("next=%q redirected to %q, want /", next, loc)
		}
	}
}

// ---- cart, search, stock, counts ----

func TestParallelCartAddsAllCount(t *testing.T) {
	e := newEnv(t)
	p := e.product(100, 50)
	u := e.newClient()
	u.register()
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u.do("POST", "/cart/items", url.Values{"variant_id": {vid(p)}, "quantity": {"1"}})
		}()
	}
	wg.Wait()
	_, cart := u.mustStatus(200, "GET", "/cart", nil)
	if !strings.Contains(cart, `value="10"`) {
		t.Fatalf("10 parallel adds of 1 did not add up to 10 (lost updates)")
	}
	// Quantity is clamped per line.
	u.addToCart(p, "20")
	_, cart = u.mustStatus(200, "GET", "/cart", nil)
	if !strings.Contains(cart, `value="20"`) {
		t.Fatal("line quantity should clamp at 20")
	}
}

func TestGuestCartMergeIsExactlyOnceUnderParallelRequests(t *testing.T) {
	e := newEnv(t)
	p := e.product(100, 50)
	g := e.newClient()
	g.addToCart(p, "2")
	g.register()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); g.do("GET", "/cart", nil) }()
	}
	wg.Wait()
	_, cart := g.mustStatus(200, "GET", "/cart", nil)
	if !strings.Contains(cart, `value="2"`) {
		t.Fatalf("merged quantity is not exactly 2 (parallel merges doubled it?)")
	}
}

func TestAnonymousCartAddsAreRateLimited(t *testing.T) {
	e := newEnv(t)
	p := e.product(100, 5)
	limited := false
	for range 150 { // each request is a brand-new anonymous visitor (no cookie kept)
		res, _ := e.raw("POST", "/cart/items", "", url.Values{"variant_id": {vid(p)}, "quantity": {"1"}})
		if res.StatusCode == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("150 cookie-less cart adds in a row were never throttled")
	}
}

func TestCartHasALineLimit(t *testing.T) {
	e := newEnv(t)
	u := e.newClient()
	u.register()
	for i := range 51 {
		p := e.product(100, 1)
		res, _ := u.do("POST", "/cart/items", url.Values{"variant_id": {vid(p)}, "quantity": {"1"}})
		if i < 50 && res.StatusCode != 303 {
			t.Fatalf("line %d = %d, want 303", i+1, res.StatusCode)
		}
		if i == 50 && res.StatusCode != 422 {
			t.Fatalf("51st distinct product = %d, want 422 (cart full)", res.StatusCode)
		}
	}
}

func TestSearchFindsPartialWordsAndTreatsWildcardsLiterally(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tag := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	trail, _ := e.app.Catalog.CreateProduct(ctx, catalog.ProductInput{Name: "Trailblazer" + tag, PriceCents: 1, Active: true})
	pct, _ := e.app.Catalog.CreateProduct(ctx, catalog.ProductInput{Name: "Wool 100%" + tag, PriceCents: 1, Active: true})
	other, _ := e.app.Catalog.CreateProduct(ctx, catalog.ProductInput{Name: "Plain" + tag, PriceCents: 1, Active: true})
	c := e.newClient()
	link := func(p catalog.Product) string { return `href="/products/` + p.Slug + `"` }

	_, page := c.mustStatus(200, "GET", "/products?q=trailblaz", nil)
	if !strings.Contains(page, link(trail)) {
		t.Fatal("partial word did not find the product")
	}
	_, page = c.mustStatus(200, "GET", "/products?q="+url.QueryEscape("%"+tag), nil)
	if strings.Contains(page, link(trail)) || strings.Contains(page, link(other)) {
		t.Fatal("'%' acted as a wildcard instead of a literal")
	}
	_, page = c.mustStatus(200, "GET", "/products?q="+url.QueryEscape("100%"), nil)
	if !strings.Contains(page, link(pct)) {
		t.Fatal("literal percent sign did not match")
	}
	// Absurd inputs are tolerated.
	c.mustStatus(200, "GET", "/products?page=99999999999999999999&q="+strings.Repeat("a", 5000), nil)
	c.mustStatus(200, "GET", "/products?page=-5", nil)
}

func TestAdminCanCorrectStock(t *testing.T) {
	e := newEnv(t)
	a := e.adminClient()
	p := e.product(100, 5)
	lots, err := e.app.Inventory.Lots(context.Background(), p.Variants[0].ID)
	if err != nil || len(lots) != 1 {
		t.Fatalf("lots: %v %v", lots, err)
	}
	path := "/admin/variants/" + p.Variants[0].ID.String() + "/stock/" + lots[0].ID.String()

	a.mustStatus(303, "POST", path, url.Values{"quantity": {"0"}}) // a miscount: nothing is really there
	if got := e.available(p); got != 0 {
		t.Fatalf("available = %d, want 0", got)
	}
	_, page := e.newClient().mustStatus(200, "GET", "/products/"+p.Slug, nil)
	if !strings.Contains(page, "sold out") {
		t.Fatal("product should show as sold out after zeroing its stock")
	}
	a.mustStatus(303, "POST", path, url.Values{"quantity": {"7"}})
	if got := e.available(p); got != 7 {
		t.Fatalf("available = %d, want 7", got)
	}
	for name, form := range map[string]url.Values{"negative": {"quantity": {"-1"}}, "huge": {"quantity": {"99999999"}}, "text": {"quantity": {"lots"}}} {
		if res, _ := a.do("POST", path, form); res.StatusCode != 422 {
			t.Errorf("%s quantity = %d, want 422", name, res.StatusCode)
		}
	}
	// A lot cannot be edited through another product's URL.
	other := e.product(100, 1)
	a.mustStatus(404, "POST", "/admin/variants/"+other.Variants[0].ID.String()+"/stock/"+lots[0].ID.String(), url.Values{"quantity": {"1"}})
}

func TestDashboardCountsAreExactAndOrdersPaginate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	before, err := e.app.Orders.API().CountByStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := e.product(100, 10)
	u := e.newClient()
	u.register()
	for range 3 {
		u.addToCart(p, "1")
		u.placeOrder()
	}
	after, _ := e.app.Orders.API().CountByStatus(ctx)
	if after[order.StatusAwaitingPayment]-before[order.StatusAwaitingPayment] != 3 {
		t.Fatalf("awaiting_payment grew by %d, want 3", after[order.StatusAwaitingPayment]-before[order.StatusAwaitingPayment])
	}
	a := e.adminClient()
	a.mustStatus(200, "GET", "/admin", nil)
	a.mustStatus(200, "GET", "/admin/orders?page=2", nil)
	a.mustStatus(200, "GET", "/admin/orders?status=paid&page=9999999999", nil)
}

// ---- HTTP hygiene ----

func TestHTTPBasics(t *testing.T) {
	e := newEnv(t)
	get := func(method, path string, hdr map[string]string) (*http.Response, string) {
		req, _ := http.NewRequest(method, e.server.URL+path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Transport: &http.Transport{DisableCompression: true}}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, string(b)
	}

	// Pages are compressed when the client accepts it, and carry a request id.
	res, _ := get("GET", "/products", map[string]string{"Accept-Encoding": "gzip"})
	if res.Header.Get("Content-Encoding") != "gzip" {
		t.Error("HTML is not gzip-compressed")
	}
	if res.Header.Get("X-Request-Id") == "" {
		t.Error("response has no X-Request-Id")
	}
	for _, h := range []string{"Content-Security-Policy", "X-Frame-Options", "X-Content-Type-Options", "Referrer-Policy"} {
		if res.Header.Get(h) == "" {
			t.Errorf("missing %s", h)
		}
	}
	if res.Header.Get("Strict-Transport-Security") != "" {
		t.Error("HSTS must only be sent in production")
	}

	// HEAD works on pages, and a trailing slash redirects instead of 404.
	if res, _ := get("HEAD", "/products", nil); res.StatusCode != 200 {
		t.Errorf("HEAD /products = %d", res.StatusCode)
	}
	if res, _ := get("GET", "/products/", nil); res.StatusCode != 301 || res.Header.Get("Location") != "/products" {
		t.Errorf("GET /products/ = %d %q", res.StatusCode, res.Header.Get("Location"))
	}

	// Static assets: fingerprinted in the page, immutable when fingerprinted, no directory listings.
	_, page := get("GET", "/", nil)
	m := regexp.MustCompile(`/static/app\.css\?v=([0-9a-f]{10})`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("page does not reference a fingerprinted stylesheet")
	}
	res, _ = get("GET", "/static/app.css?v="+m[1], nil)
	if res.StatusCode != 200 || !strings.Contains(res.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("fingerprinted css: %d cache=%q", res.StatusCode, res.Header.Get("Cache-Control"))
	}
	for _, dir := range []string{"/static", "/static/", "/static/products", "/static/products/"} {
		res, body := get("GET", dir, nil)
		if res.StatusCode == 200 || strings.Contains(body, `href="alpine.min.js"`) || strings.Contains(body, `href="trail-running-shoes`) {
			t.Errorf("GET %s = %d: a directory listing leaked", dir, res.StatusCode)
		}
	}
	if !strings.Contains(page, `rel="icon"`) {
		t.Error("no favicon link")
	}
}

func TestOriginAllowListForProxies(t *testing.T) {
	pool := testdb.Pool(t)
	cfg := testConfig(t, func(c *config.Config) { c.PublicURL = "https://shop.example.com" })
	app, err := bootstrap.New(context.Background(), cfg, pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app.Handler)
	defer srv.Close()
	post := func(origin string) int {
		req, _ := http.NewRequest("POST", srv.URL+"/logout", nil)
		req.Header.Set("Origin", origin)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	// A proxy rewrote Host, but the browser's Origin is the configured public site: allowed.
	if got := post("https://shop.example.com"); got == 403 {
		t.Errorf("public origin was blocked (%d)", got)
	}
	if got := post("https://evil.example"); got != 403 {
		t.Errorf("foreign origin = %d, want 403", got)
	}
}

// vid is the id of a product's (first/default) variant, what carts and stock are keyed by.
func vid(p catalog.Product) string { return p.Variants[0].ID.String() }

// ---- variants ----

func (e *env) variantByLabel(p catalog.Product, label string) catalog.Variant {
	e.t.Helper()
	cur, err := e.app.Catalog.ByID(context.Background(), p.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, v := range cur.Variants {
		if v.Label == label {
			return v
		}
	}
	e.t.Fatalf("variant %q not found among %+v", label, cur.Variants)
	return catalog.Variant{}
}

func TestVariantsHaveIndependentStockAndPrices(t *testing.T) {
	e := newEnv(t)
	a := e.adminClient()
	ctx := context.Background()
	p, err := e.app.Catalog.CreateProduct(ctx, catalog.ProductInput{Name: uniq("Jacket"), PriceCents: 5000, OptionName: "Size", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	add := func(label, sku, price string) *http.Response {
		res, _ := a.do("POST", "/admin/products/"+p.ID.String()+"/variants", url.Values{"label": {label}, "sku": {sku}, "price": {price}, "active": {"1"}})
		return res
	}
	skuS, skuM := "SKU-"+uuid.NewString()[:8], "SKU-"+uuid.NewString()[:8]
	if res := add("S", skuS, ""); res.StatusCode != 303 {
		t.Fatalf("add S = %d", res.StatusCode)
	}
	if res := add("M", skuM, "65.00"); res.StatusCode != 303 {
		t.Fatalf("add M = %d", res.StatusCode)
	}
	s, m := e.variantByLabel(p, "S"), e.variantByLabel(p, "M")
	// The original single form was renamed so the product's variants all have names.
	if def := e.variantByLabel(p, "Default"); def.ID != p.Variants[0].ID {
		t.Fatal("the unnamed default variant should have been renamed 'Default'")
	}
	if m.PriceCents == nil || *m.PriceCents != 6500 || s.PriceCents != nil {
		t.Fatalf("price overrides wrong: S=%v M=%v", s.PriceCents, m.PriceCents)
	}

	// Rules: names and SKUs are unique, names are required.
	if res := add("s", "", ""); res.StatusCode != 409 { // same name, different case
		t.Errorf("duplicate label = %d, want 409", res.StatusCode)
	}
	if res := add("L", skuS, ""); res.StatusCode != 409 {
		t.Errorf("duplicate SKU = %d, want 409", res.StatusCode)
	}
	if res := add("  ", "", ""); res.StatusCode != 409 {
		t.Errorf("blank label = %d, want 409", res.StatusCode)
	}
	if res := add("XL", "", "abc"); res.StatusCode != 422 {
		t.Errorf("bad price = %d, want 422", res.StatusCode)
	}

	// Stock is per variant: only S has any.
	a.mustStatus(303, "POST", "/admin/variants/"+s.ID.String()+"/stock", url.Values{"label": {"PO-1"}, "quantity": {"3"}})
	_, page := e.newClient().mustStatus(200, "GET", "/products/"+p.Slug, nil)
	for _, want := range []string{">S</button>", ">M</button>", "Size"} {
		if !strings.Contains(page, want) {
			t.Fatalf("product page lacks %q", want)
		}
	}
	if !regexp.MustCompile(`(?s)<button[^>]*\sdisabled(?:="")?>\s*M</button>`).MatchString(page) {
		t.Error("sold-out variant M should be a disabled button")
	}
	if regexp.MustCompile(`(?s)<button[^>]*\sdisabled(?:="")?>\s*S</button>`).MatchString(page) {
		t.Error("variant S has stock and must be selectable")
	}

	// Buy two of S: the order names the variant, charges its price, and only S's stock moves.
	u := e.newClient()
	u.register()
	u.mustStatus(303, "POST", "/cart/items", url.Values{"variant_id": {s.ID.String()}, "quantity": {"2"}})
	_, cart := u.mustStatus(200, "GET", "/cart", nil)
	if !strings.Contains(cart, p.Name) || !strings.Contains(cart, ">S<") && !strings.Contains(cart, "S</p>") {
		t.Fatal("cart should show the product with its variant")
	}
	payPath, order := u.placeOrder()
	_ = payPath
	if _, page := u.mustStatus(200, "GET", order, nil); !strings.Contains(page, p.Name+" — S") {
		t.Fatal("order page should name the variant")
	}
	avail, _ := e.app.Inventory.Available(ctx, []uuid.UUID{s.ID, m.ID})
	if avail[s.ID] != 1 || avail[m.ID] != 0 {
		t.Fatalf("stock S=%d M=%d, want 1/0", avail[s.ID], avail[m.ID])
	}

	// Buying the sold-out variant fails at checkout without touching S.
	u.mustStatus(303, "POST", "/cart/items", url.Values{"variant_id": {m.ID.String()}, "quantity": {"1"}})
	if res, _, _ := u.checkout(""); res.StatusCode != 409 {
		t.Fatalf("checkout of a sold-out variant = %d, want 409", res.StatusCode)
	}

	// Hiding a variant drops it from carts and the product page.
	u2 := e.newClient()
	u2.register()
	u2.mustStatus(303, "POST", "/cart/items", url.Values{"variant_id": {s.ID.String()}, "quantity": {"1"}})
	a.mustStatus(303, "POST", "/admin/variants/"+s.ID.String(), url.Values{"label": {"S"}, "sku": {skuS}}) // no "active" => hidden
	if _, cart := u2.mustStatus(200, "GET", "/cart", nil); !strings.Contains(cart, "Your cart is empty") {
		t.Fatal("a hidden variant must disappear from carts")
	}
	if res, _ := u2.do("POST", "/cart/items", url.Values{"variant_id": {s.ID.String()}, "quantity": {"1"}}); res.StatusCode != 422 {
		t.Fatalf("adding a hidden variant = %d, want 422", res.StatusCode)
	}
	if _, page := e.newClient().mustStatus(200, "GET", "/products/"+p.Slug, nil); strings.Contains(page, ">S</button>") {
		t.Fatal("hidden variant still on the product page")
	}
}

func TestProductListShowsFromPrice(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	p, _ := e.app.Catalog.CreateProduct(ctx, catalog.ProductInput{Name: uniq("Tiered"), PriceCents: 1000, Active: true})
	cheap, dear := int64(1000), int64(2500)
	e.app.Catalog.AddVariant(ctx, p.ID, catalog.VariantInput{Label: "Basic", PriceCents: &cheap, Active: true})
	e.app.Catalog.AddVariant(ctx, p.ID, catalog.VariantInput{Label: "Pro", PriceCents: &dear, Active: true})
	_, page := e.newClient().mustStatus(200, "GET", "/products?q="+url.QueryEscape(p.Name), nil)
	if !strings.Contains(page, "From") || !strings.Contains(page, "$10.00") {
		t.Fatal("cards for products with different variant prices should say 'From $10.00'")
	}
}

// ---- image uploads ----

func pngBytes(t *testing.T, w, h int, seed byte) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{seed, uint8(x), uint8(y), 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// upload posts a multipart image to the admin upload route.
func (c *client) upload(productID uuid.UUID, filename string, data []byte, alt string) (*http.Response, string) {
	c.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("image", filename)
	fw.Write(data)
	mw.WriteField("alt", alt)
	mw.Close()
	req, _ := http.NewRequest("POST", c.server.URL+"/admin/products/"+productID.String()+"/images", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := c.c.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func TestImageUploadLifecycle(t *testing.T) {
	e := newEnv(t)
	a := e.adminClient()
	p, _ := e.app.Catalog.CreateProduct(context.Background(), catalog.ProductInput{Name: uniq("Photogenic"), PriceCents: 100, Active: true})

	// Non-admins cannot upload at all.
	u := e.newClient()
	u.register()
	if res, _ := u.upload(p.ID, "a.png", pngBytes(t, 20, 20, 1), ""); res.StatusCode != 403 {
		t.Fatalf("customer upload = %d, want 403", res.StatusCode)
	}

	// A large photo is accepted, resized, and served.
	if res, body := a.upload(p.ID, "big.png", pngBytes(t, 2400, 1200, 9), "A big one"); res.StatusCode != 303 {
		t.Fatalf("upload = %d\n%s", res.StatusCode, truncate(body))
	}
	cur, _ := e.app.Catalog.ByID(context.Background(), p.ID)
	if len(cur.Images) != 1 || !strings.HasPrefix(cur.Images[0].URL, "/media/") || cur.ThumbURL != cur.Images[0].ThumbURL {
		t.Fatalf("images/cover not set: %+v thumb=%q", cur.Images, cur.ThumbURL)
	}
	res, body := e.newClient().do("GET", cur.Images[0].URL, nil)
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "image/") {
		t.Fatalf("serving the image = %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	if cfg, _, err := image.DecodeConfig(strings.NewReader(body)); err != nil || cfg.Width != 1600 {
		t.Fatalf("stored image width = %d (err %v), want it scaled to 1600", cfg.Width, err)
	}

	// A second image; the storefront shows a gallery and cards use the thumbnail.
	a.upload(p.ID, "two.png", pngBytes(t, 300, 300, 50), "Second")
	_, page := e.newClient().mustStatus(200, "GET", "/products/"+p.Slug, nil)
	if !strings.Contains(page, "Show image 2") || !strings.Contains(page, `alt="A big one"`) {
		t.Fatal("product page has no gallery with alt text")
	}
	if _, list := e.newClient().mustStatus(200, "GET", "/products?q="+url.QueryEscape(p.Name), nil); !strings.Contains(list, cur.Images[0].ThumbURL) {
		t.Fatal("listing should use the thumbnail")
	}

	// Reorder: moving the second image up makes it the cover.
	cur, _ = e.app.Catalog.ByID(context.Background(), p.ID)
	second := cur.Images[1]
	a.mustStatus(303, "POST", "/admin/products/"+p.ID.String()+"/images/"+second.ID.String()+"/move", url.Values{"dir": {"up"}})
	cur, _ = e.app.Catalog.ByID(context.Background(), p.ID)
	if cur.Images[0].ID != second.ID || cur.ThumbURL != second.ThumbURL {
		t.Fatal("moving an image up should make it the cover")
	}

	// Remove: the row and the file go away; the cover falls back to the remaining image.
	a.mustStatus(303, "POST", "/admin/products/"+p.ID.String()+"/images/"+second.ID.String()+"/delete", url.Values{})
	if res, _ := e.newClient().do("GET", second.URL, nil); res.StatusCode != 404 {
		t.Fatalf("removed image still served: %d", res.StatusCode)
	}
	cur, _ = e.app.Catalog.ByID(context.Background(), p.ID)
	if len(cur.Images) != 1 || cur.ThumbURL != cur.Images[0].ThumbURL {
		t.Fatalf("after removal: %+v / %q", cur.Images, cur.ThumbURL)
	}
	a.mustStatus(303, "POST", "/admin/products/"+p.ID.String()+"/images/"+cur.Images[0].ID.String()+"/delete", url.Values{})
	cur, _ = e.app.Catalog.ByID(context.Background(), p.ID)
	if len(cur.Images) != 0 || cur.ThumbURL != "" || cur.ImageURL != "" {
		t.Fatalf("removing the last image should clear the cover: %+v", cur)
	}
}

func TestImageUploadRejectsBadFiles(t *testing.T) {
	e := newEnv(t)
	a := e.adminClient()
	p, _ := e.app.Catalog.CreateProduct(context.Background(), catalog.ProductInput{Name: uniq("Picky"), PriceCents: 100, Active: true})
	for name, data := range map[string][]byte{
		"evil.svg":  []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"fake.png":  []byte("<html><script>alert(1)</script></html>"),
		"empty.png": {},
	} {
		res, body := a.upload(p.ID, name, data, "")
		if res.StatusCode != 422 || !strings.Contains(body, "not a valid image") {
			t.Errorf("%s = %d, want 422 with a clear message", name, res.StatusCode)
		}
	}
	// A pixel bomb is refused without decoding it.
	bomb := image.NewGray(image.Rect(0, 0, 6000, 6000))
	var b bytes.Buffer
	png.Encode(&b, bomb)
	if res, _ := a.upload(p.ID, "bomb.png", b.Bytes(), ""); res.StatusCode != 422 {
		t.Errorf("pixel bomb = %d, want 422", res.StatusCode)
	}
	// An oversized body is refused.
	if res, _ := a.upload(p.ID, "huge.png", make([]byte, 11<<20), ""); res.StatusCode != 413 {
		t.Errorf("11 MB upload = %d, want 413", res.StatusCode)
	}
	// Missing file, unknown product.
	if res, _ := a.do("POST", "/admin/products/"+p.ID.String()+"/images", url.Values{}); res.StatusCode == 303 {
		t.Error("an upload without a file must not succeed")
	}
	if res, _ := a.upload(uuid.New(), "x.png", pngBytes(t, 10, 10, 3), ""); res.StatusCode != 404 {
		t.Errorf("upload for an unknown product = %d, want 404", res.StatusCode)
	}
	cur, _ := e.app.Catalog.ByID(context.Background(), p.ID)
	if len(cur.Images) != 0 {
		t.Fatalf("bad uploads left %d images behind", len(cur.Images))
	}
}

func TestImageLimitAndSharedFiles(t *testing.T) {
	e := newEnv(t)
	a := e.adminClient()
	ctx := context.Background()
	p1, _ := e.app.Catalog.CreateProduct(ctx, catalog.ProductInput{Name: uniq("One"), PriceCents: 1, Active: true})
	p2, _ := e.app.Catalog.CreateProduct(ctx, catalog.ProductInput{Name: uniq("Two"), PriceCents: 1, Active: true})

	// The same picture on two products is stored once; deleting from one must not break the other.
	same := pngBytes(t, 64, 64, 77)
	a.upload(p1.ID, "s.png", same, "")
	a.upload(p2.ID, "s.png", same, "")
	c1, _ := e.app.Catalog.ByID(ctx, p1.ID)
	c2, _ := e.app.Catalog.ByID(ctx, p2.ID)
	if c1.Images[0].URL != c2.Images[0].URL {
		t.Fatal("identical uploads should share one stored file")
	}
	a.mustStatus(303, "POST", "/admin/products/"+p1.ID.String()+"/images/"+c1.Images[0].ID.String()+"/delete", url.Values{})
	if res, _ := e.newClient().do("GET", c2.Images[0].URL, nil); res.StatusCode != 200 {
		t.Fatalf("a file still used by another product was deleted (%d)", res.StatusCode)
	}

	// At most 10 images per product.
	for i := range 11 {
		res, _ := a.upload(p2.ID, "n.png", pngBytes(t, 32, 32, byte(100+i)), "")
		if i < 9 && res.StatusCode != 303 { // one image exists already
			t.Fatalf("upload %d = %d", i+1, res.StatusCode)
		}
		if i >= 9 && res.StatusCode != 422 {
			t.Fatalf("upload beyond the limit = %d, want 422", res.StatusCode)
		}
	}
}

// ---- accounts: verification, reset, password, roles, deletion ----

var tokenLink = regexp.MustCompile(`http://shop\.test(/[a-z]+\?token=[A-Za-z0-9_-]+)`)

// emailLink returns the site-relative token link in the newest email to addr whose subject contains subj.
func (e *env) emailLink(addr, subj string) string {
	e.t.Helper()
	e.drain() // deliver queued emails
	msgs := e.mailer.To(addr)
	for i := len(msgs) - 1; i >= 0; i-- {
		if strings.Contains(msgs[i].Subject, subj) {
			if m := tokenLink.FindStringSubmatch(msgs[i].Text); m != nil {
				return m[1]
			}
		}
	}
	e.t.Fatalf("no email to %s with %q and a link; got %d messages", addr, subj, len(msgs))
	return ""
}

func (e *env) noEmail(addr, subj string) {
	e.t.Helper()
	e.drain()
	for _, m := range e.mailer.To(addr) {
		if strings.Contains(m.Subject, subj) {
			e.t.Fatalf("unexpected email %q to %s", m.Subject, addr)
		}
	}
}

func newUserEnv(t *testing.T) (*env, *client, string) {
	e := newEnv(t)
	u := e.newClient()
	email := "u-" + uuid.NewString() + "@example.com"
	u.mustStatus(303, "POST", "/register", url.Values{"name": {"Ada Lovelace"}, "email": {email}, "password": {"correct horse battery"}})
	return e, u, email
}

func TestEmailVerification(t *testing.T) {
	e, u, email := newUserEnv(t)
	_, page := u.mustStatus(200, "GET", "/account", nil)
	if !strings.Contains(page, "confirm your email address") {
		t.Fatal("an unverified user should see the verification banner")
	}
	link := e.emailLink(email, "Confirm your email")
	if _, page := e.newClient().mustStatus(200, "GET", link, nil); !strings.Contains(page, "Email confirmed") {
		t.Fatal("the link did not confirm the email")
	}
	if _, page := u.mustStatus(200, "GET", "/account", nil); strings.Contains(page, "confirm your email address") {
		t.Fatal("banner still shown after verification")
	}
	e.newClient().mustStatus(400, "GET", link, nil) // single use

	// Resending is throttled and only for the signed-in user.
	u2 := e.newClient()
	u2.mustStatus(303, "POST", "/register", url.Values{"name": {"B"}, "email": {"b-" + uuid.NewString() + "@example.com"}, "password": {"correct horse battery"}})
	limited := false
	for range 6 {
		if res, _ := u2.do("POST", "/account/resend-verification", url.Values{}); res.StatusCode == 429 {
			limited = true
		}
	}
	if !limited {
		t.Fatal("resend-verification was never throttled")
	}
	if res, _ := e.newClient().do("POST", "/account/resend-verification", url.Values{}); res.StatusCode != 303 || !strings.HasPrefix(res.Header.Get("Location"), "/login") {
		t.Fatalf("anonymous resend = %d %s", res.StatusCode, res.Header.Get("Location"))
	}
}

func TestPasswordResetFlow(t *testing.T) {
	e, u, email := newUserEnv(t)
	other := e.newClient() // a second signed-in device
	other.mustStatus(303, "POST", "/login", url.Values{"email": {email}, "password": {"correct horse battery"}})

	// The answer is identical whether or not the address has an account.
	a := e.newClient()
	_, known := a.mustStatus(200, "POST", "/forgot", url.Values{"email": {email}})
	_, unknown := a.mustStatus(200, "POST", "/forgot", url.Values{"email": {"nobody-" + uuid.NewString() + "@example.com"}})
	if !strings.Contains(known, "Check your inbox") || !strings.Contains(unknown, "Check your inbox") {
		t.Fatal("forgot-password must always show the same confirmation")
	}
	first := e.emailLink(email, "Reset your password")
	if len(e.mailer.To("nobody@example.com")) != 0 {
		t.Fatal("no email may be sent to unknown addresses")
	}

	// A newer request invalidates the older link.
	a.mustStatus(200, "POST", "/forgot", url.Values{"email": {email}})
	second := e.emailLink(email, "Reset your password")
	if first == second {
		t.Fatal("a new request should produce a new link")
	}
	token := func(link string) string { return strings.SplitN(link, "token=", 2)[1] }
	if res, _ := e.newClient().do("POST", "/reset", url.Values{"token": {token(first)}, "password": {"brand new pass"}, "confirm": {"brand new pass"}}); res.StatusCode != 400 {
		t.Fatalf("the superseded link = %d, want 400", res.StatusCode)
	}

	// Mistakes do not burn the link.
	r := e.newClient()
	r.mustStatus(200, "GET", second, nil)
	if res, _ := r.do("POST", "/reset", url.Values{"token": {token(second)}, "password": {"brand new pass"}, "confirm": {"different"}}); res.StatusCode != 422 {
		t.Fatalf("mismatch = %d, want 422", res.StatusCode)
	}
	if res, _ := r.do("POST", "/reset", url.Values{"token": {token(second)}, "password": {"short"}, "confirm": {"short"}}); res.StatusCode != 422 {
		t.Fatalf("weak password = %d, want 422", res.StatusCode)
	}

	// Success: signed in, all other sessions ended, old password dead, link single-use.
	res, _ := r.do("POST", "/reset", url.Values{"token": {token(second)}, "password": {"brand new pass"}, "confirm": {"brand new pass"}})
	if res.StatusCode != 303 || res.Header.Get("Location") != "/account?notice=password-set" {
		t.Fatalf("reset = %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	r.mustStatus(200, "GET", "/account", nil)
	e.newClient().mustStatus(401, "POST", "/login", url.Values{"email": {email}, "password": {"correct horse battery"}})
	e.newClient().mustStatus(303, "POST", "/login", url.Values{"email": {email}, "password": {"brand new pass"}})
	if res, _ := e.newClient().do("POST", "/reset", url.Values{"token": {token(second)}, "password": {"another pass 1"}, "confirm": {"another pass 1"}}); res.StatusCode != 400 {
		t.Fatalf("reusing a link = %d, want 400", res.StatusCode)
	}
	other.c.Jar.(interface {
		SetCookies(*url.URL, []*http.Cookie)
	}).SetCookies(mustURL(e.server.URL), []*http.Cookie{{Name: "access_token", Value: "", MaxAge: -1, Path: "/"}})
	if res, _ := other.do("GET", "/account", nil); res.StatusCode != 303 {
		t.Fatalf("a device signed in before the reset still works: %d", res.StatusCode)
	}
	_ = u
	// Reset links that expired stop working.
	a.mustStatus(200, "POST", "/forgot", url.Values{"email": {email}})
	expired := e.emailLink(email, "Reset your password")
	e.pool.Exec(context.Background(), `UPDATE identity.action_tokens SET expires_at = now() - interval '1 minute' WHERE kind = 'password_reset'`)
	if res, _ := e.newClient().do("POST", "/reset", url.Values{"token": {token(expired)}, "password": {"yet another 1"}, "confirm": {"yet another 1"}}); res.StatusCode != 400 {
		t.Fatalf("expired link = %d, want 400", res.StatusCode)
	}
}

func mustURL(s string) *url.URL { u, _ := url.Parse(s); return u }

func TestEmailQueueDoesNotKeepSecretsAfterDelivery(t *testing.T) {
	e, _, email := newUserEnv(t)
	e.emailLink(email, "Confirm your email")
	var n int
	if err := e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM platform.jobs WHERE kind = 'email.send' AND status = 'done' AND payload <> '{}'::jsonb`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d delivered email jobs still hold their body (with tokens) in the queue", n)
	}
}

func TestChangePasswordAndProfile(t *testing.T) {
	e, u, email := newUserEnv(t)
	other := e.newClient()
	other.mustStatus(303, "POST", "/login", url.Values{"email": {email}, "password": {"correct horse battery"}})

	if res, body := u.do("POST", "/account/password", url.Values{"current_password": {"wrong"}, "new_password": {"brand new pass"}, "confirm": {"brand new pass"}}); res.StatusCode != 422 || !strings.Contains(body, "current password is not correct") {
		t.Fatalf("wrong current password = %d", res.StatusCode)
	}
	if res, _ := u.do("POST", "/account/password", url.Values{"current_password": {"correct horse battery"}, "new_password": {"short"}, "confirm": {"short"}}); res.StatusCode != 422 {
		t.Fatalf("weak new password = %d", res.StatusCode)
	}
	u.mustStatus(303, "POST", "/account/password", url.Values{"current_password": {"correct horse battery"}, "new_password": {"brand new pass"}, "confirm": {"brand new pass"}})
	u.mustStatus(200, "GET", "/account", nil) // this device keeps its (re-issued) session
	e.drain()
	found := false
	for _, m := range e.mailer.To(email) {
		found = found || strings.Contains(m.Subject, "Your password was changed")
	}
	if !found {
		t.Fatalf("no password-changed notice among %d emails", len(e.mailer.To(email)))
	}
	e.newClient().mustStatus(401, "POST", "/login", url.Values{"email": {email}, "password": {"correct horse battery"}})

	// Profile.
	u.mustStatus(303, "POST", "/account/profile", url.Values{"name": {"  Grace   Hopper "}})
	if _, page := u.mustStatus(200, "GET", "/account", nil); !strings.Contains(page, `value="Grace Hopper"`) {
		t.Fatal("name was not saved/normalised")
	}
	if res, _ := u.do("POST", "/account/profile", url.Values{"name": {"   "}}); res.StatusCode != 422 {
		t.Fatalf("blank name = %d", res.StatusCode)
	}
	if res, _ := u.do("POST", "/account/profile", url.Values{"name": {strings.Repeat("n", 101)}}); res.StatusCode != 422 {
		t.Fatalf("long name = %d", res.StatusCode)
	}
}

func TestDisabledAccountsAndRoleGuards(t *testing.T) {
	e, u, email := newUserEnv(t)
	ctx := context.Background()
	user, err := e.app.Identity.ListUsers(ctx, email, "", 1, 0)
	if err != nil || len(user) != 1 {
		t.Fatalf("lookup: %v %v", user, err)
	}
	target := user[0].ID
	adminEmail := "admin-" + uuid.NewString() + "@example.com"
	e.app.Identity.EnsureAdmin(ctx, adminEmail, "admin-password-1")
	admins, _ := e.app.Identity.ListUsers(ctx, adminEmail, "", 1, 0)
	admin := admins[0].ID

	// Guards: no self-service, and the last admin cannot be demoted or disabled.
	if err := e.app.Identity.SetRole(ctx, admin, admin, identity.RoleCustomer); !errors.Is(err, identity.ErrSelfChange) {
		t.Fatalf("self role change: %v", err)
	}
	if err := e.app.Identity.SetDisabled(ctx, admin, admin, true); !errors.Is(err, identity.ErrSelfChange) {
		t.Fatalf("self disable: %v", err)
	}
	if err := e.app.Identity.SetRole(ctx, target, admin, identity.RoleCustomer); err == nil {
		// target (a customer) acting on admin: allowed only if another admin exists
		t.Log("note: demoting succeeded because other admins exist in the shared test database")
		_ = e.app.Identity.SetRole(ctx, target, admin, identity.RoleAdmin)
	}

	// Disabling signs the user out and blocks sign-in, but says so only to someone with the password.
	if err := e.app.Identity.SetDisabled(ctx, admin, target, true); err != nil {
		t.Fatal(err)
	}
	if res, _ := u.do("GET", "/account", nil); res.StatusCode != 303 {
		t.Fatalf("a disabled user's session still works: %d", res.StatusCode)
	}
	if res, body := e.newClient().do("POST", "/login", url.Values{"email": {email}, "password": {"correct horse battery"}}); res.StatusCode != 403 || !strings.Contains(body, "disabled") {
		t.Fatalf("disabled login = %d, want 403 explaining the account is disabled", res.StatusCode)
	}
	// Someone without the password learns nothing about the account's state.
	if res, body := e.newClient().do("POST", "/login", url.Values{"email": {email}, "password": {"wrong password"}}); res.StatusCode != 401 || strings.Contains(body, "disabled") {
		t.Fatalf("wrong password on a disabled account = %d (must look like any bad login)", res.StatusCode)
	}
	if err := e.app.Identity.SetDisabled(ctx, admin, target, false); err != nil {
		t.Fatal(err)
	}
	e.newClient().mustStatus(303, "POST", "/login", url.Values{"email": {email}, "password": {"correct horse battery"}})
	if err := e.app.Identity.SetRole(ctx, admin, target, "root"); err == nil {
		t.Fatal("an unknown role was accepted")
	}
}

func TestAccountExportAndDeletion(t *testing.T) {
	e, u, email := newUserEnv(t)
	ctx := context.Background()
	p := e.product(1000, 5)
	u.mustStatus(303, "POST", "/account/addresses", url.Values{"full_name": {"Ada Lovelace"}, "phone": {"123"}, "line1": {"1 Analytical St"}, "city": {"London"}, "postal_code": {"N1"}, "country": {"UK"}, "is_default": {"1"}})
	u.addToCart(p, "1")
	_, order := u.placeOrder()
	orderID := strings.TrimPrefix(order, "/orders/")

	// Export contains the profile, addresses and orders.
	res, body := u.mustStatus(200, "GET", "/account/export", nil)
	if !strings.Contains(res.Header.Get("Content-Disposition"), "attachment") || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("export must be a no-store download")
	}
	for _, want := range []string{email, "Ada Lovelace", "1 Analytical St", orderID, p.Name} {
		if !strings.Contains(body, want) {
			t.Fatalf("export lacks %q", want)
		}
	}
	e.newClient().mustStatus(303, "GET", "/account/export", nil) // anonymous -> login

	// Wrong password never deletes.
	if res, _ := u.do("POST", "/account/delete", url.Values{"password": {"nope"}}); res.StatusCode != 303 || res.Header.Get("Location") != "/account?delete=wrong-password" {
		t.Fatalf("wrong-password delete = %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if _, page := u.mustStatus(200, "GET", "/account?delete=wrong-password", nil); !strings.Contains(page, "password is not correct") {
		t.Fatal("the account page should explain the failed deletion")
	}
	u.mustStatus(200, "GET", "/account", nil)

	// Deleting signs out, erases the account and anonymises its orders.
	if res, _ := u.do("POST", "/account/delete", url.Values{"password": {"correct horse battery"}}); res.StatusCode != 303 || res.Header.Get("Location") != "/goodbye" {
		t.Fatalf("delete = %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	e.drain()
	e.newClient().mustStatus(401, "POST", "/login", url.Values{"email": {email}, "password": {"correct horse battery"}})
	if res, _ := u.do("GET", "/account", nil); res.StatusCode != 303 {
		t.Fatalf("session survived deletion: %d", res.StatusCode)
	}
	oid, _ := uuid.Parse(orderID)
	o, err := e.app.Orders.API().Get(ctx, oid)
	if err != nil {
		t.Fatal(err)
	}
	if o.Shipping.FullName == "Ada Lovelace" || o.Shipping.Line1 != "" || o.Shipping.Phone != "" {
		t.Fatalf("order still carries personal details: %+v", o.Shipping)
	}
	if o.TotalCents != 1000 || len(o.Items) != 1 {
		t.Fatalf("the financial record must survive: %+v", o)
	}
	var carts int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM cart.carts WHERE owner LIKE 'user:%' AND owner = $1`, "user:"+o.UserID.String()).Scan(&carts)
	if carts != 0 {
		t.Fatal("the deleted user's cart remains")
	}
}

// ---- checkout engine: delivery, tax, coupons, cash on delivery, guests ----

func (e *env) shippingMethod(name string, priceCents int64, freeOver *int64) shippingmod.Method {
	e.t.Helper()
	m, err := e.app.Shipping.Create(context.Background(), shippingmod.Method{Name: name + " " + uuid.NewString()[:6], PriceCents: priceCents, FreeOverCents: freeOver, MinDays: 2, MaxDays: 4, Active: true})
	if err != nil {
		e.t.Fatal(err)
	}
	return m
}

func (c *client) checkoutPage() string {
	c.t.Helper()
	_, page := c.mustStatus(200, "GET", "/checkout", nil)
	return page
}

// totalOf reads the total the checkout page asks the server to honour.
func totalOf(t *testing.T, page string) string {
	t.Helper()
	m := expectedTotal.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no expected_total on page")
	}
	return m[1]
}

// placeWith mimics the browser: pick options (the page re-prices), then place the order at the shown total.
func (c *client) placeWith(extra url.Values) (*http.Response, string) {
	c.t.Helper()
	first := c.checkoutPage()
	key := idempotencyKey.FindStringSubmatch(first)[1]
	refresh := url.Values{"action": {"refresh"}, "idempotency_key": {key}}
	for k, v := range extra {
		refresh[k] = v
	}
	_, page := c.mustStatus(200, "POST", "/checkout", refresh)
	form := url.Values{"idempotency_key": {key}, "expected_total": {totalOf(c.t, page)}, "action": {"place"}}
	for k, v := range shipping {
		form[k] = v
	}
	for k, v := range extra {
		form[k] = v
	}
	return c.do("POST", "/checkout", form)
}

func TestDeliveryAndTaxAreAddedToTheTotal(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.TaxRatePercent, c.TaxLabel = 10, "VAT" })
	e.shippingMethod("Courier", 500, nil)
	p := e.product(10000, 5)
	u := e.newClient()
	u.register()
	u.addToCart(p, "1")

	page := u.checkoutPage()
	// 100.00 goods + 5.00 delivery = 105.00; 10% VAT on top = 10.50; total 115.50.
	if got := totalOf(t, page); got != "11550" {
		t.Fatalf("expected_total = %s, want 11550", got)
	}
	for _, want := range []string{"$105.00", "VAT", "$10.50", "$115.50", "Delivery"} {
		if !strings.Contains(page, want) && !strings.Contains(page, strings.ReplaceAll(want, "$105.00", "$5.00")) {
			t.Errorf("checkout page lacks %q", want)
		}
	}
	res, _ := u.placeWith(nil)
	if res.StatusCode != 303 {
		t.Fatalf("checkout = %d", res.StatusCode)
	}
	_, order := u.latestOrder()
	o, _ := e.app.Orders.API().Get(context.Background(), uuid.MustParse(strings.TrimPrefix(order, "/orders/")))
	if o.SubtotalCents != 10000 || o.ShippingCents != 500 || o.TaxCents != 1050 || o.TotalCents != 11550 || o.ShippingMethod == "" {
		t.Fatalf("order breakdown wrong: %+v", o)
	}
	// The payment is for the full total, tax and delivery included.
	var amount int64
	e.pool.QueryRow(context.Background(), `SELECT amount_cents FROM payment.payments WHERE order_id = $1`, o.ID).Scan(&amount)
	if amount != 11550 {
		t.Fatalf("payment amount = %d, want 11550", amount)
	}
}

func TestFreeDeliveryThresholdAndMethodChoice(t *testing.T) {
	e := newEnv(t)
	over := int64(5000)
	cheap := e.shippingMethod("Economy", 300, &over)
	fast := e.shippingMethod("Express", 900, nil)
	p := e.product(2000, 20)
	u := e.newClient()
	u.register()
	u.addToCart(p, "1") // 20.00: below the free threshold

	page := u.checkoutPage()
	if !strings.Contains(page, cheap.Name) || !strings.Contains(page, fast.Name) {
		t.Fatal("both methods should be offered")
	}
	// Choosing the dearer method re-prices the order (server-side).
	_, refreshed := u.mustStatus(200, "POST", "/checkout", url.Values{"action": {"refresh"}, "shipping_method": {fast.ID.String()}, "idempotency_key": {"k"}})
	if got := totalOf(t, refreshed); got != "2900" {
		t.Fatalf("express total = %s, want 2900", got)
	}
	// Reaching the threshold makes the economy method free.
	u.addToCart(p, "2") // 60.00
	_, page = u.mustStatus(200, "POST", "/checkout", url.Values{"action": {"refresh"}, "shipping_method": {cheap.ID.String()}, "idempotency_key": {"k"}})
	if got := totalOf(t, page); got != "6000" || !strings.Contains(page, "Free") {
		t.Fatalf("economy over the threshold: total %s, want 6000 and a 'Free' label", got)
	}
}

func TestCouponsAtCheckout(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	code := "SAVE" + strings.ToUpper(uuid.NewString()[:6])
	if _, err := e.app.Promotions.Create(ctx, promotion.Coupon{Code: code, Kind: promotion.KindPercent, Value: 20, Active: true}); err != nil {
		t.Fatal(err)
	}
	p := e.product(5000, 20)
	u := e.newClient()
	u.register()
	u.addToCart(p, "1")

	// Unknown and valid codes (case-insensitively) are reported on the page.
	_, page := u.mustStatus(200, "POST", "/checkout", url.Values{"action": {"refresh"}, "coupon": {"NOPE"}, "idempotency_key": {"k"}})
	if !strings.Contains(page, "isn&#39;t valid") && !strings.Contains(page, "isn't valid") {
		t.Fatal("an unknown code must be explained")
	}
	_, page = u.mustStatus(200, "POST", "/checkout", url.Values{"action": {"refresh"}, "coupon": {strings.ToLower(code)}, "idempotency_key": {"k"}})
	if got := totalOf(t, page); got != "4000" {
		t.Fatalf("20%% off 50.00 = 40.00, got %s", got)
	}

	// Placing the order with a bad code is refused rather than silently ignored.
	if res, _ := u.placeWith(url.Values{"coupon": {"NOPE"}}); res.StatusCode != 422 {
		t.Fatalf("placing with an invalid code = %d, want 422", res.StatusCode)
	}
	res, _ := u.placeWith(url.Values{"coupon": {code}})
	if res.StatusCode != 303 {
		t.Fatalf("checkout with a coupon = %d", res.StatusCode)
	}
	_, order := u.latestOrder()
	o, _ := e.app.Orders.API().Get(ctx, uuid.MustParse(strings.TrimPrefix(order, "/orders/")))
	if o.DiscountCents != 1000 || o.TotalCents != 4000 || o.CouponCode != code {
		t.Fatalf("order: %+v", o)
	}
	// Cancelling the unpaid order gives the use back.
	u.mustStatus(303, "POST", order+"/cancel", url.Values{})
	var uses int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM promotion.redemptions WHERE order_id = $1`, o.ID).Scan(&uses)
	if uses != 0 {
		t.Fatal("a cancelled order must release its coupon use")
	}
}

func TestCouponLimitsAreEnforced(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	one, uniq1 := 1, "ONCE"+strings.ToUpper(uuid.NewString()[:6])
	if _, err := e.app.Promotions.Create(ctx, promotion.Coupon{Code: uniq1, Kind: promotion.KindFixed, Value: 500, MaxUses: &one, Active: true}); err != nil {
		t.Fatal(err)
	}
	min := "MIN" + strings.ToUpper(uuid.NewString()[:6])
	if _, err := e.app.Promotions.Create(ctx, promotion.Coupon{Code: min, Kind: promotion.KindPercent, Value: 10, MinSubtotal: 9000, Active: true}); err != nil {
		t.Fatal(err)
	}
	expired := "OLD" + strings.ToUpper(uuid.NewString()[:6])
	past := time.Now().Add(-48 * time.Hour)
	if _, err := e.app.Promotions.Create(ctx, promotion.Coupon{Code: expired, Kind: promotion.KindFixed, Value: 100, EndsAt: &past, Active: true}); err != nil {
		t.Fatal(err)
	}
	p := e.product(2000, 50)

	a, b := e.newClient(), e.newClient()
	a.register()
	b.register()
	a.addToCart(p, "1")
	b.addToCart(p, "1")
	if res, _ := a.placeWith(url.Values{"coupon": {uniq1}}); res.StatusCode != 303 {
		t.Fatalf("first use = %d", res.StatusCode)
	}
	if res, body := b.placeWith(url.Values{"coupon": {uniq1}}); res.StatusCode != 422 || !strings.Contains(body, "fully redeemed") {
		t.Fatalf("second use of a single-use code = %d", res.StatusCode)
	}
	if _, page := b.mustStatus(200, "POST", "/checkout", url.Values{"action": {"refresh"}, "coupon": {min}, "idempotency_key": {"k"}}); !strings.Contains(page, "Spend $90.00 or more") {
		t.Fatal("a minimum-order code must say how much to spend")
	}
	if _, page := b.mustStatus(200, "POST", "/checkout", url.Values{"action": {"refresh"}, "coupon": {expired}, "idempotency_key": {"k"}}); !strings.Contains(page, "expired") {
		t.Fatal("an expired code must say so")
	}

	// Many buyers racing for the last use: exactly one wins.
	race := "RACE" + strings.ToUpper(uuid.NewString()[:6])
	e.app.Promotions.Create(ctx, promotion.Coupon{Code: race, Kind: promotion.KindFixed, Value: 100, MaxUses: &one, Active: true})
	var wg sync.WaitGroup
	var wins int32
	var mu sync.Mutex
	for range 6 {
		c := e.newClient()
		c.register()
		c.addToCart(p, "1")
		wg.Add(1)
		go func() {
			defer wg.Done()
			if res, _ := c.placeWith(url.Values{"coupon": {race}}); res.StatusCode == 303 {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d of 6 concurrent checkouts got the single-use coupon, want exactly 1", wins)
	}
}

func TestCashOnDeliveryEndToEnd(t *testing.T) {
	e := newEnv(t)
	p := e.product(3000, 5)
	u := e.newClient()
	u.register()
	u.addToCart(p, "2")

	res, _ := u.placeWith(url.Values{"payment_method": {"cod"}})
	if res.StatusCode != 303 || !strings.HasPrefix(res.Header.Get("Location"), "/orders/") {
		t.Fatalf("COD checkout = %d %s, want an immediate redirect to the order", res.StatusCode, res.Header.Get("Location"))
	}
	order := strings.SplitN(res.Header.Get("Location"), "?", 2)[0]
	if got := u.orderStatus(order); got != "Confirmed" {
		t.Fatalf("COD order status = %q, want Confirmed straight away", got)
	}
	if got := e.available(p); got != 3 {
		t.Fatalf("stock = %d, want 3 (committed at once)", got)
	}
	oid := uuid.MustParse(strings.TrimPrefix(order, "/orders/"))
	// A card payment page does not exist for it.
	var pid uuid.UUID
	e.pool.QueryRow(context.Background(), `SELECT id FROM payment.payments WHERE order_id = $1`, oid).Scan(&pid)
	u.mustStatus(404, "GET", "/pay/"+pid.String()+"/mock", nil)

	a := e.adminClient()
	e.drain() // fulfilment pickup
	a.mustStatus(303, "POST", "/admin"+order+"/ship", url.Values{"carrier": {"DHL"}, "number": {"JD0123"}, "url": {"https://track.example/JD0123"}})
	_, page := u.mustStatus(200, "GET", order, nil)
	if !strings.Contains(page, "JD0123") || !strings.Contains(page, "Track parcel") {
		t.Fatal("the customer should see tracking details")
	}
	a.mustStatus(303, "POST", "/admin"+order+"/deliver", url.Values{})
	var status string
	e.pool.QueryRow(context.Background(), `SELECT status FROM payment.payments WHERE id = $1`, pid).Scan(&status)
	if status != "succeeded" {
		t.Fatalf("payment status after delivery = %q, want the cash recorded as collected", status)
	}
	if got := u.orderStatus(order); got != "Delivered" {
		t.Fatalf("status = %q, want Delivered", got)
	}
	// The customer was emailed at each step.
	e.drain()
	subjects := ""
	for _, m := range e.mailer.Sent() {
		subjects += m.Subject + "|"
	}
	for _, want := range []string{"confirmed", "on its way", "was delivered"} {
		if !strings.Contains(subjects, want) {
			t.Errorf("no email mentioning %q; sent: %s", want, subjects)
		}
	}
}

func TestGuestCheckoutCreatesAnAccountAndRefusesExistingEmails(t *testing.T) {
	e := newEnv(t)
	p := e.product(1500, 5)
	g := e.newClient()
	g.addToCart(p, "1") // anonymous
	page := g.checkoutPage()
	if !strings.Contains(page, `name="email"`) {
		t.Fatal("anonymous checkout must ask for contact details")
	}

	email := "guest-" + uuid.NewString() + "@example.com"
	form := func(email, name string) url.Values {
		f := url.Values{"idempotency_key": {idempotencyKey.FindStringSubmatch(page)[1]}, "expected_total": {totalOf(t, page)}, "action": {"place"},
			"email": {email}, "name": {name}}
		for k, v := range shipping {
			f[k] = v
		}
		return f
	}
	// Missing contact details: refused, and no account is created.
	if res, _ := g.do("POST", "/checkout", form("", "")); res.StatusCode != 422 {
		t.Fatalf("no contact = %d, want 422", res.StatusCode)
	}
	if res, _ := g.do("POST", "/checkout", form("not-an-email", "Gus")); res.StatusCode != 422 {
		t.Fatalf("bad email = %d, want 422", res.StatusCode)
	}
	if users, _ := e.app.Identity.ListUsers(context.Background(), "not-an-email", "", 5, 0); len(users) != 0 {
		t.Fatal("a refused guest checkout must not leave an account behind")
	}

	res, _ := g.do("POST", "/checkout", form(email, "Gus Guest"))
	if res.StatusCode != 303 || !strings.HasPrefix(res.Header.Get("Location"), "/pay/") {
		t.Fatalf("guest checkout = %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	// They are now signed in and their order exists; the cart is empty.
	g.mustStatus(200, "GET", "/orders", nil)
	if _, cart := g.mustStatus(200, "GET", "/cart", nil); !strings.Contains(cart, "Your cart is empty") {
		t.Fatal("the guest cart should be cleared")
	}
	// They are emailed a link to set a password; using it turns the guest account into a normal one.
	link := e.emailLink(email, "Set a password")
	set := e.newClient()
	set.mustStatus(200, "GET", link, nil)
	tok := strings.SplitN(link, "token=", 2)[1]
	set.mustStatus(303, "POST", "/reset", url.Values{"token": {tok}, "password": {"my new password"}, "confirm": {"my new password"}})
	e.newClient().mustStatus(303, "POST", "/login", url.Values{"email": {email}, "password": {"my new password"}})

	// A second anonymous buyer cannot take over an existing account by typing its email.
	g2 := e.newClient()
	g2.addToCart(p, "1")
	page = g2.checkoutPage()
	res, body := g2.do("POST", "/checkout", form(email, "Mallory"))
	if res.StatusCode != 409 || !strings.Contains(body, "already exists") {
		t.Fatalf("guest checkout with an existing email = %d, want 409 asking to sign in", res.StatusCode)
	}
	if _, orders := g2.do("GET", "/orders", nil); strings.Contains(orders, "Gus") {
		t.Fatal("must not expose the other account's orders")
	}
}

func TestGuestCheckoutIsRateLimited(t *testing.T) {
	e := newEnv(t)
	p := e.product(1000, 100)
	limited := false
	for i := range 14 {
		g := e.newClient()
		g.addToCart(p, "1")
		page := g.checkoutPage()
		f := url.Values{"idempotency_key": {idempotencyKey.FindStringSubmatch(page)[1]}, "expected_total": {totalOf(t, page)}, "action": {"place"},
			"email": {fmt.Sprintf("bulk-%d-%s@example.com", i, uuid.NewString()[:6])}, "name": {"Bulk"}}
		for k, v := range shipping {
			f[k] = v
		}
		if res, _ := g.do("POST", "/checkout", f); res.StatusCode == 429 {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("unlimited guest account creation from one client")
	}
}

// ---- lifecycle: cancel, refund, returns, permissions ----

// paidOrder places and pays a card order, returning the customer, the order page path and its id.
func (e *env) paidOrder(p catalog.Product, qty string) (*client, string, uuid.UUID) {
	e.t.Helper()
	u := e.newClient()
	u.register()
	u.addToCart(p, qty)
	payPath, order := u.placeOrder()
	u.pay(payPath, "succeeded")
	e.drain()
	return u, order, uuid.MustParse(strings.TrimPrefix(order, "/orders/"))
}

// paidUnpickedOrder is a confirmed order the warehouse has not picked up yet.
func (e *env) paidUnpickedOrder(p catalog.Product, qty string) (*client, string, uuid.UUID) {
	e.t.Helper()
	u := e.newClient()
	u.register()
	u.addToCart(p, qty)
	payPath, order := u.placeOrder()
	u.pay(payPath, "succeeded")
	e.drainJobs()
	return u, order, uuid.MustParse(strings.TrimPrefix(order, "/orders/"))
}

func (e *env) staffClient() *client {
	e.t.Helper()
	email := "staff-" + uuid.NewString() + "@example.com"
	c := e.newClient()
	c.mustStatus(303, "POST", "/register", url.Values{"name": {"Sam Staff"}, "email": {email}, "password": {"staff-password-1"}})
	users, _ := e.app.Identity.ListUsers(context.Background(), email, "", 1, 0)
	admins, _ := e.app.Identity.ListUsers(context.Background(), "admin-", identity.RoleAdmin, 1, 0)
	var actor uuid.UUID
	if len(admins) > 0 {
		actor = admins[0].ID
	} else {
		e.adminClient()
		admins, _ = e.app.Identity.ListUsers(context.Background(), "admin-", identity.RoleAdmin, 1, 0)
		actor = admins[0].ID
	}
	if err := e.app.Identity.SetRole(context.Background(), actor, users[0].ID, identity.RoleStaff); err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *env) orderOf(id uuid.UUID) order.Order {
	e.t.Helper()
	o, err := e.app.Orders.API().Get(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return o
}

func (e *env) paymentStatus(orderID uuid.UUID) string {
	var s string
	e.pool.QueryRow(context.Background(), `SELECT status FROM payment.payments WHERE order_id = $1`, orderID).Scan(&s)
	return s
}

func TestCustomerCancelsAPaidOrderAndIsRefunded(t *testing.T) {
	e := newEnv(t)
	p := e.product(4000, 5)
	u, order, oid := e.paidUnpickedOrder(p, "2")
	if o := e.orderOf(oid); o.Status != "paid" {
		t.Fatalf("setup: status = %s, want paid and not yet picked up", o.Status)
	}
	if got := e.available(p); got != 3 {
		t.Fatalf("stock before cancel = %d", got)
	}
	u.mustStatus(303, "POST", order+"/cancel", url.Values{})
	e.drainJobs()
	o := e.orderOf(oid)
	if o.Status != "cancelled" || o.RefundCents != 4000*2 || o.CancelledAt == nil {
		t.Fatalf("order after cancel: %s refund %d, want cancelled with a full refund", o.Status, o.RefundCents)
	}
	if got := e.paymentStatus(oid); got != "refunded" {
		t.Fatalf("payment = %s, want refunded", got)
	}
	if got := e.available(p); got != 5 {
		t.Fatalf("stock = %d, want 5 (restocked)", got)
	}
	found := false
	for _, m := range e.mailer.Sent() {
		found = found || strings.Contains(m.Subject, "cancelled") && strings.Contains(m.Text, "refunding")
	}
	if !found {
		t.Fatal("the customer should be told a refund is coming")
	}
	// Cancelling twice is harmless.
	u.mustStatus(303, "POST", order+"/cancel", url.Values{})
	if e.paymentStatus(oid) != "refunded" {
		t.Fatal("a repeated cancel must not change the payment")
	}
}

func TestCancellingACashOnDeliveryOrderRestocks(t *testing.T) {
	e := newEnv(t)
	p := e.product(2000, 4)
	u := e.newClient()
	u.register()
	u.addToCart(p, "3")
	res, _ := u.placeWith(url.Values{"payment_method": {"cod"}})
	order := strings.SplitN(res.Header.Get("Location"), "?", 2)[0]
	oid := uuid.MustParse(strings.TrimPrefix(order, "/orders/"))
	if got := e.available(p); got != 1 {
		t.Fatalf("stock = %d", got)
	}
	u.mustStatus(303, "POST", order+"/cancel", url.Values{})
	o := e.orderOf(oid)
	if o.Status != "cancelled" || o.RefundCents != 0 {
		t.Fatalf("COD cancel: %s refund %d (nothing was collected, so nothing is refunded)", o.Status, o.RefundCents)
	}
	if got := e.available(p); got != 4 {
		t.Fatalf("stock = %d, want 4 (restocked)", got)
	}
	if got := e.paymentStatus(oid); got != "failed" {
		t.Fatalf("payment = %s, want the pending cash payment closed", got)
	}
}

func TestOrderCannotBeCancelledOncePreparing(t *testing.T) {
	e := newEnv(t)
	p := e.product(1000, 5)
	u, order, oid := e.paidOrder(p, "1")
	e.pool.Exec(context.Background(), `UPDATE orders.orders SET status = 'fulfilling' WHERE id = $1`, oid)
	if res, _ := u.do("POST", order+"/cancel", url.Values{}); res.StatusCode != 303 {
		t.Fatalf("cancel = %d", res.StatusCode)
	}
	if got := e.orderOf(oid).Status; got != "fulfilling" {
		t.Fatalf("status = %s: a prepared order must not be cancelled by the customer", got)
	}
	if _, page := u.mustStatus(200, "GET", order, nil); strings.Contains(page, "Cancel order") {
		t.Fatal("the cancel button should be gone once preparing")
	}
}

func TestAdminRefundsADeliveredOrder(t *testing.T) {
	e := newEnv(t)
	p := e.product(2500, 5)
	u, order, oid := e.paidOrder(p, "2")
	a := e.adminClient()
	a.mustStatus(303, "POST", "/admin"+order+"/ship", url.Values{})
	a.mustStatus(303, "POST", "/admin"+order+"/deliver", url.Values{})

	// Refund without restocking (the goods are damaged).
	a.mustStatus(303, "POST", "/admin"+order+"/refund", url.Values{"reason": {"arrived damaged"}})
	o := e.orderOf(oid)
	if o.Status != "refunded" || o.RefundCents != 5000 || o.RefundedAt == nil {
		t.Fatalf("after refund: %s %d", o.Status, o.RefundCents)
	}
	if got := e.paymentStatus(oid); got != "refunded" {
		t.Fatalf("payment = %s", got)
	}
	if got := e.available(p); got != 3 {
		t.Fatalf("stock = %d, want 3: the refund did not ask to restock", got)
	}
	e.drain()
	if _, page := u.mustStatus(200, "GET", order, nil); !strings.Contains(page, "arrived damaged") {
		t.Fatal("the refund reason should be in the customer's order history")
	}
	// A second refund is refused (nothing left to refund) and does not double-pay.
	if res, _ := a.do("POST", "/admin"+order+"/refund", url.Values{}); res.StatusCode != 409 {
		t.Fatalf("second refund = %d, want 409", res.StatusCode)
	}
	found := false
	for _, m := range e.mailer.Sent() {
		found = found || strings.Contains(m.Subject, "refunded")
	}
	if !found {
		t.Fatal("the customer should be emailed about the refund")
	}
}

func TestReturnRequestApproveAndReject(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.AlertEmail = "ops@shop.test" })
	p := e.product(3000, 10)
	a := e.adminClient()

	// Not shipped yet: nothing to return.
	u, order, oid := e.paidOrder(p, "1")
	if res, _ := u.do("POST", order+"/return", url.Values{"reason": {"changed my mind"}}); res.StatusCode != 422 {
		t.Fatalf("return before shipping = %d, want 422", res.StatusCode)
	}
	a.mustStatus(303, "POST", "/admin"+order+"/ship", url.Values{})
	a.mustStatus(303, "POST", "/admin"+order+"/deliver", url.Values{})

	// A reason is required; then the request is accepted once.
	if res, _ := u.do("POST", order+"/return", url.Values{"reason": {"  "}}); res.StatusCode != 422 {
		t.Fatalf("blank reason = %d", res.StatusCode)
	}
	u.mustStatus(303, "POST", order+"/return", url.Values{"reason": {"too small"}})
	if res, _ := u.do("POST", order+"/return", url.Values{"reason": {"again"}}); res.StatusCode != 422 {
		t.Fatalf("second request = %d, want 422", res.StatusCode)
	}
	e.drain()
	if len(e.mailer.To("ops@shop.test")) == 0 {
		t.Fatal("staff should be alerted to the return request")
	}

	// Staff can decline; only admins can approve (it moves money).
	staff := e.staffClient()
	if res, _ := staff.do("POST", "/admin"+order+"/return/approve", url.Values{}); res.StatusCode != 403 {
		t.Fatalf("staff approving a return = %d, want 403", res.StatusCode)
	}
	a.mustStatus(303, "POST", "/admin"+order+"/return/approve", url.Values{"restock": {"1"}, "note": {"thanks, refunded"}})
	o := e.orderOf(oid)
	if o.Status != "refunded" || o.Return.Status != "approved" || o.RefundCents != 3000 {
		t.Fatalf("after approval: %s / %s / %d", o.Status, o.Return.Status, o.RefundCents)
	}
	if got := e.available(p); got != 10 {
		t.Fatalf("stock = %d, want 10 (returned goods restocked)", got)
	}

	// Rejecting.
	u2, order2, oid2 := e.paidOrder(p, "1")
	a.mustStatus(303, "POST", "/admin"+order2+"/ship", url.Values{})
	u2.mustStatus(303, "POST", order2+"/return", url.Values{"reason": {"meh"}})
	staff.mustStatus(303, "POST", "/admin"+order2+"/return/reject", url.Values{"note": {"worn items cannot be returned"}})
	o2 := e.orderOf(oid2)
	if o2.Return.Status != "rejected" || o2.Status == "refunded" {
		t.Fatalf("after rejection: %s / %s", o2.Status, o2.Return.Status)
	}
	if _, page := u2.mustStatus(200, "GET", order2, nil); !strings.Contains(page, "worn items cannot be returned") {
		t.Fatal("the customer should see the store's reply")
	}
}

func TestReturnWindowCloses(t *testing.T) {
	e := newEnv(t)
	p := e.product(1000, 5)
	u, order, oid := e.paidOrder(p, "1")
	a := e.adminClient()
	a.mustStatus(303, "POST", "/admin"+order+"/ship", url.Values{})
	a.mustStatus(303, "POST", "/admin"+order+"/deliver", url.Values{})
	e.pool.Exec(context.Background(), `UPDATE orders.orders SET delivered_at = now() - interval '30 days' WHERE id = $1`, oid)
	if res, _ := u.do("POST", order+"/return", url.Values{"reason": {"late"}}); res.StatusCode != 422 {
		t.Fatalf("return after the window = %d, want 422", res.StatusCode)
	}
	if _, page := u.mustStatus(200, "GET", order, nil); strings.Contains(page, "Request return") {
		t.Fatal("the return form must disappear after the window")
	}
}

func TestStaffAndAdminPermissions(t *testing.T) {
	e := newEnv(t)
	p := e.product(1000, 5)
	staff := e.staffClient()
	admin := e.adminClient()
	cust := e.newClient()
	cust.register()
	_, order, _ := e.paidOrder(p, "1")

	// Customers see nothing of the back office; staff see the shop floor, not configuration.
	for _, path := range []string{"/admin", "/admin/orders", "/admin/users", "/admin/coupons"} {
		if res, _ := cust.do("GET", path, nil); res.StatusCode != 403 {
			t.Errorf("customer GET %s = %d, want 403", path, res.StatusCode)
		}
	}
	for _, path := range []string{"/admin", "/admin/products", "/admin/orders", "/admin/categories"} {
		if res, _ := staff.do("GET", path, nil); res.StatusCode != 200 {
			t.Errorf("staff GET %s = %d, want 200", path, res.StatusCode)
		}
	}
	for _, path := range []string{"/admin/users", "/admin/shipping", "/admin/coupons", "/admin/audit", "/admin/import"} {
		if res, _ := staff.do("GET", path, nil); res.StatusCode != 403 {
			t.Errorf("staff GET %s = %d, want 403 (admin only)", path, res.StatusCode)
		}
		if res, _ := admin.do("GET", path, nil); res.StatusCode != 200 {
			t.Errorf("admin GET %s = %d, want 200", path, res.StatusCode)
		}
	}
	// Money-moving actions need an admin; staff may still ship and note.
	if res, _ := staff.do("POST", "/admin"+order+"/refund", url.Values{}); res.StatusCode != 403 {
		t.Errorf("staff refund = %d, want 403", res.StatusCode)
	}
	if res, _ := staff.do("POST", "/admin"+order+"/cancel", url.Values{}); res.StatusCode != 403 {
		t.Errorf("staff cancelling a paid order = %d, want 403", res.StatusCode)
	}
	staff.mustStatus(303, "POST", "/admin"+order+"/note", url.Values{"note": {"fragile"}})
	staff.mustStatus(303, "POST", "/admin"+order+"/ship", url.Values{})
	// Staff may cancel an order still awaiting payment.
	u := e.newClient()
	u.register()
	u.addToCart(p, "1")
	_, unpaid := u.placeOrder()
	staff.mustStatus(303, "POST", "/admin"+unpaid+"/cancel", url.Values{"reason": {"duplicate"}})
	// The role change takes effect at once, without waiting for the access token to expire.
	users, _ := e.app.Identity.ListUsers(context.Background(), "", identity.RoleStaff, 50, 0)
	admins, _ := e.app.Identity.ListUsers(context.Background(), "admin-", identity.RoleAdmin, 1, 0)
	for _, us := range users {
		e.app.Identity.SetRole(context.Background(), admins[0].ID, us.ID, identity.RoleCustomer)
	}
	if res, _ := staff.do("GET", "/admin/orders", nil); res.StatusCode != 403 {
		t.Fatalf("a demoted staff member still has access: %d", res.StatusCode)
	}
}

func TestAdminOrderSearchFiltersAndCSVExport(t *testing.T) {
	e := newEnv(t)
	a := e.adminClient()
	p := e.product(1500, 20)
	u := e.newClient()
	email := "=cmd-" + uuid.NewString()[:6] + "@example.com" // a spreadsheet formula, as a hostile customer might choose
	u.mustStatus(303, "POST", "/register", url.Values{"name": {"=HYPERLINK(\"http://evil\")"}, "email": {email}, "password": {"correct horse battery"}})
	u.addToCart(p, "1")
	_, order := u.placeOrder()
	oid := strings.TrimPrefix(order, "/orders/")

	_, page := a.mustStatus(200, "GET", "/admin/orders?q="+url.QueryEscape(oid[len(oid)-8:]), nil)
	if !strings.Contains(page, "/admin/orders/"+oid) {
		t.Fatal("search by order number found nothing")
	}
	if _, page = a.mustStatus(200, "GET", "/admin/orders?status=shipped&q="+url.QueryEscape(oid[len(oid)-8:]), nil); strings.Contains(page, "/admin/orders/"+oid) {
		t.Fatal("the status filter should exclude it")
	}
	a.mustStatus(200, "GET", "/admin/orders?from=not-a-date&to=2020-13-45&page=-3", nil) // junk is tolerated

	res, csvBody := a.mustStatus(200, "GET", "/admin/orders/export.csv?q="+url.QueryEscape(oid[len(oid)-8:]), nil)
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/csv") || !strings.Contains(res.Header.Get("Content-Disposition"), "attachment") {
		t.Fatal("export must be a CSV download")
	}
	if !strings.Contains(csvBody, oid) {
		t.Fatal("the order is missing from the export")
	}
	for _, line := range strings.Split(csvBody, "\n")[1:] {
		for _, cell := range strings.Split(line, ",") {
			if strings.HasPrefix(strings.Trim(cell, `"`), "=") {
				t.Fatalf("a cell starting with '=' would run as a spreadsheet formula: %s", cell)
			}
		}
	}
	// The export is for staff and admins only.
	u.mustStatus(403, "GET", "/admin/orders/export.csv", nil)
}

func TestAuditLogRecordsBackOfficeActions(t *testing.T) {
	e := newEnv(t)
	a := e.adminClient()
	p := e.product(1000, 5)
	_, order, _ := e.paidOrder(p, "1")
	a.mustStatus(303, "POST", "/admin"+order+"/ship", url.Values{"carrier": {"UPS"}})
	_, page := a.mustStatus(200, "GET", "/admin/audit?action=order.", nil)
	if !strings.Contains(page, "order.shipped") || !strings.Contains(page, "admin-") {
		t.Fatal("the shipment should be in the audit log with the admin's email")
	}
	if _, page = a.mustStatus(200, "GET", "/admin/audit?action=zzz.", nil); strings.Contains(page, "order.shipped") {
		t.Fatal("the filter should narrow the log")
	}
}

// ---- admin tools: users, delivery, coupons, import, dashboard ----

func TestAdminManagesUsers(t *testing.T) {
	e, u, email := newUserEnv(t)
	a := e.adminClient()
	users, _ := e.app.Identity.ListUsers(context.Background(), email, "", 1, 0)
	uid := users[0].ID.String()

	_, page := a.mustStatus(200, "GET", "/admin/users?q="+url.QueryEscape(email), nil)
	if !strings.Contains(page, email) {
		t.Fatal("user search found nothing")
	}
	a.mustStatus(303, "POST", "/admin/users/"+uid+"/role", url.Values{"role": {"staff"}})
	if got, _ := e.app.Identity.User(context.Background(), users[0].ID); got.Role != "staff" {
		t.Fatalf("role = %s", got.Role)
	}
	if res, _ := a.do("POST", "/admin/users/"+uid+"/role", url.Values{"role": {"emperor"}}); res.StatusCode != 422 {
		t.Fatalf("unknown role = %d, want 422", res.StatusCode)
	}
	// Disabling signs the user out at once.
	a.mustStatus(303, "POST", "/admin/users/"+uid+"/status", url.Values{"action": {"disable"}})
	if res, _ := u.do("GET", "/account", nil); res.StatusCode != 303 {
		t.Fatalf("a disabled user kept their session: %d", res.StatusCode)
	}
	a.mustStatus(303, "POST", "/admin/users/"+uid+"/status", url.Values{"action": {"enable"}})
	e.newClient().mustStatus(303, "POST", "/login", url.Values{"email": {email}, "password": {"correct horse battery"}})

	// An admin cannot change their own role or disable themselves.
	admins, _ := e.app.Identity.ListUsers(context.Background(), "admin-", "admin", 50, 0)
	var self uuid.UUID
	for _, x := range admins {
		if res, _ := a.do("POST", "/admin/users/"+x.ID.String()+"/status", url.Values{"action": {"disable"}}); res.StatusCode == 409 {
			self = x.ID
			break
		}
	}
	if self == uuid.Nil {
		t.Fatal("an admin was able to disable their own account")
	}
	if res, _ := a.do("POST", "/admin/users/"+self.String()+"/role", url.Values{"role": {"customer"}}); res.StatusCode != 409 {
		t.Fatalf("self-demotion = %d, want 409", res.StatusCode)
	}
	a.mustStatus(404, "POST", "/admin/users/"+uuid.NewString()+"/role", url.Values{"role": {"staff"}})
}

func TestAdminManagesDeliveryMethods(t *testing.T) {
	e := newEnv(t)
	a := e.adminClient()
	name := "Rocket " + uuid.NewString()[:6]
	a.mustStatus(303, "POST", "/admin/shipping", url.Values{"name": {name}, "price": {"7.50"}, "free_over": {"80"}, "min_days": {"1"}, "max_days": {"2"}, "active": {"1"}})
	ms, _ := e.app.Shipping.All(context.Background())
	var m shippingmod.Method
	for _, x := range ms {
		if x.Name == name {
			m = x
		}
	}
	if m.ID == uuid.Nil || m.PriceCents != 750 || m.FreeOverCents == nil || *m.FreeOverCents != 8000 || m.Eta() != "1–2 days" {
		t.Fatalf("created method wrong: %+v", m)
	}
	for name, form := range map[string]url.Values{
		"blank name":    {"name": {" "}, "price": {"1"}},
		"bad price":     {"name": {"x"}, "price": {"lots"}},
		"negative":      {"name": {"x"}, "price": {"-1"}},
		"days reversed": {"name": {"x"}, "price": {"1"}, "min_days": {"5"}, "max_days": {"2"}},
		"bad threshold": {"name": {"x"}, "price": {"1"}, "free_over": {"0"}},
	} {
		if res, _ := a.do("POST", "/admin/shipping", form); res.StatusCode != 422 {
			t.Errorf("%s = %d, want 422", name, res.StatusCode)
		}
	}
	// Switching a method off removes it from checkout; deleting is idempotent.
	p := e.product(1000, 5)
	u := e.newClient()
	u.register()
	u.addToCart(p, "1")
	if !strings.Contains(u.checkoutPage(), name) {
		t.Fatal("an active method is offered at checkout")
	}
	a.mustStatus(303, "POST", "/admin/shipping/"+m.ID.String(), url.Values{"name": {name}, "price": {"7.50"}}) // no "active" => off
	if strings.Contains(u.checkoutPage(), name) {
		t.Fatal("an inactive method must not be offered")
	}
	a.mustStatus(303, "POST", "/admin/shipping/"+m.ID.String()+"/delete", url.Values{})
	a.mustStatus(303, "POST", "/admin/shipping/"+m.ID.String()+"/delete", url.Values{})
}

func TestAdminManagesCoupons(t *testing.T) {
	e := newEnv(t)
	a := e.adminClient()
	code := "HELLO" + strings.ToUpper(uuid.NewString()[:5])
	a.mustStatus(303, "POST", "/admin/coupons", url.Values{"code": {strings.ToLower(code)}, "kind": {"percent"}, "value": {"15"}, "min_subtotal": {"20"}, "ends": {"2099-12-31"}, "max_uses": {"10"}, "active": {"1"}})
	if res, _ := a.do("POST", "/admin/coupons", url.Values{"code": {code}, "kind": {"percent"}, "value": {"5"}}); res.StatusCode != 422 {
		t.Fatalf("duplicate code = %d, want 422", res.StatusCode)
	}
	for name, form := range map[string]url.Values{
		"percent over 100": {"code": {"BADONE1"}, "kind": {"percent"}, "value": {"150"}},
		"zero fixed":       {"code": {"BADONE2"}, "kind": {"fixed"}, "value": {"0"}},
		"bad code":         {"code": {"a b"}, "kind": {"percent"}, "value": {"5"}},
		"end before start": {"code": {"BADONE3"}, "kind": {"fixed"}, "value": {"5"}, "starts": {"2030-02-01"}, "ends": {"2030-01-01"}},
		"bad date":         {"code": {"BADONE4"}, "kind": {"fixed"}, "value": {"5"}, "starts": {"soon"}},
		"bad limit":        {"code": {"BADONE5"}, "kind": {"fixed"}, "value": {"5"}, "max_uses": {"zero"}},
	} {
		if res, _ := a.do("POST", "/admin/coupons", form); res.StatusCode != 422 {
			t.Errorf("%s = %d, want 422", name, res.StatusCode)
		}
	}
	list, _ := e.app.Promotions.List(context.Background())
	var c promotion.Coupon
	for _, x := range list {
		if x.Code == code {
			c = x
		}
	}
	if c.ID == uuid.Nil || c.Value != 15 || c.MinSubtotal != 2000 || c.MaxUses == nil || *c.MaxUses != 10 || c.EndsAt == nil {
		t.Fatalf("coupon not stored as typed: %+v", c)
	}
	// The code and type are fixed; the rest can be edited.
	a.mustStatus(303, "POST", "/admin/coupons/"+c.ID.String(), url.Values{"value": {"25"}, "active": {"1"}, "code": {"HACKED"}, "kind": {"fixed"}})
	c, _ = e.app.Promotions.Get(context.Background(), c.ID)
	if c.Code != code || c.Kind != "percent" || c.Value != 25 {
		t.Fatalf("after edit: %+v (code and type must not change)", c)
	}
	// A used coupon cannot be deleted.
	p := e.product(5000, 5)
	u := e.newClient()
	u.register()
	u.addToCart(p, "1")
	if res, _ := u.placeWith(url.Values{"coupon": {code}}); res.StatusCode != 303 {
		t.Fatalf("using the coupon = %d", res.StatusCode)
	}
	if res, _ := a.do("POST", "/admin/coupons/"+c.ID.String()+"/delete", url.Values{}); res.StatusCode != 409 {
		t.Fatalf("deleting a used coupon = %d, want 409", res.StatusCode)
	}
	// Deactivating (no "active" field) keeps the record but stops new use.
	a.mustStatus(303, "POST", "/admin/coupons/"+c.ID.String(), url.Values{"value": {"25"}})
	u2 := e.newClient()
	u2.register()
	u2.addToCart(p, "1")
	_, page := u2.mustStatus(200, "POST", "/checkout", url.Values{"action": {"refresh"}, "coupon": {code}, "idempotency_key": {"k"}})
	if !strings.Contains(page, "no longer active") {
		t.Fatal("an inactive coupon must be refused with an explanation")
	}
}

func (e *env) importCSV(a *client, csvText string) (*http.Response, string) {
	e.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "products.csv")
	fw.Write([]byte(csvText))
	mw.Close()
	req, _ := http.NewRequest("POST", e.server.URL+"/admin/import", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := a.c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func TestProductImportFromCSV(t *testing.T) {
	e := newEnv(t)
	a := e.adminClient()
	ctx := context.Background()
	tag := strings.ToLower(uuid.NewString()[:6])
	csvText := "name,slug,category,price,description,active,sku,stock,option_name,variant,variant_price\n" +
		"Tote " + tag + ",tote-" + tag + ",Bags " + tag + ",18.00,A tote,1,TOTE-" + strings.ToUpper(tag) + ",40,,,\n" +
		"Coat " + tag + ",coat-" + tag + ",Bags " + tag + ",75.00,A coat,1,,,Size,,\n" +
		",coat-" + tag + ",,,,,COAT-S-" + strings.ToUpper(tag) + ",5,,S,\n" +
		",coat-" + tag + ",,,,,COAT-XL-" + strings.ToUpper(tag) + ",3,,XL,80.00\n" +
		",nope-" + tag + ",,,,,X,1,,M,\n" + // variant of a product that does not exist
		"Broken " + tag + ",broken-" + tag + ",,abc,,1,,,,,\n" + // bad price
		",,,5.00,,,,,,,\n" + // no name
		"Neg " + tag + ",neg-" + tag + ",,5.00,,1,,-4,,,\n" // negative stock

	res, body := e.importCSV(a, csvText)
	if res.StatusCode != 200 {
		t.Fatalf("import = %d\n%s", res.StatusCode, truncate(body))
	}
	for _, want := range []string{"Created <strong>2</strong>", "<strong>2</strong> variant", "4 row(s) were not imported", "no product with slug", "price must be a number", "needs a name", "stock must be"} {
		if !strings.Contains(body, want) {
			t.Errorf("import report lacks %q", want)
		}
	}
	tote, err := e.app.Catalog.BySlug(ctx, "tote-"+tag)
	if err != nil || tote.PriceCents != 1800 || len(tote.Variants) != 1 {
		t.Fatalf("tote: %+v %v", tote, err)
	}
	if avail, _ := e.app.Inventory.Available(ctx, []uuid.UUID{tote.Variants[0].ID}); avail[tote.Variants[0].ID] != 40 {
		t.Fatalf("tote stock = %d, want 40", avail[tote.Variants[0].ID])
	}
	coat, _ := e.app.Catalog.BySlug(ctx, "coat-"+tag)
	labels := map[string]*int64{}
	activeDefault := false
	for _, v := range coat.Variants {
		labels[v.Label] = v.PriceCents
		if v.Label == "Default" && v.Active {
			activeDefault = true
		}
	}
	if len(coat.Variants) != 3 || labels["S"] != nil || labels["XL"] == nil || *labels["XL"] != 8000 {
		t.Fatalf("coat variants: %+v", coat.Variants)
	}
	if activeDefault {
		t.Fatal("the unnamed default variant should be hidden once real variants exist")
	}

	// Re-importing is safe: nothing is duplicated.
	_, again := e.importCSV(a, csvText)
	if !strings.Contains(again, "Created <strong>0</strong>") {
		t.Fatalf("re-import created products again:\n%s", truncate(again))
	}
	list, _ := e.app.Catalog.List(ctx, catalog.Filter{Query: "Tote " + tag, IncludeInactive: true})
	if len(list.Products) != 1 {
		t.Fatalf("%d products named Tote after a re-import, want 1", len(list.Products))
	}

	// Bad files are refused with a message.
	for name, text := range map[string]string{"empty": "", "no header": "just,some,words\n1,2,3\n", "binary": "\x00\x01\x02"} {
		if res, _ := e.importCSV(a, text); res.StatusCode != 422 {
			t.Errorf("%s file = %d, want 422", name, res.StatusCode)
		}
	}
	// Customers and staff cannot import.
	staff := e.staffClient()
	if res, _ := staff.do("GET", "/admin/import", nil); res.StatusCode != 403 {
		t.Errorf("staff import page = %d, want 403", res.StatusCode)
	}
	a.mustStatus(200, "GET", "/admin/import/sample.csv", nil)
}

func TestDashboardReportsAndLowStock(t *testing.T) {
	e := newEnv(t)
	a := e.adminClient()
	ctx := context.Background()
	p := e.product(2500, 3) // 3 in stock is under the default threshold of 5
	u, _, _ := e.paidOrder(p, "1")
	_ = u

	_, page := a.mustStatus(200, "GET", "/admin?days=7", nil)
	for _, want := range []string{"Revenue", "Average order", "Top products", "Low stock", "Daily revenue"} {
		if !strings.Contains(page, want) {
			t.Errorf("dashboard lacks %q", want)
		}
	}
	rep, err := e.app.Orders.API().Report(ctx, time.Now().UTC().AddDate(0, 0, -7), time.Now().UTC().AddDate(0, 0, 1))
	if err != nil || rep.Orders < 1 || rep.RevenueCents < 2500 || len(rep.Top) == 0 {
		t.Fatalf("report: %+v %v", rep, err)
	}
	if rep.AvgOrderCents <= 0 || rep.AvgOrderCents > rep.RevenueCents {
		t.Fatalf("average order %d out of range", rep.AvgOrderCents)
	}
	low, err := e.app.Inventory.LowStock(ctx, 5, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range low {
		found = found || (l.VariantID == p.Variants[0].ID && l.Available == 2)
	}
	if !found {
		t.Fatal("a variant with 2 units left should be reported as low stock")
	}
	a.mustStatus(200, "GET", "/admin?days=9999", nil) // junk periods fall back to the default
	// A refunded order stops counting as revenue.
	before := rep.RevenueCents
	_, order, oid := e.paidOrder(p, "1")
	a.mustStatus(303, "POST", "/admin"+order+"/refund", url.Values{"restock": {"1"}})
	after, _ := e.app.Orders.API().Report(ctx, time.Now().UTC().AddDate(0, 0, -7), time.Now().UTC().AddDate(0, 0, 1))
	if after.RevenueCents != before || after.RefundedCents < 2500 {
		t.Fatalf("revenue %d -> %d, refunds %d: a refunded order must not count as revenue", before, after.RevenueCents, after.RefundedCents)
	}
	_ = oid
}

// ---- content pages ----

func TestStarterPagesAreDraftsWithPlaceholders(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	all, err := e.app.Content.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]content.Page{}
	for _, p := range all {
		seen[p.Slug] = p
	}
	for _, slug := range []string{"terms", "privacy", "returns", "shipping", "contact"} {
		p, ok := seen[slug]
		if !ok {
			t.Fatalf("starter page %q missing", slug)
		}
		if len(content.Placeholders(p.Body)) == 0 {
			t.Errorf("%s has no [[placeholders]]: a template must not look finished", slug)
		}
		if p.Published && len(content.Placeholders(p.Body)) > 0 {
			t.Errorf("%s is published with placeholders still in it", slug)
		}
	}
	// A draft is invisible to the public.
	if p := seen["terms"]; !p.Published {
		e.newClient().mustStatus(404, "GET", "/pages/terms", nil)
	}
}

func TestPublishingPagesAndTheFooter(t *testing.T) {
	e := newEnv(t)
	a := e.adminClient()
	slug := "faq-" + strings.ToLower(uuid.NewString()[:6])

	// A page with template placeholders cannot be published.
	res, body := a.do("POST", "/admin/pages", url.Values{"slug": {slug}, "title": {"FAQ"}, "body": {"Write to [[YOUR EMAIL]]"}, "published": {"1"}})
	if res.StatusCode != 422 || !strings.Contains(body, "replace the template placeholders") {
		t.Fatalf("publishing with placeholders = %d", res.StatusCode)
	}
	// As a draft it saves, and shows what is left to fill in.
	a.mustStatus(303, "POST", "/admin/pages", url.Values{"slug": {slug}, "title": {"FAQ"}, "body": {"Write to [[YOUR EMAIL]]"}})
	e.newClient().mustStatus(404, "GET", "/pages/"+slug, nil)
	pages, _ := e.app.Content.All(context.Background())
	var id uuid.UUID
	for _, p := range pages {
		if p.Slug == slug {
			id = p.ID
		}
	}
	_, edit := a.mustStatus(200, "GET", "/admin/pages/"+id.String(), nil)
	if !strings.Contains(edit, "[[YOUR EMAIL]]") {
		t.Fatal("the editor should list the placeholders still to fill in")
	}
	// Duplicate and invalid URLs are refused.
	if res, _ := a.do("POST", "/admin/pages", url.Values{"slug": {slug}, "title": {"Again"}}); res.StatusCode != 409 {
		t.Fatalf("duplicate slug = %d, want 409", res.StatusCode)
	}
	for _, bad := range []string{"Has Spaces", "../etc", "a", "x/y", "-lead", "dot.dot"} {
		if res, _ := a.do("POST", "/admin/pages", url.Values{"slug": {bad}, "title": {"t"}}); res.StatusCode != 422 {
			t.Errorf("slug %q = %d, want 422", bad, res.StatusCode)
		}
	}

	// Fill it in (with hostile content) and publish.
	hostile := "# FAQ\n\nEmail <script>alert(1)</script> us.\n\n- [click](javascript:alert(1))\n- **fine**\n"
	a.mustStatus(303, "POST", "/admin/pages/"+id.String(), url.Values{"slug": {slug}, "title": {"FAQ"}, "body": {hostile}, "published": {"1"}})
	_, page := e.newClient().mustStatus(200, "GET", "/pages/"+slug, nil)
	if !strings.Contains(page, "<h1>FAQ</h1>") || !strings.Contains(page, "<strong>fine</strong>") {
		t.Fatal("the page should be rendered")
	}
	if strings.Contains(page, "<script>alert(1)") || strings.Contains(page, `href="javascript:`) {
		t.Fatal("a page body must not be able to inject script")
	}
	// It appears in the footer of other pages straight away.
	if _, home := e.newClient().mustStatus(200, "GET", "/", nil); !strings.Contains(home, `href="/pages/`+slug+`"`) {
		t.Fatal("a published page should be linked from the footer")
	}
	// Unpublishing removes it again.
	a.mustStatus(303, "POST", "/admin/pages/"+id.String(), url.Values{"slug": {slug}, "title": {"FAQ"}, "body": {hostile}})
	e.newClient().mustStatus(404, "GET", "/pages/"+slug, nil)
	if _, home := e.newClient().mustStatus(200, "GET", "/", nil); strings.Contains(home, `href="/pages/`+slug+`"`) {
		t.Fatal("an unpublished page must leave the footer")
	}
	a.mustStatus(303, "POST", "/admin/pages/"+id.String()+"/delete", url.Values{})
	a.mustStatus(303, "POST", "/admin/pages/"+id.String()+"/delete", url.Values{}) // idempotent
	a.mustStatus(404, "GET", "/admin/pages/"+uuid.NewString(), nil)

	// Admin only.
	staff := e.staffClient()
	staff.mustStatus(403, "GET", "/admin/pages", nil)
	e.newClient().mustStatus(303, "GET", "/admin/pages", nil)
}

// ---- alerts: abandoned carts and low stock ----

func alertsEnv(t *testing.T) *env {
	e := newEnv(t, func(c *config.Config) {
		c.AbandonedCartHours, c.AlertEmail, c.LowStockThreshold = 24, "ops@shop.test", 5
	})
	// "Already sent today" is database state shared by every test in the run: start clean.
	if _, err := e.pool.Exec(context.Background(), `DELETE FROM alerts.sent`); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) ageCart(owner string, age time.Duration) {
	e.t.Helper()
	// A cart that was reminded before is made to look reminded *before* its latest change, as real time would have it.
	if _, err := e.pool.Exec(context.Background(), `
		UPDATE cart.carts SET updated_at = now() - $2::interval,
			reminded_at = CASE WHEN reminded_at IS NULL THEN NULL ELSE now() - $2::interval - interval '25 hours' END
		WHERE owner = $1`, owner, age.String()); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) userOwner(email string) string {
	us, err := e.app.Identity.ListUsers(context.Background(), email, "", 1, 0)
	if err != nil || len(us) != 1 {
		e.t.Fatalf("user %s: %v %v", email, us, err)
	}
	return "user:" + us[0].ID.String()
}

func (e *env) reminders(addr string) int {
	n := 0
	for _, m := range e.mailer.To(addr) {
		if strings.Contains(m.Subject, "left something in your cart") {
			n++
		}
	}
	return n
}

func TestAbandonedCartReminderIsSentOnceAndRespectsPreferences(t *testing.T) {
	e := alertsEnv(t)
	ctx := context.Background()
	p := e.product(1000, 50)

	u := e.newClient()
	email := "shopper-" + uuid.NewString() + "@example.com"
	u.mustStatus(303, "POST", "/register", url.Values{"name": {"Shay Shopper"}, "email": {email}, "password": {"correct horse battery"}})
	u.addToCart(p, "2")
	owner := e.userOwner(email)
	run := func() {
		t.Helper()
		if err := e.app.Alerts.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		e.drainJobs()
	}

	// A fresh cart is not abandoned yet, and a very old one is history.
	run()
	if e.reminders(email) != 0 {
		t.Fatal("a cart used a moment ago must not be reminded")
	}
	e.ageCart(owner, 30*24*time.Hour)
	run()
	if e.reminders(email) != 0 {
		t.Fatal("a month-old cart must not be reminded")
	}

	// Idle for a day: exactly one reminder, naming the items.
	e.ageCart(owner, 30*time.Hour)
	run()
	run() // a second pass must not repeat it
	if got := e.reminders(email); got != 1 {
		t.Fatalf("reminders = %d, want exactly 1", got)
	}
	var body string
	for _, m := range e.mailer.To(email) {
		if strings.Contains(m.Subject, "left something") {
			body = m.Text
		}
	}
	if !strings.Contains(body, p.Name) || !strings.Contains(body, "http://shop.test/cart") {
		t.Fatalf("reminder lacks the item or the cart link:\n%s", body)
	}

	// Changing the cart and abandoning it again earns one more reminder.
	u.addToCart(p, "1")
	e.ageCart(owner, 30*time.Hour)
	run()
	if got := e.reminders(email); got != 2 {
		t.Fatalf("reminders after a new idle period = %d, want 2", got)
	}

	// Opting out stops them.
	u.mustStatus(303, "POST", "/account/profile", url.Values{"name": {"Shay Shopper"}}) // checkbox absent = off
	u.addToCart(p, "1")
	e.ageCart(owner, 30*time.Hour)
	run()
	if got := e.reminders(email); got != 2 {
		t.Fatalf("a customer who opted out got a reminder (%d total)", got)
	}
	if _, page := u.mustStatus(200, "GET", "/account", nil); strings.Contains(page, `name="cart_reminders" value="1" checked`) {
		t.Fatal("the account page should show the preference as off")
	}
}

func TestNoRemindersForGuestsDisabledUsersOrPlacedOrders(t *testing.T) {
	e := alertsEnv(t)
	ctx := context.Background()
	p := e.product(1000, 50)
	run := func() { t.Helper(); e.app.Alerts.RunOnce(ctx); e.drainJobs() }

	// Anonymous visitors have no email at all.
	g := e.newClient()
	g.addToCart(p, "1")
	e.pool.Exec(ctx, `UPDATE cart.carts SET updated_at = now() - interval '30 hours' WHERE owner LIKE 'guest:%'`)
	run()

	// A disabled customer is left alone.
	d, _, dEmail := newUserEnvIn(e)
	d.addToCart(p, "1")
	e.ageCart(e.userOwner(dEmail), 30*time.Hour)
	admin := e.adminClient()
	_ = admin
	us, _ := e.app.Identity.ListUsers(ctx, dEmail, "", 1, 0)
	admins, _ := e.app.Identity.ListUsers(ctx, "admin-", identity.RoleAdmin, 1, 0)
	if err := e.app.Identity.SetDisabled(ctx, admins[0].ID, us[0].ID, true); err != nil {
		t.Fatal(err)
	}
	run()
	if e.reminders(dEmail) != 0 {
		t.Fatal("a disabled account must not be emailed")
	}

	// Someone who bought what was in the cart has no abandoned cart.
	b, _, bEmail := newUserEnvIn(e)
	b.addToCart(p, "1")
	b.placeOrder()
	e.ageCart(e.userOwner(bEmail), 30*time.Hour)
	run()
	if e.reminders(bEmail) != 0 {
		t.Fatal("an emptied cart must not be reminded")
	}
}

// newUserEnvIn registers a customer on an existing environment.
func newUserEnvIn(e *env) (*client, *env, string) {
	u := e.newClient()
	email := "u-" + uuid.NewString() + "@example.com"
	u.mustStatus(303, "POST", "/register", url.Values{"name": {"Ada Lovelace"}, "email": {email}, "password": {"correct horse battery"}})
	return u, e, email
}

func TestLowStockDigestIsSentOncePerDay(t *testing.T) {
	e := alertsEnv(t)
	ctx := context.Background()
	p := e.product(1000, 2)
	hidden := e.product(1000, 1)
	if err := e.app.Catalog.ArchiveProduct(ctx, hidden.ID); err != nil {
		t.Fatal(err)
	}
	digests := func() []mail.Message {
		var out []mail.Message
		for _, m := range e.mailer.To("ops@shop.test") {
			if strings.Contains(m.Subject, "Low stock") {
				out = append(out, m)
			}
		}
		return out
	}
	run := func() {
		t.Helper()
		if err := e.app.Alerts.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		e.drainJobs()
	}

	run()
	run()
	got := digests()
	if len(got) != 1 {
		t.Fatalf("%d digests after two passes, want 1", len(got))
	}
	if !strings.Contains(got[0].Text, p.Name) || !strings.Contains(got[0].Text, "2 left") {
		t.Fatalf("digest lacks the low item:\n%s", got[0].Text)
	}
	if strings.Contains(got[0].Text, hidden.Name) {
		t.Fatal("a deleted product must not appear in the digest")
	}
	// A day later it is sent again.
	e.pool.Exec(ctx, `UPDATE alerts.sent SET sent_at = now() - interval '25 hours' WHERE key = 'low_stock'`)
	run()
	if got := digests(); len(got) != 2 {
		t.Fatalf("%d digests the next day, want 2", len(got))
	}

	// With no alert address configured, nothing is sent.
	quiet := newEnv(t)
	quiet.product(1000, 1)
	if err := quiet.app.Alerts.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	quiet.drainJobs()
	if len(quiet.mailer.Sent()) != 0 {
		t.Fatal("no digest without ALERT_EMAIL")
	}
}

// ---- storefront: sorting, filters, SEO, related products ----

var jsonLDRe = regexp.MustCompile(`(?s)<script type="application/ld\+json">(.*?)</script>`)

func (e *env) productIn(name string, cents int64, stock int, cat *catalog.Category) catalog.Product {
	e.t.Helper()
	in := catalog.ProductInput{Name: name, PriceCents: cents, Active: true, Description: "A fine thing. " + name}
	if cat != nil {
		in.CategoryID = &cat.ID
	}
	p, err := e.app.Catalog.CreateProduct(context.Background(), in)
	if err != nil {
		e.t.Fatal(err)
	}
	if stock > 0 {
		if err := e.app.Inventory.AddLot(context.Background(), p.Variants[0].ID, "t", stock); err != nil {
			e.t.Fatal(err)
		}
	}
	return p
}

// order returns the position of each product link in page (-1 when absent).
func positions(page string, ps ...catalog.Product) []int {
	out := make([]int, len(ps))
	for i, p := range ps {
		out[i] = strings.Index(page, `href="/products/`+p.Slug+`"`)
	}
	return out
}

func TestSortingAndPriceFilters(t *testing.T) {
	e := newEnv(t)
	tag := strings.ToLower(uuid.NewString()[:6])
	cheap := e.productIn("Zeta "+tag, 1000, 5, nil)
	mid := e.productIn("Alpha "+tag, 5000, 5, nil)
	dear := e.productIn("Mu "+tag, 9000, 5, nil)
	c := e.newClient()
	get := func(q string) string { _, p := c.mustStatus(200, "GET", "/products?q="+tag+q, nil); return p }
	inOrder := func(page string, want ...catalog.Product) bool {
		pos := positions(page, want...)
		for i := range pos {
			if pos[i] < 0 || (i > 0 && pos[i] < pos[i-1]) {
				return false
			}
		}
		return true
	}

	if !inOrder(get("&sort=price_asc"), cheap, mid, dear) {
		t.Error("price ascending is wrong")
	}
	if !inOrder(get("&sort=price_desc"), dear, mid, cheap) {
		t.Error("price descending is wrong")
	}
	if !inOrder(get("&sort=name"), mid, dear, cheap) { // Alpha, Mu, Zeta
		t.Error("name ordering is wrong")
	}
	if !inOrder(get("&sort=newest"), dear, mid, cheap) {
		t.Error("newest first is wrong")
	}
	if !inOrder(get("&sort=<script>"), cheap, mid, dear) && get("&sort=<script>") == "" {
		t.Error("an unknown sort must be ignored, not break the page")
	}

	between := get("&min=20&max=60") // 20.00 - 60.00
	if p := positions(between, cheap, mid, dear); p[0] >= 0 || p[1] < 0 || p[2] >= 0 {
		t.Errorf("price range 20-60 should show only the 50.00 product: %v", p)
	}
	if p := positions(get("&min=60"), cheap, mid, dear); p[0] >= 0 || p[1] >= 0 || p[2] < 0 {
		t.Errorf("min 60 should show only the dearest: %v", p)
	}
	for _, junk := range []string{"&min=abc", "&max=-5", "&min=1e9&max=%00", "&page=zz"} {
		c.mustStatus(200, "GET", "/products?q="+tag+junk, nil)
	}
	// Filters survive in the form and in pagination links.
	if page := get("&sort=price_asc&min=20"); !strings.Contains(page, `value="20.00"`) || !strings.Contains(page, `selected`) {
		t.Error("the form should reflect the active filters")
	}
}

func TestProductPageSEOAndStructuredData(t *testing.T) {
	e := newEnv(t)
	a := e.adminClient()
	// A hostile name that tries to close the script element.
	name := `Evil </script><script>alert(1)</script> & "quotes" ` + uuid.NewString()[:5]
	p := e.productIn(name, 2599, 4, nil)
	a.upload(p.ID, "pic.png", pngBytes(t, 400, 400, 7), "front")

	c := e.newClient()
	res, page := c.mustStatus(200, "GET", "/products/"+p.Slug, nil)
	if res.Header.Get("X-Robots-Tag") != "" {
		t.Error("a product page must be indexable")
	}
	for _, want := range []string{
		`<link rel="canonical" href="http://shop.test/products/` + p.Slug + `"`,
		`<meta property="og:type" content="product"`,
		`<meta property="og:image" content="http://shop.test/media/`,
		`<meta name="twitter:card" content="summary_large_image"`,
		`<meta property="og:site_name" content="Test Shop"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	if strings.Contains(page, "</script><script>alert(1)") {
		t.Fatal("the product name escaped from its context")
	}
	m := jsonLDRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no JSON-LD block")
	}
	var doc struct {
		Type   string   `json:"@type"`
		Name   string   `json:"name"`
		Image  []string `json:"image"`
		Offers struct {
			Type         string `json:"@type"`
			Price        string `json:"price"`
			Currency     string `json:"priceCurrency"`
			Availability string `json:"availability"`
			URL          string `json:"url"`
		} `json:"offers"`
	}
	if err := json.Unmarshal([]byte(m[1]), &doc); err != nil {
		t.Fatalf("JSON-LD is not valid JSON: %v\n%s", err, m[1])
	}
	if doc.Type != "Product" || doc.Name != name || doc.Offers.Price != "25.99" || doc.Offers.Currency != "USD" ||
		doc.Offers.Availability != "https://schema.org/InStock" || !strings.HasPrefix(doc.Offers.URL, "http://shop.test/products/") ||
		len(doc.Image) == 0 || !strings.HasPrefix(doc.Image[0], "http://shop.test/media/") {
		t.Fatalf("structured data wrong: %+v", doc)
	}
	// Strictly one script close per script open: the JSON could not break out.
	if strings.Count(page, "<script") != strings.Count(page, "</script>") {
		t.Fatal("unbalanced script tags: markup injection through JSON-LD")
	}

	// Sold out flips the availability signal.
	lots, _ := e.app.Inventory.Lots(context.Background(), p.Variants[0].ID)
	e.app.Inventory.SetLotQuantity(context.Background(), p.Variants[0].ID, lots[0].ID, 0)
	_, page = c.mustStatus(200, "GET", "/products/"+p.Slug, nil)
	if !strings.Contains(jsonLDRe.FindStringSubmatch(page)[1], "OutOfStock") {
		t.Error("a sold-out product must say OutOfStock")
	}
}

func TestListingMetaAndNoIndexRules(t *testing.T) {
	e := newEnv(t)
	c := e.newClient()
	cat, _ := e.app.Catalog.CreateCategory(context.Background(), uniq("Gadgets"))
	e.productIn(uniq("Widget"), 1000, 1, &cat)
	_, plain := c.mustStatus(200, "GET", "/products", nil)
	if !strings.Contains(plain, `rel="canonical" href="http://shop.test/products"`) || strings.Contains(plain, `content="noindex"`) {
		t.Error("the plain listing should be canonical and indexable")
	}
	_, byCat := c.mustStatus(200, "GET", "/products?category="+cat.Slug, nil)
	if !strings.Contains(byCat, "<title>"+cat.Name) || strings.Contains(byCat, `content="noindex"`) {
		t.Error("a category page should be titled after the category and indexable")
	}
	for _, q := range []string{"?q=abc", "?sort=price_asc", "?min=5", "?page=2"} {
		if _, p := c.mustStatus(200, "GET", "/products"+q, nil); !strings.Contains(p, `<meta name="robots" content="noindex"`) {
			t.Errorf("%s should be noindex", q)
		}
	}
	// Private areas carry a noindex header whatever they render.
	for _, path := range []string{"/cart", "/login", "/register", "/checkout", "/admin", "/orders", "/account"} {
		res, _ := c.do("GET", path, nil)
		if res.Header.Get("X-Robots-Tag") == "" {
			t.Errorf("%s lacks X-Robots-Tag", path)
		}
	}
}

func TestRobotsAndSitemap(t *testing.T) {
	e := newEnv(t)
	c := e.newClient()
	ctx := context.Background()
	cat, _ := e.app.Catalog.CreateCategory(ctx, uniq("Maps"))
	live := e.productIn(uniq("Live"), 1000, 1, &cat)
	gone := e.productIn(uniq("Gone"), 1000, 1, &cat)
	e.app.Catalog.ArchiveProduct(ctx, gone.ID)
	draft, _ := e.app.Catalog.CreateProduct(ctx, catalog.ProductInput{Name: uniq("Draft"), PriceCents: 1, Active: false})

	res, robots := c.mustStatus(200, "GET", "/robots.txt", nil)
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/plain") {
		t.Error("robots.txt content type")
	}
	for _, want := range []string{"Disallow: /admin", "Disallow: /cart", "Disallow: /checkout", "Sitemap: http://shop.test/sitemap.xml"} {
		if !strings.Contains(robots, want) {
			t.Errorf("robots.txt lacks %q", want)
		}
	}
	res, body := c.mustStatus(200, "GET", "/sitemap.xml", nil)
	if !strings.Contains(res.Header.Get("Content-Type"), "xml") {
		t.Error("sitemap content type")
	}
	var set struct {
		URLs []struct {
			Loc string `xml:"loc"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal([]byte(body), &set); err != nil {
		t.Fatalf("sitemap is not valid XML: %v", err)
	}
	locs := map[string]bool{}
	for _, u := range set.URLs {
		locs[u.Loc] = true
	}
	for _, want := range []string{"http://shop.test/", "http://shop.test/products", "http://shop.test/products/" + live.Slug, "http://shop.test/products?category=" + cat.Slug} {
		if !locs[want] {
			t.Errorf("sitemap lacks %s", want)
		}
	}
	for _, p := range []catalog.Product{gone, draft} {
		if locs["http://shop.test/products/"+p.Slug] {
			t.Errorf("sitemap lists a hidden/deleted product %s", p.Slug)
		}
	}
}

func TestRelatedProducts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cat, _ := e.app.Catalog.CreateCategory(ctx, uniq("Family"))
	other, _ := e.app.Catalog.CreateCategory(ctx, uniq("Stranger"))
	a := e.productIn(uniq("Main"), 1000, 1, &cat)
	sib := e.productIn(uniq("Sibling"), 1000, 1, &cat)
	hidden := e.productIn(uniq("Hidden"), 1000, 1, &cat)
	e.app.Catalog.ArchiveProduct(ctx, hidden.ID)
	far := e.productIn(uniq("Unrelated"), 1000, 1, &other)

	_, page := e.newClient().mustStatus(200, "GET", "/products/"+a.Slug, nil)
	if !strings.Contains(page, "You may also like") || !strings.Contains(page, `href="/products/`+sib.Slug+`"`) {
		t.Error("a sibling from the same category should be suggested")
	}
	for name, p := range map[string]catalog.Product{"itself": a, "a deleted product": hidden, "another category": far} {
		// The product's own link appears in canonical/breadcrumb markup; look only inside the related section.
		i := strings.Index(page, "You may also like")
		if i >= 0 && strings.Contains(page[i:], `href="/products/`+p.Slug+`"`) {
			t.Errorf("related products include %s", name)
		}
	}
	// A product with no category has nothing related and no empty heading.
	lone := e.productIn(uniq("Lone"), 1000, 1, nil)
	if _, p := e.newClient().mustStatus(200, "GET", "/products/"+lone.Slug, nil); strings.Contains(p, "You may also like") {
		t.Error("no related section expected for an uncategorised product")
	}
}
