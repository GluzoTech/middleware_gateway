// Package postgres owns the PostgreSQL connection pool used for relational
// configuration: platforms, integrations, routes, credentials and
// idempotency records.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config describes how to reach the database.
type Config struct {
	URL            string
	MaxConns       int32
	ConnectTimeout time.Duration
}

// Connect opens a pool and verifies connectivity with a bounded ping.
//
// pgx redacts passwords when it reports connection-string errors, so wrapped
// errors are safe to log.
func Connect(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse configuration: %w", err)
	}
	if cfg.MaxConns > 0 {
		poolCfg.MaxConns = cfg.MaxConns
	}
	if cfg.ConnectTimeout > 0 {
		poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: create pool: %w", err)
	}

	pingCtx := ctx
	if cfg.ConnectTimeout > 0 {
		var cancel context.CancelFunc
		pingCtx, cancel = context.WithTimeout(ctx, cfg.ConnectTimeout)
		defer cancel()
	}
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	return pool, nil
}

// Checker adapts a pool to the readiness probe.
type Checker struct {
	Pool *pgxpool.Pool
}

// Name implements health.Checker.
func (c Checker) Name() string { return "postgres" }

// Check implements health.Checker.
func (c Checker) Check(ctx context.Context) error { return c.Pool.Ping(ctx) }
