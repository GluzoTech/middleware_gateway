package health_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/gluzo/integration-gateway/app/health"
)

type fakeChecker struct {
	name string
	err  error
}

func (f fakeChecker) Name() string                { return f.name }
func (f fakeChecker) Check(context.Context) error { return f.err }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func serve(h gin.HandlerFunc, path string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET(path, h)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestLive(t *testing.T) {
	h := health.NewHandler("svc", "1.2.3", time.Second, discardLogger())
	w := serve(h.Live, "/health")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["status"] != "ok" || body["service"] != "svc" || body["version"] != "1.2.3" {
		t.Fatalf("unexpected body: %v", body)
	}
}

func TestReady(t *testing.T) {
	tests := []struct {
		name       string
		checkers   []health.Checker
		wantStatus int
		wantBody   string
		wantChecks map[string]string
	}{
		{
			name:       "no dependencies",
			wantStatus: http.StatusOK,
			wantBody:   "ready",
			wantChecks: map[string]string{},
		},
		{
			name:       "all healthy",
			checkers:   []health.Checker{fakeChecker{name: "postgres"}, fakeChecker{name: "redis"}},
			wantStatus: http.StatusOK,
			wantBody:   "ready",
			wantChecks: map[string]string{"postgres": "ok", "redis": "ok"},
		},
		{
			name:       "one failing",
			checkers:   []health.Checker{fakeChecker{name: "postgres"}, fakeChecker{name: "redis", err: errors.New("dial tcp: refused")}},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "not_ready",
			wantChecks: map[string]string{"postgres": "ok", "redis": "failed"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := health.NewHandler("svc", "dev", time.Second, discardLogger(), tt.checkers...)
			w := serve(h.Ready, "/ready")
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			var body struct {
				Status string            `json:"status"`
				Checks map[string]string `json:"checks"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			if body.Status != tt.wantBody {
				t.Fatalf("status field = %q, want %q", body.Status, tt.wantBody)
			}
			if len(body.Checks) != len(tt.wantChecks) {
				t.Fatalf("checks = %v, want %v", body.Checks, tt.wantChecks)
			}
			for k, v := range tt.wantChecks {
				if body.Checks[k] != v {
					t.Errorf("check %s = %q, want %q", k, body.Checks[k], v)
				}
			}
			if strings.Contains(w.Body.String(), "refused") {
				t.Fatalf("dependency error detail leaked into response: %s", w.Body.String())
			}
		})
	}
}
