// Package http serves the public storefront pages for browsing the catalog.
package http

import (
	"context"
	"errors"
	"log/slog"
	nethttp "net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/catalog/app"
	"github.com/NaheedRayan/goat-architecture/internal/catalog/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/auth"
	"github.com/NaheedRayan/goat-architecture/internal/platform/httpx"
	"github.com/NaheedRayan/goat-architecture/internal/platform/money"
	"github.com/NaheedRayan/goat-architecture/internal/platform/ui"
)

// StockLookup lets the storefront show availability without importing the
// inventory module. It may be nil, in which case every product shows as available.
type StockLookup interface {
	Available(ctx context.Context, productIDs []uuid.UUID) (map[uuid.UUID]int, error)
}

// Rating is a product's average review rating.
type Rating struct {
	Count   int
	Average float64
}

// Extras are optional storefront features that other modules provide (reviews,
// wishlists). It may be nil, in which case the product pages simply omit them.
type Extras interface {
	Ratings(ctx context.Context, productIDs []uuid.UUID) map[uuid.UUID]Rating
	// Reviews renders the reviews block of a product page; flash is a just-submitted outcome code.
	Reviews(ctx context.Context, productID uuid.UUID, slug, flash string) templ.Component
	Wished(ctx context.Context, productID uuid.UUID) bool
	WishlistIDs(ctx context.Context) []uuid.UUID
}

type Handler struct {
	svc    *app.Service
	stock  StockLookup
	extras Extras
	log    *slog.Logger
}

func NewHandler(svc *app.Service, stock StockLookup, extras Extras, log *slog.Logger) *Handler {
	return &Handler{svc: svc, stock: stock, extras: extras, log: log}
}

func (h *Handler) Routes(r chi.Router) {
	r.Get("/", h.home)
	r.Get("/products", h.list)
	r.Get("/products/{slug}", h.detail)
	r.Group(func(r chi.Router) {
		r.Use(auth.Require)
		r.Get("/wishlist", h.wishlist)
	})
}

func (h *Handler) wishlist(w nethttp.ResponseWriter, r *nethttp.Request) {
	var ps []domain.Product
	if h.extras != nil {
		if ids := h.extras.WishlistIDs(r.Context()); len(ids) > 0 {
			all, err := h.svc.ByIDs(r.Context(), ids)
			if err != nil {
				h.fail(w, r, err)
				return
			}
			byID := make(map[uuid.UUID]domain.Product, len(all))
			for _, p := range all {
				byID[p.ID] = p
			}
			for _, id := range ids { // keep most-recently-added first; skip products that are gone or hidden
				if p, ok := byID[id]; ok && p.Active && p.ArchivedAt == nil {
					ps = append(ps, p)
				}
			}
		}
	}
	httpx.Render(w, r, nethttp.StatusOK, WishlistPage(h.cards(r.Context(), ps)))
}

// Card is a product plus the availability the UI needs.
type Card struct {
	Product domain.Product
	InStock bool
	Rating  Rating
}

// FromPrice is the lowest price across the product's variants.
func (c Card) FromPrice() int64 {
	if c.Product.MinPriceCents > 0 {
		return c.Product.MinPriceCents
	}
	return c.Product.PriceCents
}

// PriceVaries reports whether variants have different prices ("From $x").
func (c Card) PriceVaries() bool { return c.Product.MaxPriceCents > c.Product.MinPriceCents }

// VariantView is one selectable variant on the product page.
type VariantView struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Price   string `json:"price"`
	InStock bool   `json:"in_stock"`
}

// stockFor returns available units per variant, or known=false when stock is not tracked/unavailable.
func (h *Handler) stockFor(ctx context.Context, ids []uuid.UUID) (avail map[uuid.UUID]int, known bool) {
	if h.stock == nil || len(ids) == 0 {
		return nil, false
	}
	m, err := h.stock.Available(ctx, ids)
	if err != nil {
		h.log.Warn("stock lookup failed", "err", err)
		return nil, false
	}
	return m, true
}

func (h *Handler) cards(ctx context.Context, ps []domain.Product) []Card {
	ids := make([]uuid.UUID, len(ps))
	for i, p := range ps {
		ids[i] = p.ID
	}
	variants, err := h.svc.VariantsByProducts(ctx, ids)
	if err != nil {
		h.log.Warn("variant lookup failed", "err", err)
	}
	var vids []uuid.UUID
	for _, vs := range variants {
		for _, v := range vs {
			if v.Active {
				vids = append(vids, v.ID)
			}
		}
	}
	avail, known := h.stockFor(ctx, vids)
	var ratings map[uuid.UUID]Rating
	if h.extras != nil && len(ids) > 0 {
		ratings = h.extras.Ratings(ctx, ids)
	}
	out := make([]Card, len(ps))
	for i, p := range ps {
		inStock := !known
		for _, v := range variants[p.ID] {
			if v.Active && avail[v.ID] > 0 {
				inStock = true
			}
		}
		out[i] = Card{Product: p, InStock: inStock, Rating: ratings[p.ID]}
	}
	return out
}

func (h *Handler) home(w nethttp.ResponseWriter, r *nethttp.Request) {
	if r.URL.Path != "/" {
		nethttp.NotFound(w, r)
		return
	}
	page, err := h.svc.List(r.Context(), domain.Filter{PerPage: 8})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.Render(w, r, nethttp.StatusOK, HomePage(h.cards(r.Context(), page.Products)))
}

// ListVM is everything the listing page shows.
type ListVM struct {
	Filter   domain.Filter
	Page     domain.Page
	Cats     []domain.Category
	Cards    []Card
	MinStr   string // what the shopper typed, when valid
	MaxStr   string
	Currency string
}

func (h *Handler) list(w nethttp.ResponseWriter, r *nethttp.Request) {
	qv := r.URL.Query()
	pg, _ := strconv.Atoi(qv.Get("page"))
	f := domain.Filter{Query: qv.Get("q"), CategorySlug: qv.Get("category"), Page: pg, Sort: qv.Get("sort")}
	vm := ListVM{}
	if c, err := money.ParseCents(qv.Get("min")); err == nil && c > 0 {
		f.MinPriceCents, vm.MinStr = c, money.Decimal(c)
	}
	if c, err := money.ParseCents(qv.Get("max")); err == nil && c > 0 {
		f.MaxPriceCents, vm.MaxStr = c, money.Decimal(c)
	}
	page, err := h.svc.List(r.Context(), f)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	cats, err := h.svc.ActiveCategories(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	f.Page, f.Sort = page.Page, qv.Get("sort")
	if !domain.ValidSort(f.Sort) {
		f.Sort = ""
	}
	vm.Filter, vm.Page, vm.Cats, vm.Cards = f, page, cats, h.cards(r.Context(), page.Products)
	vm.Currency = h.currency(page.Products)
	httpx.Render(w, r, nethttp.StatusOK, ListPage(vm, h.listMeta(f, cats, page)))
}

// currency is the currency to show on the price filter; products share one, so the first will do.
func (h *Handler) currency(ps []domain.Product) string {
	if len(ps) > 0 {
		return ps[0].Currency
	}
	return ""
}

// listMeta names the page after what is being browsed. Filtered, sorted, searched and
// paginated views are not canonical, so they are kept out of search indexes.
func (h *Handler) listMeta(f domain.Filter, cats []domain.Category, p domain.Page) ui.Meta {
	m := ui.Meta{Title: "All products", Canonical: "/products", Description: "Browse every product in the store."}
	for _, c := range cats {
		if c.Slug == f.CategorySlug {
			m.Title, m.Canonical = c.Name, "/products?category="+url.QueryEscape(c.Slug)
			m.Description = "Shop " + c.Name + "."
		}
	}
	if f.Query != "" {
		m.Title, m.NoIndex = "Results for “"+clipText(f.Query, 60)+"”", true
	}
	if f.Sort != "" || f.MinPriceCents > 0 || f.MaxPriceCents > 0 || p.Page > 1 {
		m.NoIndex = true
	}
	return m
}

func clipText(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// DetailVM is everything the product page shows.
type DetailVM struct {
	Product  domain.Product
	Variants []VariantView
	Related  []Card
	Reviews  templ.Component // nil without the reviews module
	CanWish  bool            // the wishlist feature is available
	Wished   bool
}

func (h *Handler) detail(w nethttp.ResponseWriter, r *nethttp.Request) {
	p, err := h.svc.BySlug(r.Context(), chi.URLParam(r, "slug"))
	if errors.Is(err, domain.ErrNotFound) || err == nil && !p.Active {
		httpx.Render(w, r, nethttp.StatusNotFound, ui.ErrorPage(404, "We couldn't find that product."))
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var vids []uuid.UUID
	for _, v := range p.Variants {
		if v.Active {
			vids = append(vids, v.ID)
		}
	}
	avail, known := h.stockFor(r.Context(), vids)
	var views []VariantView
	for _, v := range p.Variants {
		if !v.Active {
			continue
		}
		views = append(views, VariantView{
			ID: v.ID.String(), Label: v.Label, Price: money.Format(v.EffectivePrice(p), p.Currency), InStock: !known || avail[v.ID] > 0,
		})
	}
	vm := DetailVM{Product: p, Variants: views}
	if h.extras != nil {
		vm.Reviews = h.extras.Reviews(r.Context(), p.ID, p.Slug, reviewFlash(r.URL.Query().Get("review")))
		vm.CanWish = true
		vm.Wished = h.extras.Wished(r.Context(), p.ID)
	}
	if rel, err := h.svc.Related(r.Context(), p.ID, 4); err != nil {
		h.log.Warn("related products", "err", err)
	} else {
		vm.Related = h.cards(r.Context(), rel)
	}
	httpx.Render(w, r, nethttp.StatusOK, DetailPage(vm, h.productMeta(r.Context(), p, avail, known)))
}

// reviewFlash passes only known outcome codes through to the page.
func reviewFlash(v string) string {
	switch v {
	case "saved", "invalid", "not-purchased", "error":
		return v
	}
	return ""
}

// productMeta builds the search-engine and link-preview description of a product,
// including schema.org structured data (which enables price and stock in search results).
func (h *Handler) productMeta(ctx context.Context, p domain.Product, avail map[uuid.UUID]int, known bool) ui.Meta {
	desc := clipText(strings.Join(strings.Fields(p.Description), " "), 160)
	image := p.ImageURL
	if image == "" {
		image = p.ThumbURL
	}
	var low, high int64 = -1, 0
	inStock := !known
	sku := ""
	for _, v := range p.Variants {
		if !v.Active {
			continue
		}
		price := v.EffectivePrice(p)
		if low < 0 || price < low {
			low = price
		}
		high = max(high, price)
		if avail[v.ID] > 0 {
			inStock = true
		}
		if sku == "" {
			sku = v.SKU
		}
	}
	if low < 0 {
		low, high = p.PriceCents, p.PriceCents
	}
	availability := "https://schema.org/OutOfStock"
	if inStock {
		availability = "https://schema.org/InStock"
	}
	var images []string // search engines want absolute URLs
	for _, im := range p.Images {
		images = append(images, ui.AbsURL(ctx, im.URL))
	}
	if len(images) == 0 && image != "" {
		images = []string{ui.AbsURL(ctx, image)}
	}
	doc := map[string]any{"@context": "https://schema.org", "@type": "Product", "name": p.Name, "description": desc}
	if len(images) > 0 {
		doc["image"] = images
	}
	if sku != "" {
		doc["sku"] = sku
	}
	offer := map[string]any{"priceCurrency": p.Currency, "availability": availability, "url": ui.AbsURL(ctx, "/products/"+p.Slug)}
	if low == high {
		offer["@type"], offer["price"] = "Offer", money.Decimal(low)
	} else {
		offer["@type"], offer["lowPrice"], offer["highPrice"] = "AggregateOffer", money.Decimal(low), money.Decimal(high)
	}
	doc["offers"] = offer
	if h.extras != nil {
		if rt, ok := h.extras.Ratings(ctx, []uuid.UUID{p.ID})[p.ID]; ok && rt.Count > 0 {
			doc["aggregateRating"] = map[string]any{
				"@type": "AggregateRating", "ratingValue": strconv.FormatFloat(rt.Average, 'f', 1, 64), "reviewCount": rt.Count,
			}
		}
	}
	return ui.Meta{
		Title: p.Name, Description: desc, Canonical: "/products/" + p.Slug, Image: image, Type: "product",
		JSONLD: ui.JSONLD(doc),
	}
}

func (h *Handler) fail(w nethttp.ResponseWriter, r *nethttp.Request, err error) {
	h.log.Error("catalog http", "path", r.URL.Path, "err", err)
	httpx.Render(w, r, nethttp.StatusInternalServerError, ui.ErrorPage(500, "Something went wrong on our side."))
}
