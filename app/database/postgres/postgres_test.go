package postgres_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gluzo/integration-gateway/app/database/postgres"
)

func TestConnectRejectsMalformedURLWithoutLeakingPassword(t *testing.T) {
	_, err := postgres.Connect(context.Background(), postgres.Config{
		URL:            "postgres://user:hunter2@localhost:notaport/db",
		ConnectTimeout: time.Second,
	})
	if err == nil {
		t.Fatal("expected error for malformed URL")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("password leaked into error: %v", err)
	}
}

func TestConnectFailsFastWhenUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Port 9 (discard) is reserved and never runs PostgreSQL.
	_, err := postgres.Connect(ctx, postgres.Config{
		URL:            "postgres://user:pw@127.0.0.1:9/db?sslmode=disable",
		ConnectTimeout: 500 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected connection error")
	}
}

// TestConnectIntegration exercises a real database when TEST_DATABASE_URL is
// set; otherwise it is skipped so the unit suite stays hermetic.
func TestConnectIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := postgres.Connect(ctx, postgres.Config{URL: url, MaxConns: 2, ConnectTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer pool.Close()

	if err := (postgres.Checker{Pool: pool}).Check(ctx); err != nil {
		t.Fatalf("Check: %v", err)
	}
}
