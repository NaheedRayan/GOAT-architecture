package order

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/cart"
	"github.com/NaheedRayan/goat-architecture/internal/identity"
	"github.com/NaheedRayan/goat-architecture/internal/inventory"
	httpadapter "github.com/NaheedRayan/goat-architecture/internal/order/adapters/http"
	"github.com/NaheedRayan/goat-architecture/internal/order/adapters/modules"
	"github.com/NaheedRayan/goat-architecture/internal/order/adapters/postgres"
	"github.com/NaheedRayan/goat-architecture/internal/order/app"
	"github.com/NaheedRayan/goat-architecture/internal/payment"
	"github.com/NaheedRayan/goat-architecture/internal/platform/db"
	"github.com/NaheedRayan/goat-architecture/internal/platform/outbox"
)

type Options struct {
	Pool      *pgxpool.Pool
	Tx        *db.TxManager
	Carts     cart.API
	Inventory inventory.API
	Payments  payment.API
	Identity  identity.API
	Log       *slog.Logger
}

type Module struct {
	svc *app.Service
	h   *httpadapter.Handler
	log *slog.Logger
}

func New(o Options) *Module {
	svc := app.NewService(postgres.New(o.Pool),
		modules.Carts{API: o.Carts}, modules.Inventory{API: o.Inventory},
		modules.Payments{API: o.Payments}, modules.Addresses{API: o.Identity}, o.Tx, o.Log)
	return &Module{svc: svc, h: httpadapter.NewHandler(svc, cart.UserOwner, o.Log), log: o.Log}
}

func (m *Module) API() API { return apiImpl{m.svc} }

func (m *Module) Routes(r chi.Router) { m.h.Routes(r) }

// RegisterHandlers subscribes the order module to events from payment and inventory.
func (m *Module) RegisterHandlers(w *outbox.Worker) {
	w.Handle(payment.EventSucceeded, orderEvent(m.svc.OnPaymentSucceeded))
	w.Handle(payment.EventFailed, orderEvent(m.svc.OnPaymentFailed))
	w.Handle(inventory.EventReservationExpired, orderEvent(m.svc.OnReservationExpired))
}

// orderEvent decodes the {"order_id": ...} field shared by all subscribed events.
func orderEvent(h func(ctx context.Context, orderID uuid.UUID) error) outbox.Handler {
	return func(ctx context.Context, j outbox.Job) error {
		var p struct {
			OrderID uuid.UUID `json:"order_id"`
		}
		if err := json.Unmarshal(j.Payload, &p); err != nil {
			return err
		}
		return h(ctx, p.OrderID)
	}
}

// FulfillOnce hands a batch of paid orders to fulfillment. Several instances
// may run side by side; SKIP LOCKED gives each a disjoint batch.
func (m *Module) FulfillOnce(ctx context.Context) ([]uuid.UUID, error) {
	return m.svc.ClaimForFulfillment(ctx, 20)
}

// Background polls FulfillOnce until ctx is cancelled.
func (m *Module) Background(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		ids, err := m.FulfillOnce(ctx)
		if err != nil && ctx.Err() == nil {
			m.log.Error("claim paid orders", "err", err)
		}
		for _, id := range ids {
			m.log.Info("order picked up for fulfillment", "order_id", id)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type apiImpl struct{ s *app.Service }

func (a apiImpl) Get(ctx context.Context, id uuid.UUID) (Order, error) { return a.s.Get(ctx, id) }
func (a apiImpl) List(ctx context.Context, status string, limit, offset int) ([]Order, error) {
	return a.s.List(ctx, status, limit, offset)
}
func (a apiImpl) CountByStatus(ctx context.Context) (map[string]int, error) {
	return a.s.CountByStatus(ctx)
}
func (a apiImpl) Ship(ctx context.Context, id uuid.UUID) error { return a.s.Ship(ctx, id) }
