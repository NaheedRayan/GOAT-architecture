package review

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/catalog"
	"github.com/NaheedRayan/goat-architecture/internal/identity"
	"github.com/NaheedRayan/goat-architecture/internal/order"
	"github.com/NaheedRayan/goat-architecture/internal/platform/outbox"
	httpadapter "github.com/NaheedRayan/goat-architecture/internal/review/adapters/http"
	"github.com/NaheedRayan/goat-architecture/internal/review/adapters/postgres"
	"github.com/NaheedRayan/goat-architecture/internal/review/app"
)

type Options struct {
	Pool     *pgxpool.Pool
	Orders   order.API
	Catalog  catalog.API
	Identity identity.API
	Log      *slog.Logger
}

type Module struct {
	svc *app.Service
	h   *httpadapter.Handler
}

func New(o Options) *Module {
	svc := app.NewService(postgres.New(o.Pool), o.Orders)
	return &Module{svc: svc, h: httpadapter.NewHandler(svc, productIDs{o.Catalog}, names{o.Identity}, o.Log)}
}

func (m *Module) API() API { return m.svc }

func (m *Module) Routes(r chi.Router) { m.h.Routes(r) }

// Section renders the reviews block of a product page.
func (m *Module) Section(ctx context.Context, productID uuid.UUID, slug, flash string) templ.Component {
	return m.h.Section(ctx, productID, slug, flash)
}

// ExportUser is the user's reviews, for the personal-data download.
func (m *Module) ExportUser(ctx context.Context, userID uuid.UUID) (any, error) {
	rs, err := m.svc.ExportUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, len(rs))
	for i, r := range rs {
		out[i] = map[string]any{"product_id": r.ProductID, "rating": r.Rating, "title": r.Title, "body": r.Body, "status": r.Status, "created_at": r.CreatedAt}
	}
	return out, nil
}

// RegisterHandlers erases a deleted account's reviews.
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

type productIDs struct{ api catalog.API }

func (p productIDs) IDBySlug(ctx context.Context, slug string) (uuid.UUID, error) {
	pr, err := p.api.BySlug(ctx, slug)
	if err != nil {
		return uuid.Nil, err
	}
	return pr.ID, nil
}

type names struct{ api identity.API }

func (n names) NameOf(ctx context.Context, id uuid.UUID) (string, error) {
	u, err := n.api.User(ctx, id)
	return u.Name, err
}
