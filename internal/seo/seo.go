// Package seo serves robots.txt and sitemap.xml, and keeps private areas out of
// search indexes. It owns no data: it reads the catalogue and content pages
// through their public APIs.
package seo

import (
	"encoding/xml"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/NaheedRayan/goat-architecture/internal/catalog"
	"github.com/NaheedRayan/goat-architecture/internal/content"
)

type Options struct {
	Catalog catalog.API
	Content content.API
	BaseURL string // https://shop.example.com; when empty the request's host is used
	Log     *slog.Logger
}

type Module struct {
	o Options

	mu        sync.Mutex
	cached    []byte
	cachedAt  time.Time
	cachedFor string
}

func New(o Options) *Module { return &Module{o: o} }

func (m *Module) Routes(r chi.Router) {
	r.Get("/robots.txt", m.robots)
	r.Get("/sitemap.xml", m.sitemap)
}

// privatePrefixes are pages that belong to one person or to staff: never indexed.
var privatePrefixes = []string{"/admin", "/account", "/cart", "/checkout", "/orders", "/pay/", "/login", "/register", "/forgot", "/reset", "/verify", "/goodbye", "/webhooks"}

// NoIndex asks search engines to skip private pages even if something links to them.
func NoIndex(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, p := range privatePrefixes {
			if strings.HasPrefix(r.URL.Path, p) {
				w.Header().Set("X-Robots-Tag", "noindex, nofollow")
				break
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (m *Module) base(r *http.Request) string {
	if m.o.BaseURL != "" {
		return strings.TrimRight(m.o.BaseURL, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (m *Module) robots(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString("User-agent: *\n")
	for _, p := range privatePrefixes {
		b.WriteString("Disallow: " + p + "\n")
	}
	b.WriteString("Disallow: /products?*sort=\nDisallow: /products?*q=\n")
	b.WriteString("\nSitemap: " + m.base(r) + "/sitemap.xml\n")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(b.String()))
}

type urlEntry struct {
	Loc        string `xml:"loc"`
	LastMod    string `xml:"lastmod,omitempty"`
	ChangeFreq string `xml:"changefreq,omitempty"`
	Priority   string `xml:"priority,omitempty"`
}

type urlSet struct {
	XMLName xml.Name   `xml:"urlset"`
	NS      string     `xml:"xmlns,attr"`
	URLs    []urlEntry `xml:"url"`
}

const sitemapTTL = 10 * time.Minute

func (m *Module) sitemap(w http.ResponseWriter, r *http.Request) {
	base := m.base(r)
	m.mu.Lock()
	if m.cached != nil && m.cachedFor == base && time.Since(m.cachedAt) < sitemapTTL {
		body := m.cached
		m.mu.Unlock()
		writeXML(w, body)
		return
	}
	m.mu.Unlock()

	set := urlSet{NS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	add := func(path, lastmod, freq, prio string) {
		set.URLs = append(set.URLs, urlEntry{Loc: base + path, LastMod: lastmod, ChangeFreq: freq, Priority: prio})
	}
	add("/", "", "daily", "1.0")
	add("/products", "", "daily", "0.9")
	cats, err := m.o.Catalog.ActiveCategories(r.Context())
	if err != nil {
		m.fail(w, err)
		return
	}
	for _, c := range cats {
		add("/products?category="+url.QueryEscape(c.Slug), "", "daily", "0.7")
	}
	entries, err := m.o.Catalog.Sitemap(r.Context())
	if err != nil {
		m.fail(w, err)
		return
	}
	for _, e := range entries {
		add("/products/"+url.PathEscape(e.Slug), e.CreatedAt.UTC().Format("2006-01-02"), "weekly", "0.8")
	}
	pages, err := m.o.Content.PublishedLinks(r.Context())
	if err != nil {
		m.fail(w, err)
		return
	}
	for _, p := range pages {
		add("/pages/"+url.PathEscape(p.Slug), "", "monthly", "0.3")
	}
	body, err := xml.MarshalIndent(set, "", "  ")
	if err != nil {
		m.fail(w, err)
		return
	}
	body = append([]byte(xml.Header), body...)
	m.mu.Lock()
	m.cached, m.cachedAt, m.cachedFor = body, time.Now(), base
	m.mu.Unlock()
	writeXML(w, body)
}

func writeXML(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=600")
	_, _ = w.Write(body)
}

func (m *Module) fail(w http.ResponseWriter, err error) {
	m.o.Log.Error("sitemap", "err", err)
	http.Error(w, "unavailable", http.StatusInternalServerError)
}
