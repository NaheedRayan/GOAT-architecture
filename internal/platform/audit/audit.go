// Package audit keeps an append-only log of back-office actions.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/platform/db"
	"github.com/NaheedRayan/goat-architecture/internal/platform/id"
)

type Entry struct {
	ID        uuid.UUID
	At        time.Time
	ActorID   *uuid.UUID
	ActorRole string
	Action    string // e.g. "product.update"
	Entity    string // e.g. "product"
	EntityID  string
	Detail    map[string]any
	IP        string
}

type Logger struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

func New(pool *pgxpool.Pool, log *slog.Logger) *Logger { return &Logger{pool: pool, log: log} }

// Record writes an entry. A failure is logged but never fails the action being
// audited: losing a log line must not block a refund or a price fix.
func (l *Logger) Record(ctx context.Context, e Entry) {
	detail, err := json.Marshal(e.Detail)
	if err != nil || e.Detail == nil {
		detail = []byte("{}")
	}
	_, err = db.Q(ctx, l.pool).Exec(ctx, `
		INSERT INTO platform.audit_log (id, actor_id, actor_role, action, entity, entity_id, detail, ip)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id.New(), e.ActorID, e.ActorRole, e.Action, e.Entity, e.EntityID, detail, e.IP)
	if err != nil {
		l.log.Error("audit write failed", "action", e.Action, "entity", e.Entity, "entity_id", e.EntityID, "err", err)
	}
}

type Filter struct {
	Action   string // exact action or prefix like "product."
	Entity   string
	EntityID string
	ActorID  *uuid.UUID
}

// List returns entries newest first.
func (l *Logger) List(ctx context.Context, f Filter, limit, offset int) ([]Entry, error) {
	rows, err := db.Q(ctx, l.pool).Query(ctx, `
		SELECT id, at, actor_id, actor_role, action, entity, entity_id, detail, ip
		FROM platform.audit_log
		WHERE ($1 = '' OR action = $1 OR action LIKE $1 || '%')
		  AND ($2 = '' OR entity = $2)
		  AND ($3 = '' OR entity_id = $3)
		  AND ($4::uuid IS NULL OR actor_id = $4)
		ORDER BY at DESC, id DESC
		LIMIT $5 OFFSET $6`, f.Action, f.Entity, f.EntityID, f.ActorID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var detail []byte
		if err := rows.Scan(&e.ID, &e.At, &e.ActorID, &e.ActorRole, &e.Action, &e.Entity, &e.EntityID, &detail, &e.IP); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(detail, &e.Detail)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Purge deletes entries older than the retention period.
func (l *Logger) Purge(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := db.Q(ctx, l.pool).Exec(ctx, `DELETE FROM platform.audit_log WHERE at < now() - $1::interval`, olderThan.String())
	return tag.RowsAffected(), err
}
