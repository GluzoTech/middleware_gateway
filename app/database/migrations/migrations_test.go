package migrations_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gluzo/integration-gateway/app/database/migrations"
	"github.com/gluzo/integration-gateway/app/database/postgres"
)

func TestLoadOrdersByVersionAndIgnoresOtherFiles(t *testing.T) {
	fsys := fstest.MapFS{
		"0002_second.sql": {Data: []byte("SELECT 2;")},
		"0001_first.sql":  {Data: []byte("  SELECT 1;\n")},
		"README.md":       {Data: []byte("ignored")},
	}
	migs, err := migrations.Load(fsys)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(migs) != 2 {
		t.Fatalf("got %d migrations, want 2", len(migs))
	}
	if migs[0].Version != "0001_first" || migs[1].Version != "0002_second" {
		t.Fatalf("unexpected order: %v", migs)
	}
	if migs[0].SQL != "SELECT 1;" {
		t.Fatalf("SQL not trimmed: %q", migs[0].SQL)
	}
}

func TestLoadRejectsEmptyMigration(t *testing.T) {
	fsys := fstest.MapFS{"0001_empty.sql": {Data: []byte("\n\n")}}
	if _, err := migrations.Load(fsys); err == nil {
		t.Fatal("expected error for empty migration")
	}
}

// TestApplyIntegration runs against a real database when TEST_DATABASE_URL is
// set. It verifies that migrations apply once and that a second run is a no-op.
func TestApplyIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := postgres.Connect(ctx, postgres.Config{URL: url, MaxConns: 2, ConnectTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer pool.Close()

	table := "migrations_test_" + time.Now().Format("20060102150405")
	fsys := fstest.MapFS{
		"9999_" + table + ".sql": {Data: []byte("CREATE TABLE " + table + " (id INT PRIMARY KEY);")},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+table)
		_, _ = pool.Exec(context.Background(), "DELETE FROM schema_migrations WHERE version = $1", "9999_"+table)
	})

	for i := 0; i < 2; i++ {
		if err := migrations.Apply(ctx, pool, fsys, logger); err != nil {
			t.Fatalf("Apply run %d: %v", i+1, err)
		}
	}

	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations WHERE version = $1", "9999_"+table).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Fatalf("migration recorded %d times, want 1", count)
	}
}
