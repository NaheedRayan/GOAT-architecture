package payment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	httpadapter "github.com/NaheedRayan/goat-architecture/internal/payment/adapters/http"
	"github.com/NaheedRayan/goat-architecture/internal/payment/adapters/mock"
	"github.com/NaheedRayan/goat-architecture/internal/payment/adapters/nogateway"
	"github.com/NaheedRayan/goat-architecture/internal/payment/adapters/postgres"
	"github.com/NaheedRayan/goat-architecture/internal/payment/app"
	"github.com/NaheedRayan/goat-architecture/internal/platform/db"
	"github.com/NaheedRayan/goat-architecture/internal/platform/outbox"
)

type Options struct {
	Pool          *pgxpool.Pool
	Tx            *db.TxManager
	Production    bool   // refuses the mock provider
	Provider      string // "mock" is the only built-in provider
	WebhookSecret string
	Log           *slog.Logger
}

type Module struct {
	svc *app.Service
	h   *httpadapter.Handler
}

func New(o Options) (*Module, error) {
	if o.Production && o.Provider == "mock" {
		return nil, errors.New("the mock payment provider cannot be used in production")
	}
	var gw app.Gateway
	switch o.Provider {
	case "mock":
		gw = mock.New(o.WebhookSecret)
	case "none":
		gw = nogateway.Gateway{}
	default:
		return nil, fmt.Errorf("payment provider %q is not implemented; add an adapter implementing app.Gateway", o.Provider)
	}
	svc := app.NewService(postgres.New(o.Pool), gw, o.Tx, outbox.NewPublisher(o.Pool))
	return &Module{svc: svc, h: httpadapter.NewHandler(svc, o.Provider == "mock", o.Log)}, nil
}

func (m *Module) API() API { return apiImpl{m.svc} }

func (m *Module) Routes(r chi.Router) { m.h.Routes(r) }

type apiImpl struct{ s *app.Service }

func (a apiImpl) CreateIntent(ctx context.Context, orderID, userID uuid.UUID, amountCents int64, currency, method string) (Payment, error) {
	return a.s.CreateIntent(ctx, orderID, userID, amountCents, currency, method)
}

func (a apiImpl) CollectCOD(ctx context.Context, paymentID uuid.UUID) (Payment, error) {
	return a.s.CollectCOD(ctx, paymentID)
}

func (a apiImpl) Refund(ctx context.Context, paymentID uuid.UUID) (Payment, error) {
	return a.s.Refund(ctx, paymentID)
}

func (a apiImpl) Fail(ctx context.Context, paymentID uuid.UUID) (Payment, error) {
	return a.s.Settle(ctx, paymentID, false, "")
}
