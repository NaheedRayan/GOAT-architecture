package order

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
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
	"github.com/NaheedRayan/goat-architecture/internal/promotion"
	"github.com/NaheedRayan/goat-architecture/internal/shipping"
)

// Settings are the store-wide rules the order module applies (tax, payment methods, returns).
type Settings = app.Settings

type Options struct {
	Pool       *pgxpool.Pool
	Tx         *db.TxManager
	Carts      cart.API
	Inventory  inventory.API
	Payments   payment.API
	Identity   identity.API
	Shipping   shipping.API
	Promotions promotion.API
	Mail       app.Mailer
	Settings   Settings
	// CartOwnerFor finds the cart of the current visitor (signed in or anonymous).
	CartOwnerFor func(r *http.Request) (owner string, ok bool)
	// GuestSignIn creates a password-less account for a checkout without registration.
	GuestSignIn func(w http.ResponseWriter, r *http.Request, email, name string) (uuid.UUID, error)
	Log         *slog.Logger
}

type Module struct {
	svc      *app.Service
	h        *httpadapter.Handler
	settings Settings
	log      *slog.Logger
}

func New(o Options) *Module {
	svc := app.NewService(app.Deps{
		Repo: postgres.New(o.Pool), Carts: modules.Carts{API: o.Carts}, Inventory: modules.Inventory{API: o.Inventory},
		Payments: modules.Payments{API: o.Payments}, Shipping: modules.Shipping{API: o.Shipping}, Promotions: modules.Promotions{API: o.Promotions},
		Customers: modules.Customers{API: o.Identity}, Addresses: modules.Addresses{API: o.Identity},
		Mail: o.Mail, Tx: o.Tx, Settings: o.Settings, Log: o.Log,
	})
	guestIssue := func(err error) (httpadapter.GuestIssue, bool) {
		var ve identity.ValidationError
		switch {
		case err == nil:
		case errors.Is(err, identity.ErrAccountExists):
			return httpadapter.GuestIssue{Msg: "An account with this email already exists. Please sign in to continue.", Status: http.StatusConflict, SignIn: true}, true
		case errors.As(err, &ve):
			return httpadapter.GuestIssue{Msg: ve.Error(), Status: http.StatusUnprocessableEntity}, true
		case errors.Is(err, identity.ErrTooManyAttempts):
			return httpadapter.GuestIssue{Msg: "Too many attempts. Please wait a moment and try again.", Status: http.StatusTooManyRequests}, true
		}
		return httpadapter.GuestIssue{}, false
	}
	h := httpadapter.NewHandler(svc, o.CartOwnerFor, o.GuestSignIn, guestIssue, o.Log)
	return &Module{svc: svc, h: h, settings: o.Settings, log: o.Log}
}

func (m *Module) API() API { return apiImpl{m} }

func (m *Module) Routes(r chi.Router) { m.h.Routes(r) }

// RegisterHandlers subscribes the order module to events from payment, inventory and identity.
func (m *Module) RegisterHandlers(w *outbox.Worker) {
	w.Handle(payment.EventSucceeded, orderEvent(m.svc.OnPaymentSucceeded))
	w.Handle(payment.EventFailed, orderEvent(m.svc.OnPaymentFailed))
	w.Handle(inventory.EventReservationExpired, orderEvent(m.svc.OnReservationExpired))
	w.Handle(identity.EventAccountDeleted, func(ctx context.Context, j outbox.Job) error {
		var p struct {
			UserID uuid.UUID `json:"user_id"`
		}
		if err := json.Unmarshal(j.Payload, &p); err != nil {
			return err
		}
		return m.svc.AnonymizeUser(ctx, p.UserID)
	})
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

// ExportUser is the module's contribution to a user's data download.
func (m *Module) ExportUser(ctx context.Context, userID uuid.UUID) (any, error) {
	os, err := m.svc.ExportUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	type line struct {
		Name      string `json:"name"`
		SKU       string `json:"sku,omitempty"`
		Quantity  int    `json:"quantity"`
		UnitPrice int64  `json:"unit_price_cents"`
	}
	type exported struct {
		ID             uuid.UUID `json:"id"`
		PlacedAt       time.Time `json:"placed_at"`
		Status         string    `json:"status"`
		Currency       string    `json:"currency"`
		SubtotalCents  int64     `json:"subtotal_cents"`
		DiscountCents  int64     `json:"discount_cents"`
		ShippingCents  int64     `json:"shipping_cents"`
		TaxCents       int64     `json:"tax_cents"`
		TotalCents     int64     `json:"total_cents"`
		ShippingMethod string    `json:"shipping_method"`
		PaymentMethod  string    `json:"payment_method"`
		Contact        string    `json:"contact_email"`
		Shipping       any       `json:"shipping_address"`
		Tracking       any       `json:"tracking,omitempty"`
		Items          []line    `json:"items"`
	}
	out := make([]exported, 0, len(os))
	for _, o := range os {
		e := exported{ID: o.ID, PlacedAt: o.CreatedAt, Status: o.Status, Currency: o.Currency, SubtotalCents: o.SubtotalCents,
			DiscountCents: o.DiscountCents, ShippingCents: o.ShippingCents, TaxCents: o.TaxCents, TotalCents: o.TotalCents,
			ShippingMethod: o.ShippingMethod, PaymentMethod: o.PaymentMethod, Contact: o.ContactEmail, Shipping: o.Shipping}
		if !o.Tracking.Empty() {
			e.Tracking = o.Tracking
		}
		for _, it := range o.Items {
			e.Items = append(e.Items, line{Name: it.DisplayName(), SKU: it.SKU, Quantity: it.Quantity, UnitPrice: it.UnitPriceCents})
		}
		out = append(out, e)
	}
	return out, nil
}

type apiImpl struct{ m *Module }

func (a apiImpl) Get(ctx context.Context, id uuid.UUID) (Order, error) { return a.m.svc.Get(ctx, id) }
func (a apiImpl) List(ctx context.Context, f ListFilter, limit, offset int) ([]Order, error) {
	return a.m.svc.List(ctx, f, limit, offset)
}
func (a apiImpl) CountByStatus(ctx context.Context) (map[string]int, error) {
	return a.m.svc.CountByStatus(ctx)
}
func (a apiImpl) Report(ctx context.Context, from, to time.Time) (Report, error) {
	return a.m.svc.Report(ctx, from, to)
}
func (a apiImpl) HasPurchased(ctx context.Context, userID, productID uuid.UUID) (bool, error) {
	return a.m.svc.HasPurchased(ctx, userID, productID)
}
func (a apiImpl) PendingReturns(ctx context.Context) (int, error) { return a.m.svc.PendingReturns(ctx) }
func (a apiImpl) Ship(ctx context.Context, actor, id uuid.UUID, t Tracking) error {
	return a.m.svc.Ship(ctx, actor, id, t)
}
func (a apiImpl) Deliver(ctx context.Context, actor, id uuid.UUID) error {
	return a.m.svc.Deliver(ctx, actor, id)
}
func (a apiImpl) CancelByStaff(ctx context.Context, actor, id uuid.UUID, reason string) error {
	return a.m.svc.CancelByStaff(ctx, actor, id, reason)
}
func (a apiImpl) Refund(ctx context.Context, actor, id uuid.UUID, restock bool, reason string) error {
	return a.m.svc.Refund(ctx, actor, id, restock, reason)
}
func (a apiImpl) SetNote(ctx context.Context, actor, id uuid.UUID, note string) error {
	return a.m.svc.SetNote(ctx, actor, id, note)
}
func (a apiImpl) ResolveReturn(ctx context.Context, actor, id uuid.UUID, approve bool, note string, restock bool) error {
	return a.m.svc.ResolveReturn(ctx, actor, id, approve, note, restock)
}
func (a apiImpl) ReturnDeadline(o Order) time.Time {
	return o.ReturnDeadline(a.m.settings.ReturnWindowDays)
}
