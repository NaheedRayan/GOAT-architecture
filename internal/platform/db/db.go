// Package db provides the pgx pool and a transaction helper shared by all modules.
package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens a pool with sane server-side limits: runaway statements, lock
// waits and abandoned transactions are cut off instead of pinning connections.
// maxConns <= 0 keeps the pgx default.
func Connect(ctx context.Context, url string, maxConns ...int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("db config: %w", err)
	}
	if len(maxConns) > 0 && maxConns[0] > 0 {
		cfg.MaxConns = maxConns[0]
	}
	rt := cfg.ConnConfig.RuntimeParams
	for k, v := range map[string]string{
		"statement_timeout":                   "15000",
		"lock_timeout":                        "10000",
		"idle_in_transaction_session_timeout": "30000",
	} {
		if _, set := rt[k]; !set { // an explicit setting in the URL wins
			rt[k] = v
		}
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db ping: %w", err)
	}
	return pool, nil
}

// DBTX is satisfied by both *pgxpool.Pool and pgx.Tx, so sqlc-generated
// queries and repositories work inside or outside a transaction.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type txKey struct{}

// TxManager runs functions inside a transaction that travels in the context,
// so several modules can take part in one atomic unit of work without sharing
// repository types.
type TxManager struct{ pool *pgxpool.Pool }

func NewTxManager(pool *pgxpool.Pool) *TxManager { return &TxManager{pool: pool} }

// WithTx commits if fn returns nil, otherwise rolls back. If ctx already
// carries a transaction, fn joins it.
func (m *TxManager) WithTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return fn(ctx)
	}
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := fn(context.WithValue(ctx, txKey{}, tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Q returns the transaction carried by ctx, or the pool when there is none.
func Q(ctx context.Context, pool *pgxpool.Pool) DBTX {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return tx
	}
	return pool
}

// IsForeignKeyViolation reports whether err is a Postgres foreign_key_violation.
func IsForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errorsAs(err, &pgErr) && pgErr.Code == "23503"
}

// UniqueViolationConstraint returns the violated constraint's name, or "" if err is not a unique violation.
func UniqueViolationConstraint(err error) string {
	var pgErr *pgconn.PgError
	if errorsAs(err, &pgErr) && pgErr.Code == "23505" {
		return pgErr.ConstraintName
	}
	return ""
}

// IsUniqueViolation reports whether err is a Postgres unique_violation.
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errorsAs(err, &pgErr) && pgErr.Code == "23505"
}
