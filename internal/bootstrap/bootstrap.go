// Package bootstrap is the composition root: it builds every module, connects
// them through their public APIs and returns the HTTP handler and workers.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/admin"
	"github.com/NaheedRayan/goat-architecture/internal/cart"
	"github.com/NaheedRayan/goat-architecture/internal/catalog"
	"github.com/NaheedRayan/goat-architecture/internal/identity"
	"github.com/NaheedRayan/goat-architecture/internal/inventory"
	"github.com/NaheedRayan/goat-architecture/internal/order"
	"github.com/NaheedRayan/goat-architecture/internal/payment"
	"github.com/NaheedRayan/goat-architecture/internal/platform/auth"
	"github.com/NaheedRayan/goat-architecture/internal/platform/config"
	"github.com/NaheedRayan/goat-architecture/internal/platform/db"
	"github.com/NaheedRayan/goat-architecture/internal/platform/httpx"
	"github.com/NaheedRayan/goat-architecture/internal/platform/outbox"
	"github.com/NaheedRayan/goat-architecture/internal/platform/ui"
	"github.com/NaheedRayan/goat-architecture/web"
)

type App struct {
	Handler http.Handler
	Worker  *outbox.Worker

	// Module handles, exposed for the server's background loops and for tests.
	Identity  identity.API
	Catalog   catalog.API
	Inventory inventory.API
	Orders    *order.Module
	InvModule *inventory.Module
	idModule  *identity.Module
	cartMod   *cart.Module
	cfg       config.Config
}

func New(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	tx := db.NewTxManager(pool)

	signer, ephemeral, err := auth.NewSigner(cfg.JWTSeed, cfg.AccessTokenTTL)
	if err != nil {
		return nil, err
	}
	if ephemeral {
		if cfg.Production() {
			return nil, errors.New("JWT_SEED is required in production")
		}
		log.Warn("JWT_SEED not set: using an ephemeral signing key, sessions end on restart")
	}

	inv := inventory.New(inventory.Options{Pool: pool, Tx: tx, ReservationTTL: cfg.ReservationTTL, Log: log})
	cat := catalog.New(catalog.Options{Pool: pool, Currency: cfg.Currency, Stock: inv.API(), Log: log})
	ident := identity.New(identity.Options{
		Pool: pool, Signer: signer, AccessTTL: cfg.AccessTokenTTL, RefreshTTL: cfg.RefreshTokenTTL,
		SecureCookies: cfg.Production(), Log: log,
	})
	pay, err := payment.New(payment.Options{
		Pool: pool, Tx: tx, Production: cfg.Production(), Provider: cfg.PaymentProvider, WebhookSecret: cfg.PaymentWebhookSecret, Log: log,
	})
	if err != nil {
		return nil, err
	}
	carts := cart.New(cart.Options{Pool: pool, Catalog: cat.API(), SecureCookies: cfg.Production(), Log: log})
	orders := order.New(order.Options{
		Pool: pool, Tx: tx, Carts: carts.API(), Inventory: inv.API(), Payments: pay.API(), Identity: ident.API(), Log: log,
	})
	back := admin.New(admin.Options{Catalog: cat.API(), Inventory: inv.API(), Orders: orders.API(), Log: log})

	if cfg.AdminEmail != "" && cfg.AdminPassword != "" {
		if err := ident.API().EnsureAdmin(ctx, cfg.AdminEmail, cfg.AdminPassword); err != nil {
			return nil, fmt.Errorf("bootstrap admin: %w", err)
		}
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID, httpx.EchoRequestID)
	if cfg.TrustProxy { // only behind a proxy we control; otherwise clients could spoof their IP
		r.Use(middleware.RealIP)
	}
	var extraHosts []string
	if u, err := url.Parse(cfg.PublicURL); err == nil && u.Host != "" {
		extraHosts = append(extraHosts, u.Host)
	}
	r.Use(middleware.Recoverer, httpx.RequestLog(log), httpx.SecurityHeaders(cfg.Production()),
		middleware.GetHead, middleware.RedirectSlashes,
		middleware.Compress(5, "text/html", "text/css", "text/plain", "application/javascript", "image/svg+xml"),
		httpx.BodyLimit(1<<20), httpx.OriginCheck(extraHosts...))
	r.Handle("/static/*", web.Handler())
	r.Get("/healthz", func(w http.ResponseWriter, req *http.Request) {
		if err := pool.Ping(req.Context()); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	r.Group(func(r chi.Router) {
		r.Use(ident.Authenticate, carts.Middleware)
		r.NotFound(func(w http.ResponseWriter, req *http.Request) {
			httpx.Render(w, req, http.StatusNotFound, ui.ErrorPage(404, "That page doesn't exist."))
		})
		ident.Routes(r)
		cat.Routes(r)
		carts.Routes(r)
		orders.Routes(r)
		pay.Routes(r)
		back.Routes(r)
	})

	worker := outbox.NewWorker(pool, log)
	orders.RegisterHandlers(worker)

	return &App{
		Handler: r, Worker: worker, Identity: ident.API(), Catalog: cat.API(), Inventory: inv.API(),
		Orders: orders, InvModule: inv, idModule: ident, cartMod: carts, cfg: cfg,
	}, nil
}

// RunWorkers runs the background loops (job queue, reservation expiry,
// fulfillment pickup) until ctx is cancelled, then returns.
func (a *App) RunWorkers(ctx context.Context) {
	var wg sync.WaitGroup
	for _, fn := range []func(context.Context){a.Worker.Run, a.InvModule.Background, a.Orders.Background, a.idModule.Background, a.cartMod.Background} {
		wg.Add(1)
		go func() { defer wg.Done(); fn(ctx) }()
	}
	wg.Wait()
}
