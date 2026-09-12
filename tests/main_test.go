// Package tests holds integration tests that run against real infrastructure.
//
// PostgreSQL comes from TEST_DATABASE_URL when set; otherwise an embedded
// PostgreSQL server is started for the duration of the test binary (the
// binaries are downloaded once and cached under the home directory). Pass
// -short to skip the whole package.
package tests

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gluzo/integration-gateway/app/database/migrations"
	"github.com/gluzo/integration-gateway/app/database/postgres"
)

// pool is shared by every test in the package. The schema is migrated once
// in TestMain; tests create their own uniquely named rows.
var pool *pgxpool.Pool

func TestMain(m *testing.M) {
	code, err := run(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "integration tests:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func run(m *testing.M) (int, error) {
	flag.Parse()
	if testing.Short() {
		fmt.Println("skipping integration tests in -short mode")
		return 0, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		var stop func()
		var err error
		url, stop, err = startEmbeddedPostgres()
		if err != nil {
			return 0, err
		}
		defer stop()
	}

	p, err := postgres.Connect(ctx, postgres.Config{URL: url, MaxConns: 5, ConnectTimeout: 15 * time.Second})
	if err != nil {
		return 0, err
	}
	defer p.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := migrations.Apply(ctx, p, migrations.Files(), logger); err != nil {
		return 0, err
	}

	pool = p
	return m.Run(), nil
}

func startEmbeddedPostgres() (string, func(), error) {
	port, err := freePort()
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "gateway-pg-")
	if err != nil {
		return "", nil, err
	}

	cfg := embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).
		Port(uint32(port)).
		RuntimePath(filepath.Join(dir, "runtime")).
		DataPath(filepath.Join(dir, "data")).
		StartTimeout(90 * time.Second).
		Logger(io.Discard)
	db := embeddedpostgres.NewDatabase(cfg)
	if err := db.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, fmt.Errorf("start embedded postgres: %w", err)
	}
	stop := func() {
		_ = db.Stop()
		_ = os.RemoveAll(dir)
	}
	url := fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable", port)
	return url, stop, nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
