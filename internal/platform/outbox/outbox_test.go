package outbox_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NaheedRayan/goat-architecture/internal/platform/id"
	"github.com/NaheedRayan/goat-architecture/internal/platform/outbox"
	"github.com/NaheedRayan/goat-architecture/internal/platform/testdb"
)

// Tests share a database with other packages, so each uses its own job kind
// and the worker only claims kinds it has handlers for.
func setup(t *testing.T) (*outbox.Worker, *pgxpool.Pool, string) {
	t.Helper()
	pool := testdb.Pool(t)
	w := outbox.NewWorker(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.BatchSize = 7
	return w, pool, "test." + id.New().String()
}

func pending(t *testing.T, pool *pgxpool.Pool, kind string) int {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM platform.jobs WHERE kind = $1 AND status IN ('pending','running')`, kind).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestConcurrentWorkersClaimEachJobOnce(t *testing.T) {
	w, pool, kind := setup(t)
	ctx := context.Background()
	const total = 200
	for i := range total {
		if err := outbox.Enqueue(ctx, pool, kind, map[string]int{"n": i}); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	seen := map[string]int{}
	w.Handle(kind, func(_ context.Context, j outbox.Job) error {
		mu.Lock()
		seen[j.ID.String()]++
		mu.Unlock()
		return nil
	})

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				n, err := w.RunOnce(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				// An empty claim can just mean peers hold the remaining rows; stop only when none are left.
				if n == 0 {
					if pending(t, pool, kind) == 0 {
						return
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
		}()
	}
	wg.Wait()

	if len(seen) != total {
		t.Fatalf("processed %d distinct jobs, want %d", len(seen), total)
	}
	for id, c := range seen {
		if c != 1 {
			t.Fatalf("job %s ran %d times, want 1", id, c)
		}
	}
}

func TestFailedJobRetriesThenDies(t *testing.T) {
	w, pool, kind := setup(t)
	ctx := context.Background()
	if err := outbox.Enqueue(ctx, pool, kind, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE platform.jobs SET max_attempts = 2 WHERE kind = $1`, kind); err != nil {
		t.Fatal(err)
	}
	calls := 0
	w.Handle(kind, func(context.Context, outbox.Job) error { calls++; return errors.New("boom") })

	for range 2 {
		if _, err := w.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		// Skip the backoff delay.
		if _, err := pool.Exec(ctx, `UPDATE platform.jobs SET run_at = now() - interval '1 second' WHERE kind = $1 AND status = 'pending'`, kind); err != nil {
			t.Fatal(err)
		}
	}

	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM platform.jobs WHERE kind = $1`, kind).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "dead" || calls != 2 {
		t.Fatalf("status=%s calls=%d, want dead/2", status, calls)
	}
}

func TestWorkerIgnoresKindsWithoutHandler(t *testing.T) {
	w, pool, kind := setup(t)
	ctx := context.Background()
	if err := outbox.Enqueue(ctx, pool, kind, nil); err != nil {
		t.Fatal(err)
	}
	w.Handle("test.other."+id.New().String(), func(context.Context, outbox.Job) error { return nil })
	if n, err := w.RunOnce(ctx); err != nil || n != 0 {
		t.Fatalf("RunOnce = %d, %v; want 0 claimed", n, err)
	}
	if got := pending(t, pool, kind); got != 1 {
		t.Fatalf("pending = %d, want the job left untouched", got)
	}
}

func TestPoisonJobsAreBuriedNotReclaimedForever(t *testing.T) {
	w, pool, kind := setup(t)
	ctx := context.Background()
	if err := outbox.Enqueue(ctx, pool, kind, nil); err != nil {
		t.Fatal(err)
	}
	// A job whose worker keeps dying: lease expired, attempts used up.
	if _, err := pool.Exec(ctx, `UPDATE platform.jobs SET status='running', attempts=max_attempts, locked_until = now() - interval '1 minute' WHERE kind=$1`, kind); err != nil {
		t.Fatal(err)
	}
	ran := false
	w.Handle(kind, func(context.Context, outbox.Job) error { ran = true; return nil })
	if _, err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM platform.jobs WHERE kind=$1`, kind).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "dead" || ran {
		t.Fatalf("status=%s ran=%v, want a dead job that never ran again", status, ran)
	}
}

func TestInFlightJobsFinishWhenShutdownStarts(t *testing.T) {
	w, pool, kind := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := outbox.Enqueue(ctx, pool, kind, nil); err != nil {
		t.Fatal(err)
	}
	var handlerCtxErr error
	w.Handle(kind, func(hctx context.Context, _ outbox.Job) error {
		cancel() // the process is told to stop while the job runs
		time.Sleep(20 * time.Millisecond)
		handlerCtxErr = hctx.Err()
		return nil
	})
	if _, err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := pool.QueryRow(context.Background(), `SELECT status FROM platform.jobs WHERE kind=$1`, kind).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if handlerCtxErr != nil || status != "done" {
		t.Fatalf("handler ctx err=%v status=%s, want the job to finish normally", handlerCtxErr, status)
	}
}

func TestPurgeRemovesOldFinishedJobsOnly(t *testing.T) {
	_, pool, kind := setup(t)
	ctx := context.Background()
	for _, st := range []string{"done", "dead", "pending"} {
		if err := outbox.Enqueue(ctx, pool, kind, st); err != nil {
			t.Fatal(err)
		}
	}
	// Age all three well past both thresholds; set statuses.
	for _, st := range []string{"done", "dead", "pending"} {
		if _, err := pool.Exec(ctx, `UPDATE platform.jobs SET status=$2, updated_at = now() - interval '60 days' WHERE kind=$1 AND payload = to_jsonb($2::text)`, kind, st); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := outbox.Purge(ctx, pool, 7*24*time.Hour, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	var left []string
	rows, err := pool.Query(ctx, `SELECT status FROM platform.jobs WHERE kind=$1`, kind)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		left = append(left, s)
	}
	if len(left) != 1 || left[0] != "pending" {
		t.Fatalf("left after purge = %v, want only the pending job (old done/dead rows removed)", left)
	}
}

func TestEverySubscriberOfAKindRuns(t *testing.T) {
	w, pool, kind := setup(t)
	ctx := context.Background()
	if err := outbox.Enqueue(ctx, pool, kind, nil); err != nil {
		t.Fatal(err)
	}
	var a, b int
	w.Handle(kind, func(context.Context, outbox.Job) error { a++; return nil })
	w.Handle(kind, func(context.Context, outbox.Job) error { b++; return nil })
	if _, err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if a != 1 || b != 1 {
		t.Fatalf("subscribers ran a=%d b=%d, want both once", a, b)
	}
}

func TestOneFailingSubscriberRetriesTheJobButOthersStillRan(t *testing.T) {
	w, pool, kind := setup(t)
	ctx := context.Background()
	if err := outbox.Enqueue(ctx, pool, kind, nil); err != nil {
		t.Fatal(err)
	}
	ok := 0
	w.Handle(kind, func(context.Context, outbox.Job) error { return errors.New("boom") })
	w.Handle(kind, func(context.Context, outbox.Job) error { ok++; return nil })
	if _, err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	pool.QueryRow(ctx, `SELECT status FROM platform.jobs WHERE kind=$1`, kind).Scan(&status)
	if ok != 1 || status != "pending" {
		t.Fatalf("healthy subscriber ran %d times, job status %q; want it to have run once and the job to be queued for retry", ok, status)
	}
}
