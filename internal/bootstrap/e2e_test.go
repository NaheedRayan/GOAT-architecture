package bootstrap_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
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
	"github.com/NaheedRayan/goat-architecture/internal/order"
	"github.com/NaheedRayan/goat-architecture/internal/platform/config"
	"github.com/NaheedRayan/goat-architecture/internal/platform/testdb"
)

type env struct {
	pool   *pgxpool.Pool
	t      *testing.T
	app    *bootstrap.App
	server *httptest.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testdb.Pool(t)
	cfg := config.Config{
		Env: "test", Currency: "USD", ReservationTTL: 15 * time.Minute, AccessTokenTTL: 15 * time.Minute,
		RefreshTokenTTL: time.Hour, PaymentProvider: "mock", PaymentWebhookSecret: "test-secret",
	}
	var logw io.Writer = io.Discard
	if os.Getenv("TEST_LOG") != "" {
		logw = os.Stderr
	}
	app, err := bootstrap.New(context.Background(), cfg, pool, slog.New(slog.NewTextHandler(logw, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app.Handler)
	t.Cleanup(srv.Close)
	return &env{t: t, app: app, server: srv, pool: pool}
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
		if err := e.app.Inventory.AddLot(ctx, p.ID, "t", stock); err != nil {
			e.t.Fatal(err)
		}
	}
	return p
}

func (e *env) available(p catalog.Product) int {
	m, err := e.app.Inventory.Available(context.Background(), []uuid.UUID{p.ID})
	if err != nil {
		e.t.Fatal(err)
	}
	return m[p.ID]
}

func (c *client) addToCart(p catalog.Product, qty string) {
	c.t.Helper()
	c.mustStatus(303, "POST", "/cart/items", url.Values{"product_id": {p.ID.String()}, "quantity": {qty}})
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

var statusRe = regexp.MustCompile(`Awaiting payment|Paid|Preparing|Shipped|Cancelled`)

func (c *client) orderStatus(orderPath string) string {
	c.t.Helper()
	_, page := c.mustStatus(200, "GET", orderPath, nil)
	return statusRe.FindString(page)
}

// drain runs queued jobs and fulfillment pickup deterministically.
func (e *env) drain() {
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
	if _, err := e.app.Orders.FulfillOnce(ctx); err != nil {
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
	// Find the order through the customer's order list.
	_, list := c.mustStatus(200, "GET", "/orders", nil)
	m := regexp.MustCompile(`/orders/[0-9a-f-]{36}`).FindString(list)
	if m == "" {
		c.t.Fatal("no order in list")
	}
	return payPath, m
}

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
		`UPDATE inventory.reservations SET expires_at = now() - interval '1 minute' WHERE product_id = $1`, p.ID); err != nil {
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

func TestAnonymousIsRedirectedToLogin(t *testing.T) {
	e := newEnv(t)
	res, _ := e.newClient().mustStatus(303, "GET", "/checkout", nil)
	if loc := res.Header.Get("Location"); loc != "/login?next=%2Fcheckout" {
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
	if res, _ := shopper.do("POST", "/cart/items", url.Values{"product_id": {p.ID.String()}, "quantity": {"1"}}); res.StatusCode != 422 {
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
	cfg := config.Config{
		Env: "production", Currency: "USD", AccessTokenTTL: time.Minute, RefreshTokenTTL: time.Hour, ReservationTTL: time.Minute,
		PaymentProvider: "mock", JWTSeed: "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=",
	}
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
			u.do("POST", "/cart/items", url.Values{"product_id": {p.ID.String()}, "quantity": {"1"}})
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
		res, _ := e.raw("POST", "/cart/items", "", url.Values{"product_id": {p.ID.String()}, "quantity": {"1"}})
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
		res, _ := u.do("POST", "/cart/items", url.Values{"product_id": {p.ID.String()}, "quantity": {"1"}})
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
	lots, err := e.app.Inventory.Lots(context.Background(), p.ID)
	if err != nil || len(lots) != 1 {
		t.Fatalf("lots: %v %v", lots, err)
	}
	path := "/admin/products/" + p.ID.String() + "/stock/" + lots[0].ID.String()

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
	a.mustStatus(404, "POST", "/admin/products/"+other.ID.String()+"/stock/"+lots[0].ID.String(), url.Values{"quantity": {"1"}})
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
	cfg := config.Config{
		Env: "test", Currency: "USD", ReservationTTL: time.Minute, AccessTokenTTL: time.Minute, RefreshTokenTTL: time.Hour,
		PaymentProvider: "mock", PublicURL: "https://shop.example.com",
	}
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
