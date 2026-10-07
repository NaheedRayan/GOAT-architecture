// Package admin is the back office. It owns no data: it composes the public
// APIs of the other modules behind a role-aware router.
//
// Roles: staff run the day-to-day shop (catalogue, stock, orders, shipping);
// admins additionally handle money and configuration (refunds, returns, users,
// delivery methods, coupons, imports and the audit trail).
package admin

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/catalog"
	"github.com/NaheedRayan/goat-architecture/internal/content"
	"github.com/NaheedRayan/goat-architecture/internal/identity"
	"github.com/NaheedRayan/goat-architecture/internal/inventory"
	"github.com/NaheedRayan/goat-architecture/internal/order"
	"github.com/NaheedRayan/goat-architecture/internal/platform/audit"
	"github.com/NaheedRayan/goat-architecture/internal/platform/auth"
	"github.com/NaheedRayan/goat-architecture/internal/platform/httpx"
	"github.com/NaheedRayan/goat-architecture/internal/platform/ui"
	"github.com/NaheedRayan/goat-architecture/internal/promotion"
	"github.com/NaheedRayan/goat-architecture/internal/review"
	"github.com/NaheedRayan/goat-architecture/internal/shipping"
)

type Options struct {
	Catalog           catalog.API
	Inventory         inventory.API
	Orders            order.API
	Shipping          shipping.API
	Promotions        promotion.API
	Identity          identity.API
	Content           content.API
	Reviews           review.API
	Audit             *audit.Logger
	LowStockThreshold int
	Currency          string
	Log               *slog.Logger
}

type Module struct{ o Options }

func New(o Options) *Module {
	if o.LowStockThreshold <= 0 {
		o.LowStockThreshold = 5
	}
	return &Module{o: o}
}

func (m *Module) Routes(r chi.Router) {
	r.Route("/admin", func(r chi.Router) {
		r.Use(auth.RequireAnyRole(auth.RoleAdmin, auth.RoleStaff))

		// ---- staff and admins ----
		r.Get("/", m.dashboard)

		r.Get("/products", m.products)
		r.Get("/products/new", m.productForm)
		r.Post("/products", m.createProduct)
		r.Get("/products/{id}/edit", m.productForm)
		r.Post("/products/{id}", m.updateProduct)
		r.Post("/products/{id}/images", m.addImage)
		r.Post("/products/{id}/images/{img}/delete", m.removeImage)
		r.Post("/products/{id}/images/{img}/move", m.moveImage)
		r.Post("/products/{id}/variants", m.addVariant)
		r.Post("/variants/{vid}", m.updateVariant)
		r.Post("/variants/{vid}/stock", m.receiveStock)
		r.Post("/variants/{vid}/stock/{lot}", m.setStock)
		r.Post("/products/{id}/delete", m.archiveProduct)
		r.Post("/products/{id}/restore", m.restoreProduct)

		r.Get("/categories", m.categories)
		r.Post("/categories", m.createCategory)
		r.Post("/categories/{id}", m.renameCategory)
		r.Post("/categories/{id}/delete", m.deleteCategory)

		r.Get("/orders", m.orders)
		r.Get("/orders/export.csv", m.exportOrders)
		r.Get("/orders/{id}", m.orderDetail)
		r.Post("/orders/{id}/ship", m.ship)
		r.Post("/orders/{id}/deliver", m.deliver)
		r.Post("/orders/{id}/cancel", m.cancelOrder)
		r.Post("/orders/{id}/note", m.orderNote)
		r.Post("/orders/{id}/return/reject", m.rejectReturn)

		// ---- admins only: money and configuration ----
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(auth.RoleAdmin))
			r.Post("/orders/{id}/refund", m.refundOrder)
			r.Post("/orders/{id}/return/approve", m.approveReturn)

			r.Get("/users", m.users)
			r.Post("/users/{id}/role", m.setUserRole)
			r.Post("/users/{id}/status", m.setUserStatus)

			r.Get("/shipping", m.shippingMethods)
			r.Post("/shipping", m.createShipping)
			r.Post("/shipping/{id}", m.updateShipping)
			r.Post("/shipping/{id}/delete", m.deleteShipping)

			r.Get("/coupons", m.coupons)
			r.Post("/coupons", m.createCoupon)
			r.Post("/coupons/{id}", m.updateCoupon)
			r.Post("/coupons/{id}/delete", m.deleteCoupon)

			r.Get("/pages", m.pages)
			r.Get("/pages/new", m.pageNew)
			r.Post("/pages", m.pageSave)
			r.Get("/pages/{id}", m.pageEdit)
			r.Post("/pages/{id}", m.pageSave)
			r.Post("/pages/{id}/delete", m.pageDelete)

			r.Get("/reviews", m.reviews)
			r.Post("/reviews/{id}/status", m.reviewStatus)
			r.Post("/reviews/{id}/delete", m.reviewDelete)

			r.Get("/audit", m.auditLog)

			r.Get("/import", m.importForm)
			r.Get("/import/sample.csv", m.importSample)
			r.Post("/import", m.importProducts)
		})
	})
}

// ---- helpers shared by the admin handlers ----

func (m *Module) fail(w http.ResponseWriter, r *http.Request, err error) {
	m.o.Log.Error("admin http", "path", r.URL.Path, "err", err)
	httpx.Render(w, r, http.StatusInternalServerError, ui.ErrorPage(500, "Something went wrong."))
}

func atoi(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		return n
	}
	return def
}

func actor(r *http.Request) auth.Claims {
	c, _ := auth.FromContext(r.Context())
	return c
}

func isAdmin(r *http.Request) bool { return actor(r).Role == auth.RoleAdmin }

func (m *Module) forbid(w http.ResponseWriter, r *http.Request) {
	httpx.Render(w, r, http.StatusForbidden, ui.ErrorPage(403, "That action needs an administrator."))
}

func uuidParam(r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	return id, err == nil
}

// audit records a back-office action by the signed-in admin or staff member.
func (m *Module) audit(r *http.Request, action, entity, entityID string, detail map[string]any) {
	if m.o.Audit == nil {
		return
	}
	e := audit.Entry{Action: action, Entity: entity, EntityID: entityID, Detail: detail, IP: httpx.ClientIP(r)}
	if c, ok := auth.FromContext(r.Context()); ok {
		uid := c.UserID
		e.ActorID, e.ActorRole = &uid, c.Role
	}
	m.o.Audit.Record(r.Context(), e)
}

// ---- dashboard ----

var orderStatuses = []string{
	order.StatusAwaitingPayment, order.StatusPaid, order.StatusFulfilling, order.StatusShipped,
	order.StatusDelivered, order.StatusCancelled, order.StatusRefunded,
}

func (m *Module) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	days := atoi(r.URL.Query().Get("days"), 30)
	if days != 7 && days != 30 && days != 90 {
		days = 30
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	from, to := today.AddDate(0, 0, -(days-1)), today.AddDate(0, 0, 1)

	counts, err := m.o.Orders.CountByStatus(ctx)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	pending, err := m.o.Orders.PendingReturns(ctx)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	rep, err := m.o.Orders.Report(ctx, from, to)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	low, err := m.lowStock(r)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	drafts, _ := m.o.Content.UnpublishedCount(ctx)
	vm := DashboardVM{DraftPages: drafts, Currency: m.o.Currency, Days: days, Report: rep, PendingReturns: pending, LowStock: low, Threshold: m.o.LowStockThreshold, IsAdmin: isAdmin(r)}
	for _, s := range orderStatuses {
		vm.Counts = append(vm.Counts, StatusCount{Status: s, N: counts[s]})
	}
	vm.Series = fillDays(rep.ByDay, from, days)
	httpx.Render(w, r, http.StatusOK, DashboardPage(vm))
}

// lowStock lists live variants at or below the alert threshold, with names.
func (m *Module) lowStock(r *http.Request) ([]LowStockRow, error) {
	levels, err := m.o.Inventory.LowStock(r.Context(), m.o.LowStockThreshold, 50)
	if err != nil || len(levels) == 0 {
		return nil, err
	}
	ids := make([]uuid.UUID, len(levels))
	for i, l := range levels {
		ids[i] = l.VariantID
	}
	infos, err := m.o.Catalog.VariantInfos(r.Context(), ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[uuid.UUID]catalog.VariantInfo, len(infos))
	for _, v := range infos {
		byID[v.VariantID] = v
	}
	var rows []LowStockRow
	for _, l := range levels {
		v, ok := byID[l.VariantID]
		if !ok || !v.Active { // deleted or hidden: not worth an alert
			continue
		}
		rows = append(rows, LowStockRow{ProductID: v.ProductID, Name: v.ProductName, Label: v.Label, SKU: v.SKU, Available: l.Available})
		if len(rows) == 20 {
			break
		}
	}
	return rows, nil
}

// fillDays turns the sparse per-day rows into a continuous series (zero for quiet days).
func fillDays(rows []order.DayStat, from time.Time, days int) []order.DayStat {
	byDay := make(map[string]order.DayStat, len(rows))
	for _, d := range rows {
		byDay[d.Day.UTC().Format("2006-01-02")] = d
	}
	out := make([]order.DayStat, days)
	for i := range out {
		day := from.AddDate(0, 0, i)
		out[i] = order.DayStat{Day: day}
		if d, ok := byDay[day.Format("2006-01-02")]; ok {
			out[i] = order.DayStat{Day: day, Orders: d.Orders, RevenueCents: d.RevenueCents}
		}
	}
	return out
}

var errNotFound = errors.New("not found")
