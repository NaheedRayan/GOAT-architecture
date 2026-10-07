// Package catalog is the public surface of the catalog module.
package catalog

import (
	"context"
	"io"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/catalog/domain"
)

type (
	Product      = domain.Product
	Category     = domain.Category
	CategoryStat = domain.CategoryStat
	Variant      = domain.Variant
	SitemapEntry = domain.SitemapEntry
	Image        = domain.Image
	VariantInfo  = domain.VariantInfo
	VariantInput = domain.VariantInput
	Filter       = domain.Filter
	Page         = domain.Page
	ProductInput = domain.ProductInput
)

var (
	ErrNotFound  = domain.ErrNotFound
	ErrSlugTaken = domain.ErrSlugTaken

	ErrCategoryExists = domain.ErrCategoryExists
	ErrCategoryGone   = domain.ErrCategoryGone

	ErrImageNotFound   = domain.ErrImageNotFound
	ErrUploadsDisabled = domain.ErrUploadsDisabled
	ErrVariantNotFound = domain.ErrVariantNotFound
	ErrSKUTaken        = domain.ErrSKUTaken
	ErrLabelTaken      = domain.ErrLabelTaken
	ErrNeedsLabel      = domain.ErrNeedsLabel
)

type ValidationError = domain.ValidationError

// SortOptions are the listing orderings shoppers can choose.
var SortOptions = domain.SortOptions

// Slugify turns a name into a URL slug ("" when it has no Latin letters or digits).
func Slugify(s string) string { return domain.Slugify(s) }

type API interface {
	List(ctx context.Context, f Filter) (Page, error)
	BySlug(ctx context.Context, slug string) (Product, error)
	ByID(ctx context.Context, id uuid.UUID) (Product, error)
	ByIDs(ctx context.Context, ids []uuid.UUID) ([]Product, error)
	Related(ctx context.Context, productID uuid.UUID, limit int) ([]Product, error)
	Sitemap(ctx context.Context) ([]SitemapEntry, error)
	VariantsByProducts(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]Variant, error)
	VariantInfos(ctx context.Context, variantIDs []uuid.UUID) ([]VariantInfo, error)
	AddImage(ctx context.Context, productID uuid.UUID, r io.Reader, alt string) (Image, error)
	RemoveImage(ctx context.Context, productID, imageID uuid.UUID) error
	MoveImage(ctx context.Context, productID, imageID uuid.UUID, delta int) error
	AddVariant(ctx context.Context, productID uuid.UUID, in VariantInput) (Variant, error)
	UpdateVariant(ctx context.Context, variantID uuid.UUID, in VariantInput) error
	Categories(ctx context.Context) ([]Category, error)
	ActiveCategories(ctx context.Context) ([]Category, error)
	CategoryStats(ctx context.Context) ([]CategoryStat, error)
	CreateProduct(ctx context.Context, in ProductInput) (Product, error)
	ArchiveProduct(ctx context.Context, id uuid.UUID) error
	RestoreProduct(ctx context.Context, id uuid.UUID) error
	UpdateProduct(ctx context.Context, id uuid.UUID, in ProductInput) error
	CreateCategory(ctx context.Context, name string) (Category, error)
	RenameCategory(ctx context.Context, id uuid.UUID, name string) error
	DeleteCategory(ctx context.Context, id uuid.UUID) error
}
