package catalog

import (
	"log/slog"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "github.com/NaheedRayan/goat-architecture/internal/catalog/adapters/http"
	mediaadapter "github.com/NaheedRayan/goat-architecture/internal/catalog/adapters/media"
	"github.com/NaheedRayan/goat-architecture/internal/catalog/adapters/postgres"
	"github.com/NaheedRayan/goat-architecture/internal/catalog/app"
	"github.com/NaheedRayan/goat-architecture/internal/platform/media"
)

// StockLookup is satisfied by the inventory module's API.
type StockLookup = httpadapter.StockLookup

// Extras and Rating are how the reviews and wishlist modules plug into product pages.
type (
	Extras = httpadapter.Extras
	Rating = httpadapter.Rating
)

type Options struct {
	Pool     *pgxpool.Pool
	Currency string
	Stock    StockLookup  // optional
	Media    *media.Store // optional: without it, image uploads are disabled
	Extras   Extras       // optional: reviews and wishlist
	Log      *slog.Logger
}

type Module struct {
	svc *app.Service
	h   *httpadapter.Handler
}

func New(o Options) *Module {
	var images app.ImageStore
	if o.Media != nil {
		images = mediaadapter.New(o.Media)
	}
	svc := app.NewService(postgres.New(o.Pool), o.Currency, images)
	return &Module{svc: svc, h: httpadapter.NewHandler(svc, o.Stock, o.Extras, o.Log)}
}

func (m *Module) API() API { return m.svc }

func (m *Module) Routes(r chi.Router) { m.h.Routes(r) }
