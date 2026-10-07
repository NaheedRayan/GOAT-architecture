package promotion

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/promotion/adapters/postgres"
	"github.com/NaheedRayan/goat-architecture/internal/promotion/app"
)

type Module struct{ svc *app.Service }

func New(pool *pgxpool.Pool, currency string) *Module {
	return &Module{svc: app.NewService(postgres.New(pool), currency)}
}

func (m *Module) API() API { return m.svc }
