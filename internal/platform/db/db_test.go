package db_test

import (
	"context"
	"testing"

	"github.com/NaheedRayan/goat-architecture/internal/platform/testdb"
)

func TestPoolAppliesServerSideTimeouts(t *testing.T) {
	pool := testdb.Pool(t)
	for param, want := range map[string]string{
		"statement_timeout": "15s", "lock_timeout": "10s", "idle_in_transaction_session_timeout": "30s",
	} {
		var got string
		if err := pool.QueryRow(context.Background(), "SHOW "+param).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s = %s, want %s", param, got, want)
		}
	}
}
