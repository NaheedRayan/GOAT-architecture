package shipping

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/shipping/adapters/postgres"
	"github.com/NaheedRayan/goat-architecture/internal/shipping/app"
)

type Module struct{ svc *app.Service }

func New(pool *pgxpool.Pool) *Module { return &Module{svc: app.NewService(postgres.New(pool))} }

func (m *Module) API() API { return m.svc }
