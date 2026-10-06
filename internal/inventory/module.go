package inventory

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/inventory/adapters/postgres"
	"github.com/NaheedRayan/goat-architecture/internal/inventory/app"
	"github.com/NaheedRayan/goat-architecture/internal/platform/db"
	"github.com/NaheedRayan/goat-architecture/internal/platform/outbox"
)

type Options struct {
	Pool           *pgxpool.Pool
	Tx             *db.TxManager
	ReservationTTL time.Duration
	Log            *slog.Logger
}

type Module struct {
	svc *app.Service
	log *slog.Logger
}

func New(o Options) *Module {
	return &Module{
		svc: app.NewService(postgres.New(o.Pool), o.Tx, outbox.NewPublisher(o.Pool), o.ReservationTTL),
		log: o.Log,
	}
}

func (m *Module) API() API { return m.svc }

// Background periodically releases expired reservations until ctx is cancelled.
func (m *Module) Background(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	purgeAt := time.Now()
	for {
		if time.Now().After(purgeAt) {
			purgeAt = time.Now().Add(time.Hour)
			if n, err := m.svc.PurgeHistory(ctx); err != nil && ctx.Err() == nil {
				m.log.Error("purge reservations", "err", err)
			} else if n > 0 {
				m.log.Info("purged reservations", "count", n)
			}
		}
		for {
			n, err := m.svc.ReleaseExpired(ctx, 100)
			if err != nil && ctx.Err() == nil {
				m.log.Error("release expired reservations", "err", err)
			}
			if err != nil || n < 100 {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
