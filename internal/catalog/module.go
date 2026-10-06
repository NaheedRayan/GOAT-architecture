package catalog

import (
	"log/slog"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "github.com/NaheedRayan/goat-architecture/internal/catalog/adapters/http"
	"github.com/NaheedRayan/goat-architecture/internal/catalog/adapters/postgres"
	"github.com/NaheedRayan/goat-architecture/internal/catalog/app"
)

// StockLookup is satisfied by the inventory module's API.
type StockLookup = httpadapter.StockLookup

type Options struct {
	Pool     *pgxpool.Pool
	Currency string
	Stock    StockLookup // optional
	Log      *slog.Logger
}

type Module struct {
	svc *app.Service
	h   *httpadapter.Handler
}

func New(o Options) *Module {
	svc := app.NewService(postgres.New(o.Pool), o.Currency)
	return &Module{svc: svc, h: httpadapter.NewHandler(svc, o.Stock, o.Log)}
}

func (m *Module) API() API { return m.svc }

func (m *Module) Routes(r chi.Router) { m.h.Routes(r) }
