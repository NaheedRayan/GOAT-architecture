package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/NaheedRayan/goat-architecture/internal/platform/db"
)

type Handler func(ctx context.Context, j Job) error

type Worker struct {
	q        db.DBTX
	log      *slog.Logger
	handlers map[string][]Handler
	redact   map[string]bool

	BatchSize    int
	PollInterval time.Duration
	Lease        time.Duration
	// HandlerTimeout bounds one handler run; it must stay below Lease so a slow
	// handler is not reclaimed (and run twice) while still working.
	HandlerTimeout time.Duration
}

func NewWorker(q db.DBTX, log *slog.Logger) *Worker {
	return &Worker{
		q: q, log: log, handlers: map[string][]Handler{}, redact: map[string]bool{},
		BatchSize: 10, PollInterval: time.Second, Lease: 5 * time.Minute, HandlerTimeout: 4 * time.Minute,
	}
}

// RedactPayload wipes the payload of jobs of these kinds once they finish (or
// die), for jobs whose payload is sensitive.
func (w *Worker) RedactPayload(kinds ...string) {
	for _, k := range kinds {
		w.redact[k] = true
	}
}

// Handle subscribes a handler to a job kind and makes the worker claim that kind.
// Several modules may subscribe to the same kind (an event fans out to all of
// them). If any handler fails the whole job is retried, so every handler must be
// idempotent. Call before Run.
func (w *Worker) Handle(kind string, h Handler) { w.handlers[kind] = append(w.handlers[kind], h) }

// Run polls until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(w.PollInterval)
	defer t.Stop()
	purgeAt := time.Now()
	for {
		if time.Now().After(purgeAt) {
			purgeAt = time.Now().Add(time.Hour)
			if n, err := Purge(ctx, w.q, 7*24*time.Hour, 30*24*time.Hour); err != nil && ctx.Err() == nil {
				w.log.Error("outbox purge", "err", err)
			} else if n > 0 {
				w.log.Info("outbox purged", "jobs", n)
			}
		}
		for {
			n, err := w.RunOnce(ctx)
			if err != nil && ctx.Err() == nil {
				w.log.Error("outbox poll", "err", err)
			}
			if err != nil || n < w.BatchSize {
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

// RunOnce claims and processes one batch, returning the number of jobs claimed.
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	if len(w.handlers) == 0 {
		return 0, nil
	}
	kinds := make([]string, 0, len(w.handlers))
	for k := range w.handlers {
		kinds = append(kinds, k)
	}
	// Only jobs this worker can handle are claimed, so workers with different
	// handler sets (or tests sharing a database) never fail each other's jobs.
	if n, err := reapPoison(ctx, w.q, kinds); err != nil {
		return 0, err
	} else if n > 0 {
		w.log.Error("jobs buried: their worker kept dying", "count", n)
	}
	jobs, err := claim(ctx, w.q, kinds, w.BatchSize, w.Lease)
	if err != nil {
		return 0, err
	}
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.process(ctx, j)
		}()
	}
	wg.Wait()
	return len(jobs), nil
}

func (w *Worker) process(ctx context.Context, j Job) {
	hs := w.handlers[j.Kind]
	var err error
	if len(hs) == 0 {
		err = fmt.Errorf("no handler for kind %q", j.Kind)
	} else {
		// Detached from ctx: when the process is told to stop, jobs already claimed
		// finish instead of failing half-way and burning a retry.
		hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.HandlerTimeout)
		var errs []error
		for _, h := range hs {
			if herr := safeCall(hctx, h, j); herr != nil {
				errs = append(errs, herr)
			}
		}
		cancel()
		err = errors.Join(errs...)
	}
	// Use a fresh context so shutdown does not strand the status update.
	uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err != nil {
		w.log.Warn("job failed", "kind", j.Kind, "id", j.ID, "attempt", j.Attempts, "err", err)
		dead, ferr := fail(uctx, w.q, j, err, w.redact[j.Kind])
		if ferr != nil {
			w.log.Error("job fail update", "id", j.ID, "err", ferr)
		}
		if dead {
			w.log.Error("job dead: retries exhausted, needs attention", "kind", j.Kind, "id", j.ID, "attempts", j.Attempts, "err", err)
		}
		return
	}
	if cerr := complete(uctx, w.q, j.ID, w.redact[j.Kind]); cerr != nil {
		w.log.Error("job complete update", "id", j.ID, "err", cerr)
	}
}

func safeCall(ctx context.Context, h Handler, j Job) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return h(ctx, j)
}
