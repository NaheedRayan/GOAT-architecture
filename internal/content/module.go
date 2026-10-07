package content

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "github.com/NaheedRayan/goat-architecture/internal/content/adapters/http"
	"github.com/NaheedRayan/goat-architecture/internal/content/adapters/postgres"
	"github.com/NaheedRayan/goat-architecture/internal/content/app"
)

type Module struct {
	svc *app.Service
	h   *httpadapter.Handler
}

func New(pool *pgxpool.Pool, log *slog.Logger) *Module {
	svc := app.NewService(postgres.New(pool))
	return &Module{svc: svc, h: httpadapter.NewHandler(svc, log)}
}

func (m *Module) API() API { return m.svc }

func (m *Module) Routes(r chi.Router) { m.h.Routes(r) }

// Middleware feeds the footer links to every page.
func (m *Module) Middleware(next http.Handler) http.Handler { return m.h.Middleware(next) }
