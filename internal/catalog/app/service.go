// Package app contains catalog use cases and ports.
package app

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/catalog/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/id"
)

type Repository interface {
	List(ctx context.Context, f domain.Filter) (products []domain.Product, total int, err error)
	BySlug(ctx context.Context, slug string) (domain.Product, error)
	ByID(ctx context.Context, id uuid.UUID) (domain.Product, error)
	ByIDs(ctx context.Context, ids []uuid.UUID) ([]domain.Product, error)
	Related(ctx context.Context, productID uuid.UUID, limit int) ([]domain.Product, error)
	Sitemap(ctx context.Context) ([]domain.SitemapEntry, error)
	Insert(ctx context.Context, p domain.Product, defaultVariant domain.Variant) error // ErrSlugTaken / ErrSKUTaken
	Update(ctx context.Context, p domain.Product) error                                // domain.ErrNotFound / ErrSlugTaken
	Categories(ctx context.Context) ([]domain.Category, error)
	ActiveCategories(ctx context.Context) ([]domain.Category, error)
	CategoryStats(ctx context.Context) ([]domain.CategoryStat, error)
	InsertCategory(ctx context.Context, c domain.Category) error
	RenameCategory(ctx context.Context, id uuid.UUID, name string) error
	DeleteCategory(ctx context.Context, id uuid.UUID) error
	ImagesByProducts(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]domain.Image, error)
	GetImage(ctx context.Context, productID, imageID uuid.UUID) (domain.Image, error)
	AddImage(ctx context.Context, img domain.Image) error
	RemoveImage(ctx context.Context, img domain.Image) (remainingRefs int, err error)
	ReorderImages(ctx context.Context, productID uuid.UUID, ids []uuid.UUID) error
	VariantsByProducts(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]domain.Variant, error)
	VariantInfos(ctx context.Context, ids []uuid.UUID) ([]domain.VariantInfo, error)
	GetVariant(ctx context.Context, id uuid.UUID) (domain.Variant, error)
	InsertVariant(ctx context.Context, v domain.Variant) error
	UpdateVariant(ctx context.Context, v domain.Variant) error
	RenameVariant(ctx context.Context, id uuid.UUID, label string) error
	Archive(ctx context.Context, id uuid.UUID) error
	Restore(ctx context.Context, id uuid.UUID) error
}

// ImageStore validates, processes and stores uploaded pictures. It returns
// domain.ValidationError for files a user can fix.
type ImageStore interface {
	Save(ctx context.Context, r io.Reader) (fullURL, thumbURL string, err error)
	Delete(ctx context.Context, urls ...string) error
}

type Service struct {
	repo     Repository
	currency string
	images   ImageStore // nil: uploads disabled
}

func NewService(repo Repository, currency string, images ImageStore) *Service {
	return &Service{repo: repo, currency: currency, images: images}
}

const (
	defaultPerPage = 12
	maxPerPage     = 60
)

func (s *Service) List(ctx context.Context, f domain.Filter) (domain.Page, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Page > 10000 { // keeps OFFSET far from int32 overflow
		f.Page = 10000
	}
	if !domain.ValidSort(f.Sort) {
		f.Sort = ""
	}
	f.MinPriceCents, f.MaxPriceCents = max(f.MinPriceCents, 0), max(f.MaxPriceCents, 0)
	if f.PerPage < 1 || f.PerPage > maxPerPage {
		f.PerPage = defaultPerPage
	}
	f.Query = strings.TrimSpace(f.Query)
	if r := []rune(f.Query); len(r) > 100 {
		f.Query = string(r[:100])
	}
	ps, total, err := s.repo.List(ctx, f)
	if err != nil {
		return domain.Page{}, err
	}
	return domain.Page{Products: ps, Total: total, Page: f.Page, PerPage: f.PerPage}, nil
}

func (s *Service) BySlug(ctx context.Context, slug string) (domain.Product, error) {
	p, err := s.repo.BySlug(ctx, slug)
	if err != nil {
		return p, err
	}
	return s.withVariants(ctx, p)
}

func (s *Service) ByID(ctx context.Context, pid uuid.UUID) (domain.Product, error) {
	p, err := s.repo.ByID(ctx, pid)
	if err != nil {
		return p, err
	}
	return s.withVariants(ctx, p)
}

func (s *Service) withVariants(ctx context.Context, p domain.Product) (domain.Product, error) {
	vs, err := s.repo.VariantsByProducts(ctx, []uuid.UUID{p.ID})
	if err != nil {
		return p, err
	}
	p.Variants = vs[p.ID]
	imgs, err := s.repo.ImagesByProducts(ctx, []uuid.UUID{p.ID})
	p.Images = imgs[p.ID]
	return p, err
}

// AddImage uploads a picture for a product. The first image becomes its cover.
func (s *Service) AddImage(ctx context.Context, productID uuid.UUID, r io.Reader, alt string) (domain.Image, error) {
	if s.images == nil {
		return domain.Image{}, domain.ErrUploadsDisabled
	}
	alt = strings.TrimSpace(alt)
	if len([]rune(alt)) > 200 {
		return domain.Image{}, domain.ValidationError("alt text is too long (200 characters max)")
	}
	p, err := s.repo.ByID(ctx, productID)
	if err != nil {
		return domain.Image{}, err
	}
	if p.Archived() {
		return domain.Image{}, domain.ErrNotFound
	}
	existing, err := s.repo.ImagesByProducts(ctx, []uuid.UUID{productID})
	if err != nil {
		return domain.Image{}, err
	}
	if len(existing[productID]) >= domain.MaxImagesPerProduct {
		return domain.Image{}, domain.ValidationError("a product can have at most 10 images")
	}
	full, thumb, err := s.images.Save(ctx, r)
	if err != nil {
		return domain.Image{}, err
	}
	img := domain.Image{ID: id.New(), ProductID: productID, URL: full, ThumbURL: thumb, Alt: alt, Position: len(existing[productID])}
	if err := s.repo.AddImage(ctx, img); err != nil {
		return domain.Image{}, err
	}
	return img, nil
}

// RemoveImage deletes a picture, and its files when no other product uses them.
func (s *Service) RemoveImage(ctx context.Context, productID, imageID uuid.UUID) error {
	img, err := s.repo.GetImage(ctx, productID, imageID)
	if err != nil {
		return err
	}
	refs, err := s.repo.RemoveImage(ctx, img)
	if err != nil {
		return err
	}
	if refs == 0 && s.images != nil {
		_ = s.images.Delete(ctx, img.URL, img.ThumbURL) // best effort: an orphan file is harmless
	}
	return nil
}

// MoveImage shifts a picture earlier (delta < 0) or later (delta > 0) in the gallery.
func (s *Service) MoveImage(ctx context.Context, productID, imageID uuid.UUID, delta int) error {
	all, err := s.repo.ImagesByProducts(ctx, []uuid.UUID{productID})
	if err != nil {
		return err
	}
	imgs := all[productID]
	at := -1
	for i, im := range imgs {
		if im.ID == imageID {
			at = i
		}
	}
	if at < 0 {
		return domain.ErrImageNotFound
	}
	to := at + delta
	if to < 0 || to >= len(imgs) {
		return nil // already at the edge
	}
	imgs[at], imgs[to] = imgs[to], imgs[at]
	ids := make([]uuid.UUID, len(imgs))
	for i, im := range imgs {
		ids[i] = im.ID
	}
	return s.repo.ReorderImages(ctx, productID, ids)
}

// VariantsByProducts returns each product's variants in display order.
func (s *Service) VariantsByProducts(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]domain.Variant, error) {
	if len(ids) == 0 {
		return map[uuid.UUID][]domain.Variant{}, nil
	}
	return s.repo.VariantsByProducts(ctx, ids)
}

// VariantInfos resolves variant ids to variant+product data (for carts and orders).
func (s *Service) VariantInfos(ctx context.Context, ids []uuid.UUID) ([]domain.VariantInfo, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return s.repo.VariantInfos(ctx, ids)
}

// AddVariant adds a purchasable form (size, colour...) to a product.
func (s *Service) AddVariant(ctx context.Context, productID uuid.UUID, in domain.VariantInput) (domain.Variant, error) {
	in.Label, in.SKU = strings.Join(strings.Fields(in.Label), " "), strings.TrimSpace(in.SKU)
	if err := in.Validate(); err != nil {
		return domain.Variant{}, err
	}
	if in.Label == "" {
		return domain.Variant{}, domain.ErrNeedsLabel
	}
	if _, err := s.repo.ByID(ctx, productID); err != nil {
		return domain.Variant{}, err
	}
	existing, err := s.repo.VariantsByProducts(ctx, []uuid.UUID{productID})
	if err != nil {
		return domain.Variant{}, err
	}
	vs := existing[productID]
	if len(vs) >= domain.MaxVariantsPerProduct {
		return domain.Variant{}, domain.ValidationError("a product can have at most 100 variants")
	}
	if len(vs) == 1 && vs[0].Label == "" {
		// The product was single-form; now it needs names. Avoid clashing with the new label.
		name := "Default"
		if strings.EqualFold(in.Label, name) {
			name = "Standard"
		}
		if err := s.repo.RenameVariant(ctx, vs[0].ID, name); err != nil {
			return domain.Variant{}, err
		}
	}
	v := domain.Variant{ID: id.New(), ProductID: productID, SKU: in.SKU, Label: in.Label, PriceCents: in.PriceCents, Position: len(vs), Active: in.Active}
	return v, s.repo.InsertVariant(ctx, v)
}

// UpdateVariant edits a variant. Deactivating hides it from shoppers; it is never
// deleted, because carts and past orders reference it.
func (s *Service) UpdateVariant(ctx context.Context, variantID uuid.UUID, in domain.VariantInput) error {
	in.Label, in.SKU = strings.Join(strings.Fields(in.Label), " "), strings.TrimSpace(in.SKU)
	if err := in.Validate(); err != nil {
		return err
	}
	cur, err := s.repo.GetVariant(ctx, variantID)
	if err != nil {
		return err
	}
	if in.Label == "" {
		siblings, err := s.repo.VariantsByProducts(ctx, []uuid.UUID{cur.ProductID})
		if err != nil {
			return err
		}
		if len(siblings[cur.ProductID]) > 1 {
			return domain.ErrNeedsLabel
		}
	}
	return s.repo.UpdateVariant(ctx, domain.Variant{ID: variantID, SKU: in.SKU, Label: in.Label, PriceCents: in.PriceCents, Active: in.Active})
}

func (s *Service) ByIDs(ctx context.Context, ids []uuid.UUID) ([]domain.Product, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return s.repo.ByIDs(ctx, ids)
}

// Categories lists every category (for admin pickers).
func (s *Service) Categories(ctx context.Context) ([]domain.Category, error) {
	return s.repo.Categories(ctx)
}

// ActiveCategories lists only categories that contain at least one visible product.
func (s *Service) ActiveCategories(ctx context.Context) ([]domain.Category, error) {
	return s.repo.ActiveCategories(ctx)
}

func (s *Service) CategoryStats(ctx context.Context) ([]domain.CategoryStat, error) {
	return s.repo.CategoryStats(ctx)
}

func (s *Service) CreateProduct(ctx context.Context, in domain.ProductInput) (domain.Product, error) {
	if err := in.Validate(); err != nil {
		return domain.Product{}, err
	}
	p, auto := s.build(id.New(), in)
	// A product always has a variant; a single-form product has one unnamed default.
	def := domain.Variant{ID: p.ID, ProductID: p.ID, SKU: strings.TrimSpace(in.SKU), Active: true}
	err := s.repo.Insert(ctx, p, def)
	if errors.Is(err, domain.ErrSlugTaken) && auto {
		// Two products may share a name; only an explicitly typed slug is a conflict.
		p.Slug = p.Slug + "-" + shortID(p.ID)
		err = s.repo.Insert(ctx, p, def)
	}
	if err != nil {
		return domain.Product{}, err
	}
	p.Variants = []domain.Variant{def}
	return p, nil
}

func (s *Service) UpdateProduct(ctx context.Context, pid uuid.UUID, in domain.ProductInput) error {
	if err := in.Validate(); err != nil {
		return err
	}
	p, auto := s.build(pid, in)
	err := s.repo.Update(ctx, p)
	if errors.Is(err, domain.ErrSlugTaken) && auto {
		p.Slug = p.Slug + "-" + shortID(p.ID)
		err = s.repo.Update(ctx, p)
	}
	return err
}

func (s *Service) CreateCategory(ctx context.Context, name string) (domain.Category, error) {
	name, err := validCategoryName(name)
	if err != nil {
		return domain.Category{}, err
	}
	c := domain.Category{ID: id.New(), Slug: domain.Slugify(name), Name: name}
	if c.Slug == "" { // e.g. a name written entirely in a non-Latin script
		c.Slug = "category-" + shortID(c.ID)
	}
	err = s.repo.InsertCategory(ctx, c)
	if errors.Is(err, domain.ErrSlugTaken) {
		// Distinct names can share a slug ("Rain Gear" / "Rain-Gear", or non-Latin
		// names with the same ASCII remainder). The name is free, so just make the slug unique.
		c.Slug += "-" + shortID(c.ID)
		err = s.repo.InsertCategory(ctx, c)
	}
	return c, err
}

// RenameCategory changes the display name only; the slug (and so shop URLs) stays stable.
func (s *Service) RenameCategory(ctx context.Context, cid uuid.UUID, name string) error {
	name, err := validCategoryName(name)
	if err != nil {
		return err
	}
	return s.repo.RenameCategory(ctx, cid, name)
}

// DeleteCategory removes a category; its products stay, uncategorised.
func (s *Service) DeleteCategory(ctx context.Context, cid uuid.UUID) error {
	return s.repo.DeleteCategory(ctx, cid)
}

func validCategoryName(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ") // trim and collapse whitespace
	switch {
	case name == "":
		return "", domain.ValidationError("category name is required")
	case len([]rune(name)) > domain.MaxCategoryLen:
		return "", domain.ValidationError("category name is too long (60 characters max)")
	}
	return name, nil
}

// build returns the product and whether its slug was derived (rather than typed).
func (s *Service) build(pid uuid.UUID, in domain.ProductInput) (domain.Product, bool) {
	slug := domain.Slugify(in.Slug)
	auto := slug == ""
	if auto {
		slug = domain.Slugify(in.Name)
	}
	if slug == "" { // name has no Latin letters or digits
		slug = "item-" + shortID(pid)
	}
	return domain.Product{
		ID: pid, CategoryID: in.CategoryID, Slug: slug, Name: strings.TrimSpace(in.Name),
		Description: strings.TrimSpace(in.Description), PriceCents: in.PriceCents, Currency: s.currency,
		ImageURL: strings.TrimSpace(in.ImageURL), Active: in.Active, OptionName: strings.TrimSpace(in.OptionName),
	}, auto
}

// shortID is the random tail of a UUIDv7, handy for unique slug suffixes.
func shortID(u uuid.UUID) string { return strings.ReplaceAll(u.String(), "-", "")[24:32] }

// ArchiveProduct removes a product from the store. Existing orders keep their
// price/name snapshots and carts silently drop the line.
func (s *Service) ArchiveProduct(ctx context.Context, pid uuid.UUID) error {
	return s.repo.Archive(ctx, pid)
}

// RestoreProduct un-archives a product; it comes back hidden until published.
func (s *Service) RestoreProduct(ctx context.Context, pid uuid.UUID) error {
	return s.repo.Restore(ctx, pid)
}

// Related returns other live products from the same category.
func (s *Service) Related(ctx context.Context, productID uuid.UUID, limit int) ([]domain.Product, error) {
	return s.repo.Related(ctx, productID, min(max(limit, 1), 12))
}

// Sitemap lists every live product for the sitemap.
func (s *Service) Sitemap(ctx context.Context) ([]domain.SitemapEntry, error) {
	return s.repo.Sitemap(ctx)
}
