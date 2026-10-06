package cart

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "github.com/NaheedRayan/goat-architecture/internal/cart/adapters/http"
	"github.com/NaheedRayan/goat-architecture/internal/cart/adapters/postgres"
	"github.com/NaheedRayan/goat-architecture/internal/cart/app"
	"github.com/NaheedRayan/goat-architecture/internal/catalog"
)

type Options struct {
	Pool          *pgxpool.Pool
	Catalog       catalog.API
	SecureCookies bool
	Log           *slog.Logger
}

type Module struct {
	svc *app.Service
	h   *httpadapter.Handler
	log *slog.Logger
}

func New(o Options) *Module {
	svc := app.NewService(postgres.New(o.Pool), catalogLookup{o.Catalog})
	return &Module{svc: svc, h: httpadapter.NewHandler(svc, o.SecureCookies, o.Log), log: o.Log}
}

func (m *Module) API() API { return apiImpl{m.svc} }

func (m *Module) Routes(r chi.Router) { m.h.Routes(r) }

// Middleware must run after authentication.
func (m *Module) Middleware(next http.Handler) http.Handler { return m.h.Middleware(next) }

type apiImpl struct{ s *app.Service }

func (a apiImpl) View(ctx context.Context, owner string) (View, error) { return a.s.View(ctx, owner) }
func (a apiImpl) Clear(ctx context.Context, owner string) error        { return a.s.Clear(ctx, owner) }

// catalogLookup adapts the catalog module's API to the cart's own port.
type catalogLookup struct{ api catalog.API }

func (c catalogLookup) ByIDs(ctx context.Context, ids []uuid.UUID) ([]app.VariantInfo, error) {
	vs, err := c.api.VariantInfos(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]app.VariantInfo, len(vs))
	for i, v := range vs {
		out[i] = app.VariantInfo{VariantID: v.VariantID, ProductID: v.ProductID, Slug: v.ProductSlug, Name: v.ProductName,
			Label: v.Label, SKU: v.SKU, ImageURL: v.ImageURL, PriceCents: v.PriceCents, Currency: v.Currency, Active: v.Active}
	}
	return out, nil
}

// Background deletes abandoned guest carts daily-ish (hourly check) until ctx is cancelled.
func (m *Module) Background(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n, err := m.svc.PurgeStaleGuests(ctx); err != nil && ctx.Err() == nil {
			m.log.Error("purge guest carts", "err", err)
		} else if n > 0 {
			m.log.Info("purged guest carts", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
