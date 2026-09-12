package httpserver_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gluzo/integration-gateway/app/correlation"
	"github.com/gluzo/integration-gateway/app/health"
	"github.com/gluzo/integration-gateway/app/httpserver"
)

func newTestRouter() http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return httpserver.NewRouter(httpserver.Dependencies{
		Logger:       logger,
		Health:       health.NewHandler("svc", "test", time.Second, logger),
		MaxBodyBytes: 1024,
	})
}

func TestRouter(t *testing.T) {
	r := newTestRouter()

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantError  string
	}{
		{"liveness", http.MethodGet, "/health", http.StatusOK, ""},
		{"readiness", http.MethodGet, "/ready", http.StatusOK, ""},
		{"unknown route", http.MethodGet, "/nope", http.StatusNotFound, "not found"},
		{"wrong method", http.MethodPost, "/health", http.StatusMethodNotAllowed, "method not allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(tt.method, tt.path, nil))
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if !strings.HasPrefix(w.Header().Get(correlation.HeaderName), correlation.Prefix) {
				t.Fatalf("missing correlation header on %s %s", tt.method, tt.path)
			}
			if w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("missing secure headers on %s %s", tt.method, tt.path)
			}
			if tt.wantError != "" {
				var body map[string]string
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatalf("body is not JSON: %v", err)
				}
				if body["error"] != tt.wantError {
					t.Fatalf("error = %q, want %q", body["error"], tt.wantError)
				}
			}
		})
	}
}
