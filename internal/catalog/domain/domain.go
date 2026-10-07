// Package domain holds catalog entities and rules.
package domain

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Product struct {
	ID          uuid.UUID
	CategoryID  *uuid.UUID
	Slug        string
	Name        string
	Description string
	PriceCents  int64
	Currency    string
	ImageURL    string
	Active      bool
	CreatedAt   time.Time
	ArchivedAt  *time.Time
	OptionName  string // what the variants vary by, e.g. "Size"
	ThumbURL    string // card-sized cover image ("" when none)
	Images      []Image

	// MinPriceCents/MaxPriceCents span the active variants (equal to PriceCents when
	// there is a single price). Filled by List; zero elsewhere.
	MinPriceCents int64
	MaxPriceCents int64
	// Variants is filled when a single product is loaded.
	Variants []Variant
}

// Image is an uploaded product picture. The first image is the cover.
type Image struct {
	ID        uuid.UUID
	ProductID uuid.UUID
	URL       string
	ThumbURL  string
	Alt       string
	Position  int
}

const MaxImagesPerProduct = 10

// Variant is one purchasable form of a product (a size, a colour...).
type Variant struct {
	ID         uuid.UUID
	ProductID  uuid.UUID
	SKU        string
	Label      string // "" for a product's only variant
	PriceCents *int64 // nil: use the product price
	Position   int
	Active     bool
}

// EffectivePrice is the variant's own price, or the product's when it has none.
func (v Variant) EffectivePrice(p Product) int64 {
	if v.PriceCents != nil {
		return *v.PriceCents
	}
	return p.PriceCents
}

// VariantInfo is a variant joined with its product, for carts, checkout and orders.
type VariantInfo struct {
	VariantID   uuid.UUID
	ProductID   uuid.UUID
	ProductName string
	ProductSlug string
	ImageURL    string
	SKU         string
	Label       string
	PriceCents  int64
	Currency    string
	Active      bool // variant and product both live and not archived
}

type VariantInput struct {
	SKU        string
	Label      string
	PriceCents *int64
	Active     bool
}

func (p Product) Archived() bool { return p.ArchivedAt != nil }

type Category struct {
	ID   uuid.UUID
	Slug string
	Name string
}

// CategoryStat is a category with the number of products filed under it.
type CategoryStat struct {
	Category
	ProductCount int
}

type Filter struct {
	Query           string
	CategorySlug    string
	Page            int
	PerPage         int
	IncludeInactive bool
	Archived        bool   // list archived products instead of live ones
	Sort            string // "", "newest", "price_asc", "price_desc", "name"; "" ranks by relevance when searching
	MinPriceCents   int64  // 0 = no lower bound
	MaxPriceCents   int64  // 0 = no upper bound
}

type Page struct {
	Products []Product
	Total    int
	Page     int
	PerPage  int
}

func (p Page) TotalPages() int {
	if p.PerPage <= 0 {
		return 1
	}
	return max(1, (p.Total+p.PerPage-1)/p.PerPage)
}

type ProductInput struct {
	CategoryID  *uuid.UUID
	Slug        string
	Name        string
	Description string
	PriceCents  int64
	ImageURL    string
	Active      bool
	SKU         string // SKU of the default variant (on create)
	OptionName  string
}

var (
	ErrNotFound       = errors.New("not found")
	ErrSlugTaken      = errors.New("that URL slug is already used by another product")
	ErrCategoryExists = errors.New("a category with that name already exists")
	ErrCategoryGone   = errors.New("that category no longer exists")

	ErrImageNotFound   = errors.New("image not found")
	ErrUploadsDisabled = errors.New("image uploads are not configured on this server")
	ErrVariantNotFound = errors.New("variant not found")
	ErrSKUTaken        = errors.New("that SKU is already used by another variant")
	ErrLabelTaken      = errors.New("this product already has a variant with that name")
	ErrNeedsLabel      = errors.New("a variant needs a name when the product has several")
)

const (
	MaxNameLen        = 200
	MaxCategoryLen    = 60
	MaxDescriptionLen = 5000
)

type ValidationError string

func (e ValidationError) Error() string { return string(e) }

func (in ProductInput) Validate() error {
	switch {
	case strings.TrimSpace(in.Name) == "":
		return ValidationError("name is required")
	case len([]rune(in.OptionName)) > 40:
		return ValidationError("option name is too long (40 characters max)")
	case len([]rune(in.Name)) > MaxNameLen:
		return ValidationError("name is too long (200 characters max)")
	case len([]rune(in.Description)) > MaxDescriptionLen:
		return ValidationError("description is too long (5000 characters max)")
	case in.PriceCents < 0:
		return ValidationError("price cannot be negative")
	case !validImageURL(in.ImageURL):
		return ValidationError("image URL must be an http(s) link or a path on this site such as /static/products/x.svg")
	}
	return nil
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func Slugify(s string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// validImageURL accepts an absolute http(s) URL with a host, or a site-local path.
// Protocol-relative ("//host/x") and non-http schemes (javascript:, data:) are refused.
func validImageURL(s string) bool {
	if s == "" {
		return true
	}
	if strings.HasPrefix(s, "/") {
		return !strings.HasPrefix(s, "//") && !strings.Contains(s, "\\")
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

const MaxVariantsPerProduct = 100

func (in VariantInput) Validate() error {
	switch {
	case len([]rune(strings.TrimSpace(in.Label))) > 80:
		return ValidationError("variant name is too long (80 characters max)")
	case len(in.SKU) > 64:
		return ValidationError("SKU is too long (64 characters max)")
	case in.PriceCents != nil && *in.PriceCents < 0:
		return ValidationError("price cannot be negative")
	}
	return nil
}

// SortOptions are the orderings a shopper can choose on the listing page.
var SortOptions = []struct{ Value, Label string }{
	{"", "Best match"}, {"newest", "Newest"}, {"price_asc", "Price: low to high"}, {"price_desc", "Price: high to low"}, {"name", "Name: A to Z"},
}

// ValidSort reports whether s is a known ordering.
func ValidSort(s string) bool {
	for _, o := range SortOptions {
		if o.Value == s {
			return true
		}
	}
	return false
}

// SitemapEntry is a live product's URL slug and when it was added.
type SitemapEntry struct {
	Slug      string
	CreatedAt time.Time
}
