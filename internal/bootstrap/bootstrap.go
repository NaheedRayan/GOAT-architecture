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
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/admin"
	"github.com/NaheedRayan/goat-architecture/internal/alerts"
	"github.com/NaheedRayan/goat-architecture/internal/cart"
	"github.com/NaheedRayan/goat-architecture/internal/catalog"
	"github.com/NaheedRayan/goat-architecture/internal/content"
	"github.com/NaheedRayan/goat-architecture/internal/identity"
	"github.com/NaheedRayan/goat-architecture/internal/inventory"
	"github.com/NaheedRayan/goat-architecture/internal/order"
	"github.com/NaheedRayan/goat-architecture/internal/payment"
	"github.com/NaheedRayan/goat-architecture/internal/platform/audit"
	"github.com/NaheedRayan/goat-architecture/internal/platform/auth"
	"github.com/NaheedRayan/goat-architecture/internal/platform/config"
	"github.com/NaheedRayan/goat-architecture/internal/platform/db"
	"github.com/NaheedRayan/goat-architecture/internal/platform/httpx"
	"github.com/NaheedRayan/goat-architecture/internal/platform/mail"
	"github.com/NaheedRayan/goat-architecture/internal/platform/media"
	"github.com/NaheedRayan/goat-architecture/internal/platform/outbox"
	"github.com/NaheedRayan/goat-architecture/internal/platform/ui"
	"github.com/NaheedRayan/goat-architecture/internal/privacy"
	"github.com/NaheedRayan/goat-architecture/internal/promotion"
	"github.com/NaheedRayan/goat-architecture/internal/review"
	"github.com/NaheedRayan/goat-architecture/internal/seo"
	"github.com/NaheedRayan/goat-architecture/internal/shipping"
	"github.com/NaheedRayan/goat-architecture/internal/wishlist"
	"github.com/NaheedRayan/goat-architecture/web"
)

type App struct {
	Handler http.Handler
	Worker  *outbox.Worker

	// Module handles, exposed for the server's background loops and for tests.
	Identity   identity.API
	Catalog    catalog.API
	Inventory  inventory.API
	Orders     *order.Module
	Shipping   shipping.API
	Promotions promotion.API
	Content    content.API
	InvModule  *inventory.Module
	Alerts     *alerts.Module
	idModule   *identity.Module
	cartMod    *cart.Module
	cfg        config.Config
}

// Option customises the app; used by tests to inject fakes.
type Option func(*options)

type options struct{ mailer mail.Mailer }

// WithMailer replaces the configured mailer (tests capture emails with mail.MemoryMailer).
func WithMailer(m mail.Mailer) Option { return func(o *options) { o.mailer = m } }

func New(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, log *slog.Logger, opts ...Option) (*App, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	tx := db.NewTxManager(pool)

	mailer := o.mailer
	if mailer == nil {
		var err error
		if mailer, err = newMailer(cfg, log); err != nil {
			return nil, err
		}
	}
	brand := mail.Brand{Name: cfg.SiteName, BaseURL: baseURL(cfg)}
	mailbox := mail.Outbox{Brand: brand, Pub: outbox.NewPublisher(pool)}

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

	mediaStore, err := media.NewStore(cfg.UploadDir)
	if err != nil {
		return nil, err
	}
	inv := inventory.New(inventory.Options{Pool: pool, Tx: tx, ReservationTTL: cfg.ReservationTTL, Log: log})
	extras := &storefrontExtras{}
	cat := catalog.New(catalog.Options{Pool: pool, Currency: cfg.Currency, Stock: inv.API(), Media: mediaStore, Extras: extras, Log: log})
	ident := identity.New(identity.Options{
		Pool: pool, Tx: tx, Signer: signer, AccessTTL: cfg.AccessTokenTTL, RefreshTTL: cfg.RefreshTokenTTL,
		SecureCookies: cfg.Production(), Mail: mailbox, Link: brand.Link, SiteName: cfg.SiteName, Log: log,
	})
	pay, err := payment.New(payment.Options{
		Pool: pool, Tx: tx, Production: cfg.Production(), Provider: cfg.PaymentProvider, WebhookSecret: cfg.PaymentWebhookSecret, Log: log,
	})
	if err != nil {
		return nil, err
	}
	carts := cart.New(cart.Options{Pool: pool, Catalog: cat.API(), SecureCookies: cfg.Production(), Log: log})
	pages := content.New(pool, log)
	seoMod := seo.New(seo.Options{Catalog: cat.API(), Content: pages.API(), BaseURL: cfg.PublicURL, Log: log})
	ship := shipping.New(pool)
	promos := promotion.New(pool, cfg.Currency)
	orders := order.New(order.Options{
		Pool: pool, Tx: tx, Carts: carts.API(), Inventory: inv.API(), Payments: pay.API(), Identity: ident.API(),
		Shipping: ship.API(), Promotions: promos.API(), Mail: mailbox,
		Settings: order.Settings{
			Currency: cfg.Currency, TaxRateBps: cfg.TaxRateBps(), TaxInclusive: cfg.TaxInclusive, TaxLabel: cfg.TaxLabel,
			PaymentMethods: cfg.PaymentMethods, ReturnWindowDays: cfg.ReturnWindowDays, AlertEmail: cfg.AlertEmail,
			SiteName: cfg.SiteName, Link: brand.Link,
		},
		CartOwnerFor: carts.OwnerFor, GuestSignIn: ident.GuestSignIn, Log: log,
	})

	reviewMod := review.New(review.Options{Pool: pool, Orders: orders.API(), Catalog: cat.API(), Identity: ident.API(), Log: log})
	wishMod := wishlist.New(wishlist.Options{Pool: pool, Catalog: cat.API(), Log: log})
	extras.reviews, extras.wishlist = reviewMod, wishMod.API()

	alertsMod := alerts.New(alerts.Options{
		Pool: pool, Tx: tx, Inventory: inv.API(), Catalog: cat.API(), Carts: carts.API(), Identity: ident.API(), Mail: mailbox,
		AlertEmail: cfg.AlertEmail, LowStockThreshold: cfg.LowStockThreshold, CartIdle: time.Duration(cfg.AbandonedCartHours) * time.Hour, Log: log,
	})
	privacyMod := privacy.New(privacy.Options{
		Identity: ident.API(), ClearSession: ident.ClearSession, Log: log,
		Exporters: map[string]privacy.Exporter{"orders": orders.ExportUser, "reviews": reviewMod.ExportUser, "wishlist": wishMod.ExportUser},
	})
	back := admin.New(admin.Options{
		Catalog: cat.API(), Inventory: inv.API(), Orders: orders.API(), Shipping: ship.API(), Promotions: promos.API(),
		Identity: ident.API(), Content: pages.API(), Reviews: reviewMod.API(), Audit: audit.New(pool, log), LowStockThreshold: cfg.LowStockThreshold, Currency: cfg.Currency, Log: log,
	})

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
	metrics := httpx.NewMetrics()
	metrics.Gauges = func() map[string]float64 { return queueGauges(pool) }
	r.Use(metrics.Middleware)
	r.Use(middleware.Recoverer, httpx.RequestLog(log), httpx.SecurityHeaders(cfg.Production()),
		middleware.GetHead, middleware.RedirectSlashes,
		middleware.Compress(5, "text/html", "text/css", "text/plain", "application/javascript", "image/svg+xml"),
		httpx.BodyLimitFunc(bodyLimit), httpx.OriginCheck(extraHosts...), seo.NoIndex, siteContext(cfg.SiteName, brand.BaseURL))
	r.Handle("/static/*", web.Handler())
	r.Handle("/media/*", mediaStore.Handler())
	r.Handle("/metrics", metrics.Handler(cfg.MetricsToken))
	// readyz is for load balancers: it fails while the database is unreachable or
	// the job queue is badly backed up (workers stalled).
	r.Get("/readyz", func(w http.ResponseWriter, req *http.Request) {
		if err := pool.Ping(req.Context()); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		if age := queueGauges(pool)["jobs_oldest_pending_seconds"]; age > 600 {
			http.Error(w, "job queue stalled", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	r.Get("/healthz", func(w http.ResponseWriter, req *http.Request) {
		if err := pool.Ping(req.Context()); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	r.Group(func(r chi.Router) {
		r.Use(ident.Authenticate, carts.Middleware, pages.Middleware)
		r.NotFound(func(w http.ResponseWriter, req *http.Request) {
			httpx.Render(w, req, http.StatusNotFound, ui.ErrorPage(404, "That page doesn't exist."))
		})
		ident.Routes(r)
		cat.Routes(r)
		pages.Routes(r)
		seoMod.Routes(r)
		carts.Routes(r)
		reviewMod.Routes(r)
		wishMod.Routes(r)
		orders.Routes(r)
		pay.Routes(r)
		privacyMod.Routes(r)
		back.Routes(r)
	})

	worker := outbox.NewWorker(pool, log)
	worker.Handle(mail.JobKind, mail.Handler(mailer))
	worker.RedactPayload(mail.JobKind) // reset links must not linger in the queue
	orders.RegisterHandlers(worker)
	carts.RegisterHandlers(worker)
	reviewMod.RegisterHandlers(worker)
	wishMod.RegisterHandlers(worker)

	return &App{
		Handler: r, Worker: worker, Identity: ident.API(), Catalog: cat.API(), Inventory: inv.API(),
		Orders: orders, Shipping: ship.API(), Promotions: promos.API(), Content: pages.API(), InvModule: inv, Alerts: alertsMod, idModule: ident, cartMod: carts, cfg: cfg,
	}, nil
}

// RunWorkers runs the background loops (job queue, reservation expiry,
// fulfillment pickup) until ctx is cancelled, then returns.
func (a *App) RunWorkers(ctx context.Context) {
	var wg sync.WaitGroup
	for _, fn := range []func(context.Context){a.Worker.Run, a.InvModule.Background, a.Orders.Background, a.idModule.Background, a.cartMod.Background, a.Alerts.Background} {
		wg.Add(1)
		go func() { defer wg.Done(); fn(ctx) }()
	}
	wg.Wait()
}

// uploadPath matches the admin file-upload routes (product images, CSV import), the only ones allowed a large body.
var uploadPath = regexp.MustCompile(`^/admin/(products/[0-9a-f-]{36}/images|import)$`)

func bodyLimit(r *http.Request) int64 {
	if r.Method == http.MethodPost && uploadPath.MatchString(r.URL.Path) {
		return media.MaxUploadBytes + 1<<20 // the file plus form overhead
	}
	return 1 << 20
}

func newMailer(cfg config.Config, log *slog.Logger) (mail.Mailer, error) {
	if cfg.SMTPAddr == "" {
		return mail.LogMailer{Log: log}, nil
	}
	return mail.NewSMTP(mail.SMTPConfig{Addr: cfg.SMTPAddr, User: cfg.SMTPUser, Pass: cfg.SMTPPass, From: cfg.MailFrom, TLS: cfg.SMTPTLS})
}

// baseURL is the site's public address for links in emails; a local default for development.
func baseURL(cfg config.Config) string {
	if cfg.PublicURL != "" {
		return strings.TrimRight(cfg.PublicURL, "/")
	}
	addr := cfg.HTTPAddr
	if strings.HasPrefix(addr, ":") {
		addr = "localhost" + addr
	}
	return "http://" + addr
}

// siteContext tells every page the shop's name and address (for titles, canonical links, previews).
func siteContext(name, baseURL string) func(http.Handler) http.Handler {
	site := ui.Site{Name: name, BaseURL: baseURL}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(ui.WithSite(r.Context(), site)))
		})
	}
}

// queueGauges reports job-queue health for /metrics; errors are reported as -1.
func queueGauges(pool *pgxpool.Pool) map[string]float64 {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var pending, dead int64
	var oldest float64
	err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status IN ('pending','running')),
		count(*) FILTER (WHERE status = 'dead'),
		COALESCE(EXTRACT(EPOCH FROM now() - min(run_at) FILTER (WHERE status = 'pending')), 0)::float8
		FROM platform.jobs`).Scan(&pending, &dead, &oldest)
	if err != nil {
		return map[string]float64{"jobs_pending": -1}
	}
	return map[string]float64{"jobs_pending": float64(pending), "jobs_dead": float64(dead), "jobs_oldest_pending_seconds": max(oldest, 0)}
}
