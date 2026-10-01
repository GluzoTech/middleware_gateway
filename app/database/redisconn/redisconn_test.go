package redisconn_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gluzo/integration-gateway/app/database/redisconn"
)

func TestConnectRejectsMalformedURLWithoutLeakingPassword(t *testing.T) {
	_, err := redisconn.Connect(context.Background(), redisconn.Config{
		URL:            "redis://:hunter2@localhost:notaport/0",
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

	// Port 9 (discard) is reserved and never runs Redis.
	_, err := redisconn.Connect(ctx, redisconn.Config{
		URL:            "redis://127.0.0.1:9/0",
		ConnectTimeout: 500 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected connection error")
	}
}

// TestConnectIntegration exercises a real Redis when TEST_REDIS_URL is set;
// otherwise it is skipped so the unit suite stays hermetic.
func TestConnectIntegration(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := redisconn.Connect(ctx, redisconn.Config{
		URL:            url,
		Password:       os.Getenv("TEST_REDIS_PASSWORD"),
		ConnectTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Close()

	if err := (redisconn.Checker{Client: client}).Check(ctx); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

// A ping failure must name the endpoint, so that a bare deadline error is
// traceable, and must still not carry the password.
func TestConnectPingErrorNamesAddressNotPassword(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := redisconn.Connect(ctx, redisconn.Config{
		URL:            "redis://:hunter2@127.0.0.1:9/0",
		ConnectTimeout: 500 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected connection error")
	}
	if !strings.Contains(err.Error(), "127.0.0.1:9") {
		t.Errorf("error should name the endpoint: %v", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("password leaked into error: %v", err)
	}
}
