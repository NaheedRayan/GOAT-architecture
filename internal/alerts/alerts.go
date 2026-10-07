// Package alerts runs the shop's recurring notifications: a daily low-stock digest
// for staff and a one-time "you left something in your cart" email for customers.
// It owns no business data; it reads other modules through their public APIs and
// uses a small table to remember what it already sent.
package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/cart"
	"github.com/NaheedRayan/goat-architecture/internal/catalog"
	"github.com/NaheedRayan/goat-architecture/internal/identity"
	"github.com/NaheedRayan/goat-architecture/internal/inventory"
	"github.com/NaheedRayan/goat-architecture/internal/platform/db"
	"github.com/NaheedRayan/goat-architecture/internal/platform/mail"
)

type Options struct {
	Pool      *pgxpool.Pool
	Tx        *db.TxManager
	Inventory inventory.API
	Catalog   catalog.API
	Carts     cart.API
	Identity  identity.API
	Mail      mail.Outbox

	AlertEmail        string        // staff recipient; empty disables the low-stock digest
	LowStockThreshold int           // units
	CartIdle          time.Duration // how long a cart must sit idle before a reminder; 0 disables reminders
	Log               *slog.Logger
}

type Module struct{ o Options }

func New(o Options) *Module {
	if o.LowStockThreshold <= 0 {
		o.LowStockThreshold = 5
	}
	return &Module{o: o}
}

const (
	lowStockEvery = 24 * time.Hour
	maxCartAge    = 7 * 24 * time.Hour // older carts are history, not forgotten purchases
	batch         = 50
)

// Background checks every 15 minutes until ctx is cancelled.
func (m *Module) Background(ctx context.Context) {
	t := time.NewTicker(15 * time.Minute)
	defer t.Stop()
	for {
		if err := m.RunOnce(ctx); err != nil && ctx.Err() == nil {
			m.o.Log.Error("alerts", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// RunOnce performs one pass of every alert.
func (m *Module) RunOnce(ctx context.Context) error {
	if err := m.lowStockDigest(ctx); err != nil {
		return fmt.Errorf("low stock digest: %w", err)
	}
	if err := m.cartReminders(ctx); err != nil {
		return fmt.Errorf("cart reminders: %w", err)
	}
	return nil
}

// ---- low stock ----

// claim records that key was sent now, unless it was sent within every. It reports whether the caller may send.
func (m *Module) claim(ctx context.Context, key string, every time.Duration) (bool, error) {
	tag, err := db.Q(ctx, m.o.Pool).Exec(ctx, `
		INSERT INTO alerts.sent (key, sent_at) VALUES ($1, now())
		ON CONFLICT (key) DO UPDATE SET sent_at = now() WHERE alerts.sent.sent_at < now() - $2::interval`, key, every.String())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (m *Module) lowStockDigest(ctx context.Context) error {
	if m.o.AlertEmail == "" {
		return nil
	}
	levels, err := m.o.Inventory.LowStock(ctx, m.o.LowStockThreshold, 200)
	if err != nil || len(levels) == 0 {
		return err
	}
	ids := make([]uuid.UUID, len(levels))
	for i, l := range levels {
		ids[i] = l.VariantID
	}
	infos, err := m.o.Catalog.VariantInfos(ctx, ids)
	if err != nil {
		return err
	}
	byID := make(map[uuid.UUID]catalog.VariantInfo, len(infos))
	for _, v := range infos {
		byID[v.VariantID] = v
	}
	var lines []string
	for _, l := range levels {
		v, ok := byID[l.VariantID]
		if !ok || !v.Active {
			continue
		}
		name := v.ProductName
		if v.Label != "" {
			name += " — " + v.Label
		}
		if v.SKU != "" {
			name += " (" + v.SKU + ")"
		}
		state := fmt.Sprintf("%d left", l.Available)
		if l.Available == 0 {
			state = "sold out"
		}
		lines = append(lines, name+": "+state)
	}
	if len(lines) == 0 {
		return nil
	}
	return m.o.Tx.WithTx(ctx, func(ctx context.Context) error {
		ok, err := m.claim(ctx, "low_stock", lowStockEvery)
		if err != nil || !ok {
			return err
		}
		return m.o.Mail.Send(ctx, m.o.AlertEmail, fmt.Sprintf("Low stock: %d item(s) need attention", len(lines)), mail.Content{
			Heading:    "Low stock",
			Paragraphs: []string{fmt.Sprintf("These items have %d or fewer units left. This digest is sent at most once a day.", m.o.LowStockThreshold)},
			Lines:      lines,
			Button:     &mail.Button{Label: "Open the admin", URL: m.o.Mail.Brand.Link("/admin")},
		})
	})
}

// ---- abandoned carts ----

func (m *Module) cartReminders(ctx context.Context) error {
	if m.o.CartIdle <= 0 {
		return nil
	}
	for {
		n := 0
		err := m.o.Tx.WithTx(ctx, func(ctx context.Context) error {
			carts, err := m.o.Carts.ClaimAbandoned(ctx, m.o.CartIdle, maxCartAge, batch)
			if err != nil {
				return err
			}
			n = len(carts)
			for _, c := range carts {
				if err := m.remind(ctx, c); err != nil {
					m.o.Log.Warn("cart reminder skipped", "user_id", c.UserID, "err", err)
				}
			}
			return nil
		})
		if err != nil || n < batch {
			return err
		}
	}
}

// remind emails one customer about their cart. A customer who is disabled, has opted
// out or has no email is skipped (the cart was already marked, so it is not retried).
func (m *Module) remind(ctx context.Context, c cart.Abandoned) error {
	u, err := m.o.Identity.User(ctx, c.UserID)
	if err != nil || u.Disabled || !u.CartReminders || u.Email == "" {
		return err
	}
	v, err := m.o.Carts.View(ctx, c.Owner)
	if err != nil || v.Empty() {
		return err
	}
	var lines []string
	for i, l := range v.Lines {
		if i == 5 {
			lines = append(lines, fmt.Sprintf("…and %d more", len(v.Lines)-5))
			break
		}
		name := l.Name
		if l.Label != "" {
			name += " — " + l.Label
		}
		lines = append(lines, fmt.Sprintf("%s × %d", name, l.Quantity))
	}
	first := "there"
	if f := strings.Fields(u.Name); len(f) > 0 {
		first = f[0]
	}
	return m.o.Mail.Send(ctx, u.Email, "You left something in your cart", mail.Content{
		Heading:    "Still thinking it over?",
		Paragraphs: []string{"Hi " + first + ", your cart is waiting. Items are not reserved until you check out."},
		Lines:      lines,
		Button:     &mail.Button{Label: "Back to your cart", URL: m.o.Mail.Brand.Link("/cart")},
		Footnote:   "You get one reminder per cart. You can turn these emails off on your account page.",
	})
}
