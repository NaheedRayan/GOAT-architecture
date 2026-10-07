// Package outbox is a Postgres-backed job queue and transactional outbox.
// Enqueue inside the same transaction as the business change; workers claim
// rows with FOR UPDATE SKIP LOCKED so each job runs on exactly one worker.
package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/platform/db"
	"github.com/NaheedRayan/goat-architecture/internal/platform/id"
)

type Job struct {
	ID       id.ID
	Kind     string
	Payload  json.RawMessage
	Attempts int
}

// Enqueue inserts a job. Pass a pgx.Tx as q to make it atomic with your writes.
func Enqueue(ctx context.Context, q db.DBTX, kind string, payload any) error {
	return EnqueueAt(ctx, q, kind, payload, time.Time{})
}

// EnqueueAt schedules a job to run no earlier than runAt (zero means now).
func EnqueueAt(ctx context.Context, q db.DBTX, kind string, payload any, runAt time.Time) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("outbox marshal: %w", err)
	}
	// A zero runAt defers to the database clock, avoiding app/DB clock skew
	// making a just-enqueued job look like it is scheduled in the future.
	var at *time.Time
	if !runAt.IsZero() {
		at = &runAt
	}
	_, err = q.Exec(ctx,
		`INSERT INTO platform.jobs (id, kind, payload, run_at) VALUES ($1, $2, $3, COALESCE($4, now()))`,
		id.New(), kind, b, at)
	return err
}

// claim leases up to n due jobs of the given kinds. Running jobs whose lease expired (crashed
// worker) are reclaimed. SKIP LOCKED lets concurrent workers never block or
// double-claim.
func claim(ctx context.Context, q db.DBTX, kinds []string, n int, lease time.Duration) ([]Job, error) {
	rows, err := q.Query(ctx, `
		UPDATE platform.jobs SET
			status = 'running',
			attempts = attempts + 1,
			locked_until = now() + $2::interval,
			updated_at = now()
		WHERE id IN (
			SELECT id FROM platform.jobs
			WHERE kind = ANY($3::text[])
			  AND run_at <= now()
			  AND (status = 'pending' OR (status = 'running' AND locked_until < now()))
			ORDER BY run_at, id
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, kind, payload, attempts`, n, lease.String(), kinds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.Kind, &j.Payload, &j.Attempts); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// complete marks a job done. With redact, the payload is wiped: it may hold
// something sensitive (a password-reset link) that should not outlive delivery.
func complete(ctx context.Context, q db.DBTX, jobID id.ID, redact bool) error {
	_, err := q.Exec(ctx, `
		UPDATE platform.jobs SET status='done', locked_until=NULL, updated_at=now(),
			payload = CASE WHEN $2 THEN '{}'::jsonb ELSE payload END
		WHERE id=$1`, jobID, redact)
	return err
}

// fail retries with exponential backoff, or marks the job dead after max_attempts.
func fail(ctx context.Context, q db.DBTX, j Job, cause error, redact bool) (dead bool, err error) {
	var status string
	err = q.QueryRow(ctx, `
		UPDATE platform.jobs SET
			status = CASE WHEN attempts >= max_attempts THEN 'dead' ELSE 'pending' END,
			run_at = now() + make_interval(secs => least(power(2, attempts), 3600)),
			locked_until = NULL,
			last_error = $2,
			updated_at = now(),
			payload = CASE WHEN $3 AND attempts >= max_attempts THEN '{}'::jsonb ELSE payload END
		WHERE id = $1
		RETURNING status`, j.ID, cause.Error(), redact).Scan(&status)
	return status == "dead", err
}

// reapPoison buries jobs whose worker kept dying: a lease that expired with no
// attempts left would otherwise be reclaimed and crash a worker forever.
func reapPoison(ctx context.Context, q db.DBTX, kinds []string) (int64, error) {
	tag, err := q.Exec(ctx, `
		UPDATE platform.jobs SET status = 'dead', locked_until = NULL, updated_at = now(),
			last_error = COALESCE(last_error, 'worker lease expired on every attempt')
		WHERE kind = ANY($1::text[]) AND status = 'running' AND locked_until < now() AND attempts >= max_attempts`, kinds)
	return tag.RowsAffected(), err
}

// Purge deletes finished jobs so the table does not grow without bound:
// successful ones after doneAfter, dead ones (kept longer for inspection) after deadAfter.
func Purge(ctx context.Context, q db.DBTX, doneAfter, deadAfter time.Duration) (int64, error) {
	tag, err := q.Exec(ctx, `
		DELETE FROM platform.jobs
		WHERE (status = 'done' AND updated_at < now() - $1::interval)
		   OR (status = 'dead' AND updated_at < now() - $2::interval)`, doneAfter.String(), deadAfter.String())
	return tag.RowsAffected(), err
}

// Publisher enqueues jobs/events on whatever transaction the context carries,
// so a module's event commits atomically with its own writes.
type Publisher struct{ pool *pgxpool.Pool }

func NewPublisher(pool *pgxpool.Pool) *Publisher { return &Publisher{pool: pool} }

func (p *Publisher) Publish(ctx context.Context, kind string, payload any) error {
	return Enqueue(ctx, db.Q(ctx, p.pool), kind, payload)
}
