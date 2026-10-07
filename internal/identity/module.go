package identity

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/identity/adapters/argon2"
	httpadapter "github.com/NaheedRayan/goat-architecture/internal/identity/adapters/http"
	"github.com/NaheedRayan/goat-architecture/internal/identity/adapters/postgres"
	"github.com/NaheedRayan/goat-architecture/internal/identity/app"
	"github.com/NaheedRayan/goat-architecture/internal/platform/auth"
	"github.com/NaheedRayan/goat-architecture/internal/platform/db"
	"github.com/NaheedRayan/goat-architecture/internal/platform/outbox"
)

type Options struct {
	Pool          *pgxpool.Pool
	Tx            *db.TxManager
	Signer        *auth.Signer
	AccessTTL     time.Duration
	RefreshTTL    time.Duration
	SecureCookies bool
	Mail          app.Mailer
	Link          func(path string) string
	SiteName      string
	Log           *slog.Logger
}

type Module struct {
	svc *app.Service
	h   *httpadapter.Handler
	log *slog.Logger
}

func New(o Options) *Module {
	svc := app.NewService(app.Deps{
		Repo: postgres.New(o.Pool), Hasher: argon2.Hasher{}, Tokens: o.Signer, RefreshTTL: o.RefreshTTL,
		Mail: o.Mail, Link: o.Link, Events: outbox.NewPublisher(o.Pool), Tx: o.Tx, SiteName: o.SiteName, Log: o.Log,
	})
	return &Module{svc: svc, h: httpadapter.NewHandler(svc, o.Signer, o.SecureCookies, o.AccessTTL, o.Log), log: o.Log}
}

func (m *Module) API() API { return apiImpl{m.svc} }

func (m *Module) Routes(r chi.Router) { m.h.Routes(r) }

// Authenticate is the global middleware that resolves the caller's identity.
func (m *Module) Authenticate(next http.Handler) http.Handler { return m.h.Authenticate(next) }

// GuestSignIn creates a password-less account for a checkout without registration
// and signs the browser in. It returns ErrAccountExists when the email already has an account.
func (m *Module) GuestSignIn(w http.ResponseWriter, r *http.Request, email, name string) (uuid.UUID, error) {
	return m.h.GuestSignIn(w, r, email, name)
}

// ClearSession signs the browser out (used after account deletion).
func (m *Module) ClearSession(w http.ResponseWriter) { m.h.ClearSession(w) }

type apiImpl struct{ *app.Service }

// Background purges stale tokens hourly until ctx is cancelled.
func (m *Module) Background(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n, err := m.svc.PurgeTokens(ctx); err != nil && ctx.Err() == nil {
			m.log.Error("purge refresh tokens", "err", err)
		} else if n > 0 {
			m.log.Info("purged refresh tokens", "count", n)
		}
		if n, err := m.svc.PurgeActionTokens(ctx); err != nil && ctx.Err() == nil {
			m.log.Error("purge action tokens", "err", err)
		} else if n > 0 {
			m.log.Info("purged action tokens", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
