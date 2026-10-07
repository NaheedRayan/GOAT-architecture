// Package ui holds the shared page shell and small view helpers.
package ui

import (
	"context"
	"encoding/json"
	"strings"
)

type cartCountKey struct{}
type footerKey struct{}

// FooterLink is a published content page shown in the page footer.
type FooterLink struct{ Href, Title string }

func WithFooterLinks(ctx context.Context, l []FooterLink) context.Context {
	return context.WithValue(ctx, footerKey{}, l)
}

func FooterLinks(ctx context.Context) []FooterLink {
	l, _ := ctx.Value(footerKey{}).([]FooterLink)
	return l
}

// WithCartCount lets the cart module expose the item count to the page header
// without the header importing the cart module.
func WithCartCount(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, cartCountKey{}, n)
}

func CartCount(ctx context.Context) int {
	n, _ := ctx.Value(cartCountKey{}).(int)
	return n
}

type siteKey struct{}

// Site identifies the shop and where it lives, for page titles, canonical links and social previews.
type Site struct {
	Name    string
	BaseURL string // https://shop.example.com, no trailing slash
}

func WithSite(ctx context.Context, s Site) context.Context {
	return context.WithValue(ctx, siteKey{}, s)
}

func SiteFrom(ctx context.Context) Site {
	s, _ := ctx.Value(siteKey{}).(Site)
	if s.Name == "" {
		s.Name = "GOAT Store"
	}
	return s
}

// AbsURL makes a site-relative path absolute (leaving absolute URLs alone).
func AbsURL(ctx context.Context, path string) string {
	if path == "" || strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	return strings.TrimRight(SiteFrom(ctx).BaseURL, "/") + path
}

// JSONLD encodes v as a JSON-LD document. encoding/json escapes <, > and &, so the result
// cannot terminate the surrounding <script> element.
func JSONLD(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return strings.ReplaceAll(string(b), "</", `<\/`)
}

// Meta describes a page for search engines and link previews.
type Meta struct {
	Title       string
	Description string
	Canonical   string // site-relative path of the preferred URL ("" omits the tag)
	Image       string // path or URL of a preview image
	Type        string // Open Graph type; "website" by default, "product" for products
	NoIndex     bool
	JSONLD      string // a JSON-LD document (already JSON-encoded), or ""
}
