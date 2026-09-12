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

	"github.com/google/uuid"

	"github.com/gluzo/integration-gateway/app/auth"
	"github.com/gluzo/integration-gateway/app/correlation"
	"github.com/gluzo/integration-gateway/app/health"
	"github.com/gluzo/integration-gateway/app/httpserver"
	"github.com/gluzo/integration-gateway/app/idempotency"
	"github.com/gluzo/integration-gateway/app/integrations/easyecom"
	"github.com/gluzo/integration-gateway/app/queue"
	"github.com/gluzo/integration-gateway/app/webhook"
)

const (
	testPlatformKey = "gluzo_pk_test"
	testToken       = "gluzo_at_test"
)

func newTestRouter() http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	creds := auth.NewMemoryStore()
	platform := auth.Platform{ID: uuid.New(), Name: "easyecom", Type: auth.PlatformTypeSource, Status: auth.StatusActive}
	creds.AddPlatform(platform, testPlatformKey)
	integ := auth.Integration{ID: uuid.New(), Name: "easyecom-dabur", SourcePlatformID: platform.ID, Status: auth.StatusActive}
	creds.AddToken(auth.Token{ID: uuid.New(), IntegrationID: integ.ID}, integ, testToken)

	return httpserver.NewRouter(httpserver.Dependencies{
		Logger:       logger,
		Health:       health.NewHandler("svc", "test", time.Second, logger),
		MaxBodyBytes: 1024,
		PlatformKeys: creds,
		AccessTokens: creds,
		Webhooks: []*webhook.Handler{
			webhook.NewHandler(easyecom.WebhookParser{}, idempotency.NewMemoryStore(), queue.NewMemory(4), logger),
		},
	})
}

func TestRouter(t *testing.T) {
	r := newTestRouter()

	tests := []struct {
		name       string
		method     string
		path       string
		headers    map[string]string
		body       string
		wantStatus int
		wantError  string
	}{
		{"liveness", http.MethodGet, "/health", nil, "", http.StatusOK, ""},
		{"readiness", http.MethodGet, "/ready", nil, "", http.StatusOK, ""},
		{"unknown route", http.MethodGet, "/nope", nil, "", http.StatusNotFound, "not found"},
		{"wrong method", http.MethodPost, "/health", nil, "", http.StatusMethodNotAllowed, "method not allowed"},
		{"webhook without credentials", http.MethodPost, "/webhooks/easyecom", nil, "[]", http.StatusUnauthorized, "unauthorized"},
		{"webhook event without credentials", http.MethodPost, "/webhooks/easyecom/order-confirmed", nil, "[]", http.StatusUnauthorized, "unauthorized"},
		{"webhook with credentials", http.MethodPost, "/webhooks/easyecom",
			map[string]string{auth.PlatformKeyHeader: testPlatformKey, "Authorization": "Bearer " + testToken},
			`[{"order_id":1,"warehouse_id":5}]`, http.StatusAccepted, ""},
		{"webhook with combined header", http.MethodPost, "/webhooks/easyecom/order-created",
			map[string]string{auth.CombinedCredentialHeader: testPlatformKey + ":" + testToken},
			`[{"order_id":2,"warehouse_id":5}]`, http.StatusAccepted, ""},
		{"webhook GET not allowed", http.MethodGet, "/webhooks/easyecom", nil, "", http.StatusMethodNotAllowed, "method not allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tt.wantStatus, w.Body.String())
			}
			if !strings.HasPrefix(w.Header().Get(correlation.HeaderName), correlation.Prefix) {
				t.Fatalf("missing correlation header on %s %s", tt.method, tt.path)
			}
			if w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("missing secure headers on %s %s", tt.method, tt.path)
			}
			if tt.wantError != "" {
				var body map[string]any
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
