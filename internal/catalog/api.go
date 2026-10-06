// Package catalog is the public surface of the catalog module.
package catalog

import (
	"context"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/catalog/domain"
)

type (
	Product      = domain.Product
	Category     = domain.Category
	CategoryStat = domain.CategoryStat
	Variant      = domain.Variant
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

	ErrVariantNotFound = domain.ErrVariantNotFound
	ErrSKUTaken        = domain.ErrSKUTaken
	ErrLabelTaken      = domain.ErrLabelTaken
	ErrNeedsLabel      = domain.ErrNeedsLabel
)

type ValidationError = domain.ValidationError

type API interface {
	List(ctx context.Context, f Filter) (Page, error)
	BySlug(ctx context.Context, slug string) (Product, error)
	ByID(ctx context.Context, id uuid.UUID) (Product, error)
	ByIDs(ctx context.Context, ids []uuid.UUID) ([]Product, error)
	VariantsByProducts(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]Variant, error)
	VariantInfos(ctx context.Context, variantIDs []uuid.UUID) ([]VariantInfo, error)
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
