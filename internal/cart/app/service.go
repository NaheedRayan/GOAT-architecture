// Package app contains cart use cases and ports.
package app

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/cart/domain"
	"github.com/NaheedRayan/goat-architecture/internal/platform/id"
)

type Repository interface {
	CartID(ctx context.Context, owner string) (cartID uuid.UUID, found bool, err error)
	GetOrCreate(ctx context.Context, owner string) (uuid.UUID, error)
	Items(ctx context.Context, cartID uuid.UUID) ([]domain.Item, error)
	SetItem(ctx context.Context, cartID, variantID uuid.UUID, qty int) error
	AddItem(ctx context.Context, cartID, variantID uuid.UUID, qty int) error // atomic, clamps at MaxQuantity
	Merge(ctx context.Context, from, to string) error                        // atomic
	PurgeGuests(ctx context.Context, before time.Time) (int64, error)
	ClaimAbandoned(ctx context.Context, idleBefore, notOlderThan time.Time, limit int) ([]domain.Abandoned, error)
	DeleteItem(ctx context.Context, cartID, variantID uuid.UUID) error
	Clear(ctx context.Context, cartID uuid.UUID) error
	DeleteCart(ctx context.Context, owner string) error
	Count(ctx context.Context, owner string) (int, error)
}

// VariantInfo is the slice of catalog data the cart needs about a variant.
type VariantInfo struct {
	VariantID  uuid.UUID
	ProductID  uuid.UUID
	Slug       string
	Name       string
	Label      string
	SKU        string
	ImageURL   string
	PriceCents int64
	Currency   string
	Active     bool
}

type VariantLookup interface {
	ByIDs(ctx context.Context, variantIDs []uuid.UUID) ([]VariantInfo, error)
}

type Service struct {
	repo     Repository
	variants VariantLookup
}

func NewService(repo Repository, variants VariantLookup) *Service {
	return &Service{repo: repo, variants: variants}
}

func (s *Service) Add(ctx context.Context, owner string, variantID uuid.UUID, qty int) error {
	if qty < 1 {
		return domain.ErrInvalidQuantity
	}
	ps, err := s.variants.ByIDs(ctx, []uuid.UUID{variantID})
	if err != nil {
		return err
	}
	if len(ps) == 0 || !ps[0].Active {
		return domain.ErrProductUnavailable
	}
	cartID, err := s.repo.GetOrCreate(ctx, owner)
	if err != nil {
		return err
	}
	items, err := s.repo.Items(ctx, cartID)
	if err != nil {
		return err
	}
	if len(items) >= domain.MaxLines {
		inCart := false
		for _, it := range items {
			inCart = inCart || it.VariantID == variantID
		}
		if !inCart {
			return domain.ErrCartFull
		}
	}
	// A single atomic upsert: two concurrent adds both count.
	return s.repo.AddItem(ctx, cartID, variantID, domain.ClampQuantity(qty))
}

// SetQuantity replaces a line's quantity; zero removes it.
func (s *Service) SetQuantity(ctx context.Context, owner string, variantID uuid.UUID, qty int) error {
	if qty < 0 {
		return domain.ErrInvalidQuantity
	}
	cartID, found, err := s.repo.CartID(ctx, owner)
	if err != nil || !found {
		return err
	}
	if qty == 0 {
		return s.repo.DeleteItem(ctx, cartID, variantID)
	}
	return s.repo.SetItem(ctx, cartID, variantID, domain.ClampQuantity(qty))
}

func (s *Service) Remove(ctx context.Context, owner string, variantID uuid.UUID) error {
	return s.SetQuantity(ctx, owner, variantID, 0)
}

// View prices the cart at current catalog prices. Lines whose product was
// deactivated or deleted are left out.
func (s *Service) View(ctx context.Context, owner string) (domain.View, error) {
	cartID, found, err := s.repo.CartID(ctx, owner)
	if err != nil || !found {
		return domain.View{}, err
	}
	items, err := s.repo.Items(ctx, cartID)
	if err != nil || len(items) == 0 {
		return domain.View{}, err
	}
	ids := make([]uuid.UUID, len(items))
	for i, it := range items {
		ids[i] = it.VariantID
	}
	ps, err := s.variants.ByIDs(ctx, ids)
	if err != nil {
		return domain.View{}, err
	}
	byID := make(map[uuid.UUID]VariantInfo, len(ps))
	for _, p := range ps {
		byID[p.VariantID] = p
	}
	var v domain.View
	for _, it := range items {
		p, ok := byID[it.VariantID]
		if !ok || !p.Active {
			continue
		}
		v.Lines = append(v.Lines, domain.Line{
			VariantID: p.VariantID, ProductID: p.ProductID, Slug: p.Slug, Name: p.Name, Label: p.Label, SKU: p.SKU, ImageURL: p.ImageURL,
			UnitPriceCents: p.PriceCents, Currency: p.Currency, Quantity: it.Quantity,
		})
		v.TotalCents += p.PriceCents * int64(it.Quantity)
		v.Count += it.Quantity
		v.Currency = p.Currency
	}
	sort.Slice(v.Lines, func(i, j int) bool { return v.Lines[i].Name < v.Lines[j].Name })
	return v, nil
}

func (s *Service) Count(ctx context.Context, owner string) (int, error) {
	return s.repo.Count(ctx, owner)
}

func (s *Service) Clear(ctx context.Context, owner string) error {
	cartID, found, err := s.repo.CartID(ctx, owner)
	if err != nil || !found {
		return err
	}
	return s.repo.Clear(ctx, cartID)
}

// Merge moves a guest cart into a user cart (after sign-in), summing quantities.
// It is atomic and safe to call from parallel requests.
func (s *Service) Merge(ctx context.Context, from, to string) error {
	return s.repo.Merge(ctx, from, to)
}

// PurgeStaleGuests deletes anonymous carts untouched for 30 days.
func (s *Service) PurgeStaleGuests(ctx context.Context) (int64, error) {
	return s.repo.PurgeGuests(ctx, time.Now().Add(-30*24*time.Hour))
}

// DeleteUserCart removes a deleted account's cart.
func (s *Service) DeleteUserCart(ctx context.Context, userID uuid.UUID) error {
	return s.repo.DeleteCart(ctx, domain.UserOwner(userID))
}

// NewGuestID returns a fresh identifier for an anonymous cart cookie.
func NewGuestID() uuid.UUID { return id.New() }

// ClaimAbandoned finds signed-in users' carts that have sat idle for idleFor (but not
// longer than maxAge), marks them as reminded and returns them. Call it inside the
// transaction that queues the reminder emails, so a cart is reminded exactly once.
func (s *Service) ClaimAbandoned(ctx context.Context, idleFor, maxAge time.Duration, limit int) ([]domain.Abandoned, error) {
	now := time.Now()
	return s.repo.ClaimAbandoned(ctx, now.Add(-idleFor), now.Add(-maxAge), limit)
}
