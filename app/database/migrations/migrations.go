// Package migrations applies versioned SQL files to PostgreSQL.
//
// Migrations are plain SQL files named NNNN_description.sql and applied in
// lexical order. Applied versions are recorded in schema_migrations so a
// restart never re-runs a migration, and an advisory lock serialises
// concurrent runners (for example two replicas starting at once).
package migrations

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// advisoryLockKey is an arbitrary application-wide constant; any process
// holding it is the only one applying migrations.
const advisoryLockKey int64 = 7_412_098_231

// Migration is one versioned SQL script.
type Migration struct {
	Version string
	SQL     string
}

// Load reads every *.sql file at the root of fsys, sorted by version.
func Load(fsys fs.FS) ([]Migration, error) {
	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("migrations: list files: %w", err)
	}
	sort.Strings(names)

	migs := make([]Migration, 0, len(names))
	for _, name := range names {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("migrations: read %s: %w", name, err)
		}
		sql := strings.TrimSpace(string(data))
		if sql == "" {
			return nil, fmt.Errorf("migrations: %s is empty", name)
		}
		migs = append(migs, Migration{Version: strings.TrimSuffix(name, ".sql"), SQL: sql})
	}
	return migs, nil
}

// Apply runs every migration in fsys that is not yet recorded in
// schema_migrations. Each migration runs in its own transaction together with
// its bookkeeping row, so a failure leaves the database at the previous
// version.
func Apply(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, logger *slog.Logger) error {
	migs, err := Load(fsys)
	if err != nil {
		return err
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migrations: acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return fmt.Errorf("migrations: acquire advisory lock: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", advisoryLockKey); err != nil {
			logger.Warn("migrations: release advisory lock", slog.String("error", err.Error()))
		}
	}()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("migrations: ensure schema_migrations: %w", err)
	}

	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return err
	}

	for _, m := range migs {
		if applied[m.Version] {
			continue
		}
		if err := applyOne(ctx, conn, m); err != nil {
			return err
		}
		logger.Info("migration applied", slog.String("version", m.Version))
	}
	return nil
}

func appliedVersions(ctx context.Context, conn *pgxpool.Conn) (map[string]bool, error) {
	rows, err := conn.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("migrations: read applied versions: %w", err)
	}
	defer rows.Close()

	applied := make(map[string]bool)
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("migrations: scan applied version: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("migrations: read applied versions: %w", err)
	}
	return applied, nil
}

func applyOne(ctx context.Context, conn *pgxpool.Conn, m Migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("migrations: begin %s: %w", m.Version, err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	if _, err := tx.Exec(ctx, m.SQL); err != nil {
		return fmt.Errorf("migrations: apply %s: %w", m.Version, err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", m.Version); err != nil {
		return fmt.Errorf("migrations: record %s: %w", m.Version, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("migrations: commit %s: %w", m.Version, err)
	}
	return nil
}
