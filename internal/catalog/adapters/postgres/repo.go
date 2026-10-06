// Package postgres implements the catalog repository with sqlc-generated queries.
package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/catalog/adapters/postgres/sqlcgen"
	"github.com/NaheedRayan/goat-architecture/internal/catalog/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/db"
)

type Repo struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) q(ctx context.Context) *sqlcgen.Queries { return sqlcgen.New(db.Q(ctx, r.pool)) }

func toProduct(p sqlcgen.CatalogProduct) domain.Product {
	return domain.Product{ID: p.ID, CategoryID: p.CategoryID, Slug: p.Slug, Name: p.Name, Description: p.Description,
		PriceCents: p.PriceCents, Currency: p.Currency, ImageURL: p.ImageUrl, Active: p.Active, CreatedAt: p.CreatedAt,
		ArchivedAt: p.ArchivedAt, OptionName: p.OptionName}
}

func (r *Repo) List(ctx context.Context, f domain.Filter) ([]domain.Product, int, error) {
	rows, err := r.q(ctx).ListProducts(ctx, sqlcgen.ListProductsParams{
		LikePattern: likePattern(f.Query), Archived: f.Archived, IncludeInactive: f.IncludeInactive, Query: f.Query, CategorySlug: f.CategorySlug,
		PageLimit: int32(f.PerPage), PageOffset: int32((f.Page - 1) * f.PerPage),
	})
	if err != nil {
		return nil, 0, err
	}
	out := make([]domain.Product, len(rows))
	total := 0
	for i, p := range rows {
		out[i] = domain.Product{ID: p.ID, CategoryID: p.CategoryID, Slug: p.Slug, Name: p.Name, Description: p.Description,
			PriceCents: p.PriceCents, Currency: p.Currency, ImageURL: p.ImageUrl, Active: p.Active, CreatedAt: p.CreatedAt,
			ArchivedAt: p.ArchivedAt, OptionName: p.OptionName, MinPriceCents: p.MinPrice, MaxPriceCents: p.MaxPrice}
		total = int(p.Total)
	}
	if len(rows) == 0 && f.Page > 1 {
		// Page past the end: report no rows but keep a sane total of 0.
		total = 0
	}
	return out, total, nil
}

func (r *Repo) BySlug(ctx context.Context, slug string) (domain.Product, error) {
	p, err := r.q(ctx).GetProductBySlug(ctx, slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Product{}, domain.ErrNotFound
	}
	return toProduct(p), err
}

func (r *Repo) ByID(ctx context.Context, pid uuid.UUID) (domain.Product, error) {
	p, err := r.q(ctx).GetProductByID(ctx, pid)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Product{}, domain.ErrNotFound
	}
	return toProduct(p), err
}

func (r *Repo) ByIDs(ctx context.Context, ids []uuid.UUID) ([]domain.Product, error) {
	rows, err := r.q(ctx).GetProductsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Product, len(rows))
	for i, p := range rows {
		out[i] = toProduct(p)
	}
	return out, nil
}

// Insert stores a product together with its default variant, atomically.
func (r *Repo) Insert(ctx context.Context, p domain.Product, def domain.Variant) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := sqlcgen.New(tx)
	err = q.InsertProduct(ctx, sqlcgen.InsertProductParams{
		ID: p.ID, CategoryID: p.CategoryID, Slug: p.Slug, Name: p.Name, Description: p.Description,
		PriceCents: p.PriceCents, Currency: p.Currency, ImageUrl: p.ImageURL, Active: p.Active, OptionName: p.OptionName,
	})
	if err == nil {
		err = q.InsertVariant(ctx, sqlcgen.InsertVariantParams{
			ID: def.ID, ProductID: def.ProductID, Sku: def.SKU, Label: def.Label, PriceCents: def.PriceCents, Position: int32(def.Position), Active: def.Active,
		})
	}
	if err != nil {
		return mapUnique(err)
	}
	if fkErr := db.IsForeignKeyViolation(err); fkErr {
		return domain.ErrCategoryGone
	}
	return tx.Commit(ctx)
}

// mapUnique translates the unique constraint that fired into a domain error.
func mapUnique(err error) error {
	switch db.UniqueViolationConstraint(err) {
	case "":
		if db.IsForeignKeyViolation(err) {
			return domain.ErrCategoryGone
		}
		return err
	case "variants_sku_key":
		return domain.ErrSKUTaken
	case "variants_label_key":
		return domain.ErrLabelTaken
	default:
		return domain.ErrSlugTaken
	}
}

func (r *Repo) Update(ctx context.Context, p domain.Product) error {
	n, err := r.q(ctx).UpdateProduct(ctx, sqlcgen.UpdateProductParams{
		ID: p.ID, CategoryID: p.CategoryID, Slug: p.Slug, Name: p.Name, Description: p.Description,
		PriceCents: p.PriceCents, ImageUrl: p.ImageURL, Active: p.Active, OptionName: p.OptionName,
	})
	if err != nil {
		return mapUnique(err)
	}
	if err == nil && n == 0 {
		return domain.ErrNotFound
	}
	return err
}

func (r *Repo) Categories(ctx context.Context) ([]domain.Category, error) {
	rows, err := r.q(ctx).ListCategories(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Category, len(rows))
	for i, c := range rows {
		out[i] = domain.Category{ID: c.ID, Slug: c.Slug, Name: c.Name}
	}
	return out, nil
}

func (r *Repo) ActiveCategories(ctx context.Context) ([]domain.Category, error) {
	rows, err := r.q(ctx).ListActiveCategories(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Category, len(rows))
	for i, c := range rows {
		out[i] = domain.Category{ID: c.ID, Slug: c.Slug, Name: c.Name}
	}
	return out, nil
}

func (r *Repo) CategoryStats(ctx context.Context) ([]domain.CategoryStat, error) {
	rows, err := r.q(ctx).ListCategoryStats(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.CategoryStat, len(rows))
	for i, c := range rows {
		out[i] = domain.CategoryStat{Category: domain.Category{ID: c.ID, Slug: c.Slug, Name: c.Name}, ProductCount: int(c.ProductCount)}
	}
	return out, nil
}

func (r *Repo) InsertCategory(ctx context.Context, c domain.Category) error {
	err := r.q(ctx).InsertCategory(ctx, sqlcgen.InsertCategoryParams{ID: c.ID, Slug: c.Slug, Name: c.Name})
	switch db.UniqueViolationConstraint(err) {
	case "":
		return err
	case "categories_slug_key":
		return domain.ErrSlugTaken // different name, same derived slug: caller can pick another slug
	default:
		return domain.ErrCategoryExists
	}
}

func (r *Repo) RenameCategory(ctx context.Context, cid uuid.UUID, name string) error {
	n, err := r.q(ctx).RenameCategory(ctx, sqlcgen.RenameCategoryParams{ID: cid, Name: name})
	if db.IsUniqueViolation(err) {
		return domain.ErrCategoryExists
	}
	if err == nil && n == 0 {
		return domain.ErrNotFound
	}
	return err
}

func (r *Repo) DeleteCategory(ctx context.Context, cid uuid.UUID) error {
	n, err := r.q(ctx).DeleteCategory(ctx, cid)
	if err == nil && n == 0 {
		return domain.ErrNotFound
	}
	return err
}

func (r *Repo) Archive(ctx context.Context, pid uuid.UUID) error {
	n, err := r.q(ctx).ArchiveProduct(ctx, pid)
	if err == nil && n == 0 {
		return domain.ErrNotFound
	}
	return err
}

func (r *Repo) Restore(ctx context.Context, pid uuid.UUID) error {
	n, err := r.q(ctx).RestoreProduct(ctx, pid)
	if err == nil && n == 0 {
		return domain.ErrNotFound
	}
	return err
}

// likePattern turns user input into a contains-pattern for ILIKE, escaping the
// wildcard characters so "50%" or "a_b" match literally. This is what lets
// partial words ("tra" for "Trail") match, which full-text search cannot.
func likePattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(q) + "%"
}

func toVariant(v sqlcgen.CatalogVariant) domain.Variant {
	return domain.Variant{ID: v.ID, ProductID: v.ProductID, SKU: v.Sku, Label: v.Label, PriceCents: v.PriceCents, Position: int(v.Position), Active: v.Active}
}

func (r *Repo) VariantsByProducts(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]domain.Variant, error) {
	rows, err := r.q(ctx).ListVariantsByProducts(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID][]domain.Variant{}
	for _, v := range rows {
		out[v.ProductID] = append(out[v.ProductID], toVariant(v))
	}
	return out, nil
}

func (r *Repo) VariantInfos(ctx context.Context, ids []uuid.UUID) ([]domain.VariantInfo, error) {
	rows, err := r.q(ctx).GetVariantInfos(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]domain.VariantInfo, len(rows))
	for i, x := range rows {
		out[i] = domain.VariantInfo{VariantID: x.VariantID, ProductID: x.ProductID, ProductName: x.ProductName, ProductSlug: x.ProductSlug,
			ImageURL: x.ImageUrl, SKU: x.Sku, Label: x.Label, PriceCents: x.PriceCents, Currency: x.Currency, Active: x.Active}
	}
	return out, nil
}

func (r *Repo) GetVariant(ctx context.Context, id uuid.UUID) (domain.Variant, error) {
	v, err := r.q(ctx).GetVariant(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Variant{}, domain.ErrVariantNotFound
	}
	return toVariant(v), err
}

func (r *Repo) InsertVariant(ctx context.Context, v domain.Variant) error {
	err := r.q(ctx).InsertVariant(ctx, sqlcgen.InsertVariantParams{
		ID: v.ID, ProductID: v.ProductID, Sku: v.SKU, Label: v.Label, PriceCents: v.PriceCents, Position: int32(v.Position), Active: v.Active,
	})
	if err != nil {
		return mapUnique(err)
	}
	return nil
}

func (r *Repo) UpdateVariant(ctx context.Context, v domain.Variant) error {
	n, err := r.q(ctx).UpdateVariant(ctx, sqlcgen.UpdateVariantParams{ID: v.ID, Sku: v.SKU, Label: v.Label, PriceCents: v.PriceCents, Active: v.Active})
	if err != nil {
		return mapUnique(err)
	}
	if n == 0 {
		return domain.ErrVariantNotFound
	}
	return nil
}

func (r *Repo) RenameVariant(ctx context.Context, id uuid.UUID, label string) error {
	return mapUnique(r.q(ctx).RenameVariantLabel(ctx, sqlcgen.RenameVariantLabelParams{ID: id, Label: label}))
}
