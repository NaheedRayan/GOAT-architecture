package bootstrap_test

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestReviewsAreGatedModeratedAndEscaped(t *testing.T) {
	e := newEnv(t)
	p := e.product(2500, 10)
	slug := mustSlug(t, e, p)
	page := "/products/" + slug

	// Anonymous visitors are sent to sign in.
	anon := e.newClient()
	if res, _ := anon.do("POST", page+"/reviews", url.Values{"rating": {"5"}, "body": {"x"}}); res.StatusCode != 303 || !strings.Contains(res.Header.Get("Location"), "/login") {
		t.Fatalf("anonymous review = %d %s, want redirect to login", res.StatusCode, res.Header.Get("Location"))
	}

	u := e.newClient()
	u.register()
	u.addToCart(p, "1")
	payPath, order := u.placeOrder()
	u.pay(payPath, "succeeded")
	e.drain()

	// Paid but not shipped: not yet a verified purchase.
	res, _ := u.do("POST", page+"/reviews", url.Values{"rating": {"5"}, "body": {"great"}})
	if loc := res.Header.Get("Location"); !strings.Contains(loc, "review=not-purchased") {
		t.Fatalf("review before shipping redirected to %q, want not-purchased", loc)
	}

	a := e.adminClient()
	a.mustStatus(303, "POST", strings.Replace(order, "/orders/", "/admin/orders/", 1)+"/ship", url.Values{})

	// Invalid input is refused.
	for _, bad := range []url.Values{{"rating": {"0"}, "body": {"x"}}, {"rating": {"6"}, "body": {"x"}}, {"rating": {"4"}}} {
		res, _ := u.do("POST", page+"/reviews", bad)
		if !strings.Contains(res.Header.Get("Location"), "review=invalid") {
			t.Fatalf("form %v redirected to %q, want invalid", bad, res.Header.Get("Location"))
		}
	}

	// A valid review is published; markup in it is escaped.
	res, _ = u.do("POST", page+"/reviews", url.Values{"rating": {"5"}, "title": {"<script>alert(1)</script>"}, "body": {"Loved it"}})
	if !strings.Contains(res.Header.Get("Location"), "review=saved") {
		t.Fatalf("review redirected to %q, want saved", res.Header.Get("Location"))
	}
	_, html := anon.mustStatus(200, "GET", page, nil)
	if strings.Contains(html, "<script>alert(1)") {
		t.Fatal("review title was not escaped")
	}
	if !strings.Contains(html, "Loved it") || !strings.Contains(html, "Verified purchase") || !strings.Contains(html, "aggregateRating") {
		t.Fatal("published review, badge or structured data missing")
	}

	// Writing again edits the same review rather than adding another.
	u.do("POST", page+"/reviews", url.Values{"rating": {"3"}, "body": {"Changed my mind"}})
	_, html = anon.mustStatus(200, "GET", page, nil)
	if strings.Contains(html, "Loved it") || !strings.Contains(html, "Changed my mind") || !strings.Contains(html, "1 review(s)") {
		t.Fatal("editing should replace the review")
	}
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM review.reviews WHERE product_id = $1`, p.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("reviews stored = %d (%v), want 1", n, err)
	}

	// Customers cannot moderate; admins can hide, and a hide survives an edit.
	u.mustStatus(403, "GET", "/admin/reviews", nil)
	_, list := a.mustStatus(200, "GET", "/admin/reviews", nil)
	m := regexp.MustCompile(`/admin/reviews/([0-9a-f-]{36})/status`).FindStringSubmatch(list)
	if m == nil {
		t.Fatal("review missing from the moderation page")
	}
	a.mustStatus(303, "POST", m[0], url.Values{"status": {"hidden"}})
	_, html = anon.mustStatus(200, "GET", page, nil)
	if strings.Contains(html, "Changed my mind") {
		t.Fatal("hidden review is still public")
	}
	u.do("POST", page+"/reviews", url.Values{"rating": {"1"}, "body": {"Edited after hide"}})
	_, html = anon.mustStatus(200, "GET", page, nil)
	if strings.Contains(html, "Edited after hide") {
		t.Fatal("editing must not re-publish a hidden review")
	}
	a.mustStatus(303, "POST", m[0], url.Values{"status": {"published"}})
	a.mustStatus(303, "POST", strings.Replace(m[0], "/status", "/delete", 1), url.Values{})
	_, html = anon.mustStatus(200, "GET", page, nil)
	if strings.Contains(html, "Edited after hide") {
		t.Fatal("deleted review is still public")
	}
}

func TestWishlistTogglesExportsAndIsErasedWithTheAccount(t *testing.T) {
	e := newEnv(t)
	p := e.product(1000, 5)
	slug := mustSlug(t, e, p)

	anon := e.newClient()
	if res, _ := anon.do("POST", "/wishlist/"+p.ID.String(), nil); res.StatusCode != 303 || !strings.Contains(res.Header.Get("Location"), "/login") {
		t.Fatalf("anonymous wish = %d %s, want login redirect", res.StatusCode, res.Header.Get("Location"))
	}

	u := e.newClient()
	u.register()
	u.mustStatus(404, "POST", "/wishlist/00000000-0000-7000-8000-000000000000", nil) // unknown product
	u.mustStatus(303, "POST", "/wishlist/"+p.ID.String(), url.Values{"next": {"/products/" + slug}})
	u.mustStatus(303, "POST", "/wishlist/"+p.ID.String(), nil) // adding twice is harmless
	_, html := u.mustStatus(200, "GET", "/wishlist", nil)
	if !strings.Contains(html, p.Name) {
		t.Fatal("wished product missing from the wishlist page")
	}
	_, html = u.mustStatus(200, "GET", "/products/"+slug, nil)
	if !strings.Contains(html, "Saved to wishlist") {
		t.Fatal("product page should show the saved state")
	}
	_, data := u.mustStatus(200, "GET", "/account/export", nil)
	if !strings.Contains(data, p.ID.String()) {
		t.Fatal("wishlist missing from the data export")
	}

	// Removing, then redirect hardening.
	res, _ := u.do("POST", "/wishlist/"+p.ID.String()+"/remove", url.Values{"next": {"//evil.example"}})
	if loc := res.Header.Get("Location"); strings.Contains(loc, "evil") {
		t.Fatalf("redirected to %q", loc)
	}
	_, html = u.mustStatus(200, "GET", "/wishlist", nil)
	if strings.Contains(html, p.Name) {
		t.Fatal("removed product still on the wishlist")
	}

	// Account deletion erases what is left.
	u.mustStatus(303, "POST", "/wishlist/"+p.ID.String(), nil)
	u.mustStatus(303, "POST", "/account/delete", url.Values{"password": {"correct horse battery"}})
	e.drainJobs()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM wishlist.items WHERE product_id = $1`, p.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("wishlist rows after account deletion = %d (%v), want 0", n, err)
	}
}

// Several shoppers race for the last units: exactly the stock is sold, never more.
func TestNoOversellUnderConcurrentCheckout(t *testing.T) {
	e := newEnv(t)
	const stock, buyers = 2, 6 // few buyers: registration is rate-limited per address
	p := e.product(1000, stock)

	type buyer struct {
		c          *client
		key, total string
	}
	clients := make([]buyer, buyers)
	for i := range clients { // set up sequentially; t.Fatal must not run in goroutines
		c := e.newClient()
		c.register()
		c.addToCart(p, "1")
		_, page := c.mustStatus(200, "GET", "/checkout", nil)
		clients[i] = buyer{c, idempotencyKey.FindStringSubmatch(page)[1], expectedTotal.FindStringSubmatch(page)[1]}
	}
	codes := make(chan int, buyers)
	start := make(chan struct{})
	for _, c := range clients {
		go func() {
			<-start
			res, _, _ := c.c.post(c.key, c.total, shipping)
			codes <- res.StatusCode
		}()
	}
	close(start)
	won := 0
	for range clients {
		if <-codes == 303 {
			won++
		}
	}
	if won != stock {
		t.Fatalf("%d checkouts succeeded for %d units", won, stock)
	}
	if got := e.available(p); got != 0 {
		t.Fatalf("available after the rush = %d, want 0", got)
	}
}
