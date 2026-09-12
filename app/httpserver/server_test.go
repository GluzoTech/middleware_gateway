package httpserver_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/gluzo/integration-gateway/app/config"
	"github.com/gluzo/integration-gateway/app/httpserver"
)

func testHTTPConfig() config.HTTP {
	return config.HTTP{
		ReadHeaderTimeout: time.Second,
		ReadTimeout:       time.Second,
		WriteTimeout:      time.Second,
		IdleTimeout:       time.Second,
		MaxBodyBytes:      1024,
	}
}

func TestNewSetsTimeouts(t *testing.T) {
	srv := httpserver.New(8080, testHTTPConfig(), http.NotFoundHandler())
	if srv.Addr != ":8080" {
		t.Fatalf("addr = %q", srv.Addr)
	}
	if srv.ReadHeaderTimeout == 0 || srv.ReadTimeout == 0 || srv.WriteTimeout == 0 || srv.IdleTimeout == 0 {
		t.Fatal("a server timeout was left unbounded")
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httpserver.New(0, testHTTPConfig(), http.NotFoundHandler())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- httpserver.Run(ctx, srv, time.Second, logger) }()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestRunReturnsListenError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httpserver.New(0, testHTTPConfig(), http.NotFoundHandler())
	srv.Addr = "256.256.256.256:1"

	if err := httpserver.Run(context.Background(), srv, time.Second, logger); err == nil {
		t.Fatal("expected listen error")
	}
}
