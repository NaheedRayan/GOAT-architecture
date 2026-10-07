package wishlist

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/catalog"
	"github.com/NaheedRayan/goat-architecture/internal/identity"
	"github.com/NaheedRayan/goat-architecture/internal/platform/outbox"
	httpadapter "github.com/NaheedRayan/goat-architecture/internal/wishlist/adapters/http"
	"github.com/NaheedRayan/goat-architecture/internal/wishlist/adapters/postgres"
	"github.com/NaheedRayan/goat-architecture/internal/wishlist/app"
)

type Options struct {
	Pool    *pgxpool.Pool
	Catalog catalog.API
	Log     *slog.Logger
}

type Module struct {
	svc *app.Service
	h   *httpadapter.Handler
}

func New(o Options) *Module {
	svc := app.NewService(postgres.New(o.Pool))
	return &Module{svc: svc, h: httpadapter.NewHandler(svc, exists{o.Catalog}, o.Log)}
}

func (m *Module) API() API { return m.svc }

func (m *Module) Routes(r chi.Router) { m.h.Routes(r) }

// ExportUser is the user's wishlist, for the personal-data download.
func (m *Module) ExportUser(ctx context.Context, userID uuid.UUID) (any, error) {
	ids, err := m.svc.List(ctx, userID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"product_ids": ids}, nil
}

// RegisterHandlers erases a deleted account's wishlist.
func (m *Module) RegisterHandlers(w *outbox.Worker) {
	w.Handle(identity.EventAccountDeleted, func(ctx context.Context, j outbox.Job) error {
		var p struct {
			UserID uuid.UUID `json:"user_id"`
		}
		if err := json.Unmarshal(j.Payload, &p); err != nil {
			return err
		}
		return m.svc.DeleteUser(ctx, p.UserID)
	})
}

type exists struct{ api catalog.API }

func (e exists) Exists(ctx context.Context, id uuid.UUID) bool {
	p, err := e.api.ByID(ctx, id)
	return err == nil && p.ArchivedAt == nil
}
