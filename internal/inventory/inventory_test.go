package inventory_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/NaheedRayan/goat-architecture/internal/inventory"
	"github.com/NaheedRayan/goat-architecture/internal/platform/db"
	"github.com/NaheedRayan/goat-architecture/internal/platform/id"
	"github.com/NaheedRayan/goat-architecture/internal/platform/testdb"
)

func setup(t *testing.T) (inventory.API, *db.TxManager, func(string, ...any)) {
	t.Helper()
	pool := testdb.Pool(t)
	tx := db.NewTxManager(pool)
	m := inventory.New(inventory.Options{Pool: pool, Tx: tx, ReservationTTL: time.Minute, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	exec := func(sql string, args ...any) {
		if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	return m.API(), tx, exec
}

func stock(t *testing.T, api inventory.API, pid uuid.UUID) int {
	t.Helper()
	m, err := api.Available(context.Background(), []uuid.UUID{pid})
	if err != nil {
		t.Fatal(err)
	}
	return m[pid]
}

// Many concurrent checkouts race for 5 units: exactly 5 win, none oversell.
func TestReserveNeverOversells(t *testing.T) {
	api, _, _ := setup(t)
	ctx := context.Background()
	pid := id.New()
	if err := api.AddLot(ctx, pid, "t", 5); err != nil {
		t.Fatal(err)
	}

	var ok, short atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := api.Reserve(ctx, id.New(), []inventory.Item{{ProductID: pid, Quantity: 1}})
			var oos inventory.InsufficientStockError
			switch {
			case err == nil:
				ok.Add(1)
			case errors.As(err, &oos):
				short.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	if ok.Load() != 5 || short.Load() != 15 {
		t.Fatalf("reserved=%d insufficient=%d, want 5/15", ok.Load(), short.Load())
	}
	if left := stock(t, api, pid); left != 0 {
		t.Fatalf("stock left = %d, want 0", left)
	}
}

// Plenty of stock in a single lot: contention must not cause false "sold out".
func TestReserveUnderContentionDoesNotFalselyFail(t *testing.T) {
	api, _, _ := setup(t)
	ctx := context.Background()
	pid := id.New()
	if err := api.AddLot(ctx, pid, "t", 100); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := api.Reserve(ctx, id.New(), []inventory.Item{{ProductID: pid, Quantity: 1}}); err != nil {
				t.Errorf("reserve failed with ample stock: %v", err)
			}
		}()
	}
	wg.Wait()
	if left := stock(t, api, pid); left != 80 {
		t.Fatalf("stock left = %d, want 80", left)
	}
}

func TestReserveSpansLotsAndRollsBackOnShortage(t *testing.T) {
	api, _, _ := setup(t)
	ctx := context.Background()
	a, b := id.New(), id.New()
	for _, q := range []int{2, 3} {
		if err := api.AddLot(ctx, a, "lot", q); err != nil {
			t.Fatal(err)
		}
	}
	if err := api.AddLot(ctx, b, "lot", 1); err != nil {
		t.Fatal(err)
	}
	// 4 of A spans both lots.
	if err := api.Reserve(ctx, id.New(), []inventory.Item{{ProductID: a, Quantity: 4}}); err != nil {
		t.Fatal(err)
	}
	if got := stock(t, api, a); got != 1 {
		t.Fatalf("A stock = %d, want 1", got)
	}
	// A ok but B short: the whole reservation must roll back, including A's part.
	err := api.Reserve(ctx, id.New(), []inventory.Item{{ProductID: a, Quantity: 1}, {ProductID: b, Quantity: 2}})
	var oos inventory.InsufficientStockError
	if !errors.As(err, &oos) || oos.ProductID != b {
		t.Fatalf("err = %v, want insufficient stock for B", err)
	}
	if got := stock(t, api, a); got != 1 {
		t.Fatalf("A stock after rollback = %d, want 1", got)
	}
}

func TestReleaseCommitAndExpiry(t *testing.T) {
	api, _, exec := setup(t)
	ctx := context.Background()
	pid := id.New()
	if err := api.AddLot(ctx, pid, "t", 10); err != nil {
		t.Fatal(err)
	}

	released, committed, expiring := id.New(), id.New(), id.New()
	for _, o := range []uuid.UUID{released, committed, expiring} {
		if err := api.Reserve(ctx, o, []inventory.Item{{ProductID: pid, Quantity: 2}}); err != nil {
			t.Fatal(err)
		}
	}
	if got := stock(t, api, pid); got != 4 {
		t.Fatalf("stock = %d, want 4", got)
	}

	if err := api.Release(ctx, released); err != nil {
		t.Fatal(err)
	}
	if err := api.Release(ctx, released); err != nil { // idempotent
		t.Fatal(err)
	}
	if got := stock(t, api, pid); got != 6 {
		t.Fatalf("stock after release = %d, want 6", got)
	}

	if err := api.Commit(ctx, committed); err != nil {
		t.Fatal(err)
	}
	if err := api.Commit(ctx, released); !errors.Is(err, inventory.ErrReservationLost) {
		t.Fatalf("commit after release = %v, want ErrReservationLost", err)
	}

	// Age every reservation: only still-reserved ones may be freed.
	exec(`UPDATE inventory.reservations SET expires_at = now() - interval '1 minute' WHERE product_id = $1`, pid)
	if _, err := api.ReleaseExpired(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if got := stock(t, api, pid); got != 8 { // 'expiring' came back; 'committed' stays sold
		t.Fatalf("stock after expiry = %d, want 8", got)
	}
}

// An order's lines must be released together even if the sweep batch is smaller than the order.
func TestExpirySweepReleasesWholeOrders(t *testing.T) {
	api, _, exec := setup(t)
	ctx := context.Background()
	order := id.New()
	var pids []uuid.UUID
	var items []inventory.Item
	for range 3 {
		pid := id.New()
		pids = append(pids, pid)
		if err := api.AddLot(ctx, pid, "t", 5); err != nil {
			t.Fatal(err)
		}
		items = append(items, inventory.Item{ProductID: pid, Quantity: 2})
	}
	if err := api.Reserve(ctx, order, items); err != nil {
		t.Fatal(err)
	}
	for _, pid := range pids {
		exec(`UPDATE inventory.reservations SET expires_at = now() - interval '1 minute' WHERE product_id = $1`, pid)
	}
	if _, err := api.ReleaseExpired(ctx, 1); err != nil { // batch of ONE reservation
		t.Fatal(err)
	}
	for _, pid := range pids {
		if got := stock(t, api, pid); got != 5 {
			t.Fatalf("product %s stock = %d, want 5: the order was only partly released", pid, got)
		}
	}
	if err := api.Commit(ctx, order); !errors.Is(err, inventory.ErrReservationLost) {
		t.Fatalf("commit after full release = %v, want ErrReservationLost", err)
	}
}
