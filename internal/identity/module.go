package identity

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/identity/adapters/argon2"
	httpadapter "github.com/NaheedRayan/goat-architecture/internal/identity/adapters/http"
	"github.com/NaheedRayan/goat-architecture/internal/identity/adapters/postgres"
	"github.com/NaheedRayan/goat-architecture/internal/identity/app"
	"github.com/NaheedRayan/goat-architecture/internal/platform/auth"
)

type Options struct {
	Pool          *pgxpool.Pool
	Signer        *auth.Signer
	AccessTTL     time.Duration
	RefreshTTL    time.Duration
	SecureCookies bool
	Log           *slog.Logger
}

type Module struct {
	svc *app.Service
	h   *httpadapter.Handler
	log *slog.Logger
}

func New(o Options) *Module {
	svc := app.NewService(postgres.New(o.Pool), argon2.Hasher{}, o.Signer, o.RefreshTTL)
	return &Module{svc: svc, h: httpadapter.NewHandler(svc, o.Signer, o.SecureCookies, o.AccessTTL, o.Log), log: o.Log}
}

func (m *Module) API() API { return m.svc }

func (m *Module) Routes(r chi.Router) { m.h.Routes(r) }

// Authenticate is the global middleware that resolves the caller's identity.
func (m *Module) Authenticate(next http.Handler) http.Handler { return m.h.Authenticate(next) }

// Background purges stale refresh tokens hourly until ctx is cancelled.
func (m *Module) Background(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n, err := m.svc.PurgeTokens(ctx); err != nil && ctx.Err() == nil {
			m.log.Error("purge refresh tokens", "err", err)
		} else if n > 0 {
			m.log.Info("purged refresh tokens", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
