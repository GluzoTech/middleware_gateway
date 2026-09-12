package middleware_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/gluzo/integration-gateway/app/correlation"
	"github.com/gluzo/integration-gateway/app/httpserver/middleware"
)

func newEngine(mw ...gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(mw...)
	return r
}

func TestCorrelationAssignsServerGeneratedID(t *testing.T) {
	r := newEngine(middleware.Correlation())
	var seen string
	r.GET("/", func(c *gin.Context) {
		seen = middleware.CorrelationID(c)
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(correlation.HeaderName, "client-supplied")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	got := w.Header().Get(correlation.HeaderName)
	if !strings.HasPrefix(got, correlation.Prefix) {
		t.Fatalf("response header %q lacks prefix %q", got, correlation.Prefix)
	}
	if got == "client-supplied" {
		t.Fatal("inbound correlation header must not be trusted")
	}
	if seen != got {
		t.Fatalf("handler saw %q but response carried %q", seen, got)
	}
}

func TestBodyLimit(t *testing.T) {
	const limit = 10
	r := newEngine(middleware.Correlation(), middleware.BodyLimit(limit))
	r.POST("/", func(c *gin.Context) {
		if _, err := io.ReadAll(c.Request.Body); err != nil {
			if middleware.IsBodyTooLarge(err) {
				c.AbortWithStatus(http.StatusRequestEntityTooLarge)
				return
			}
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}
		c.Status(http.StatusOK)
	})

	tests := []struct {
		name          string
		body          string
		contentLength int64 // -1 keeps the value httptest derives from the body
		wantStatus    int
	}{
		{"within limit", "12345", -1, http.StatusOK},
		{"declared oversize rejected before reading", strings.Repeat("x", 50), -1, http.StatusRequestEntityTooLarge},
		{"undeclared oversize rejected while reading", strings.Repeat("x", 50), 0, http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			if tt.contentLength >= 0 {
				req.ContentLength = tt.contentLength
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}

func TestSecureHeaders(t *testing.T) {
	r := newEngine(middleware.SecureHeaders())
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
		"Cache-Control":          "no-store",
	}
	for k, v := range want {
		if got := w.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestRecoveryReturns500AndLogs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	r := newEngine(middleware.Correlation(), middleware.Recovery(logger))
	r.GET("/boom", func(c *gin.Context) { panic("kaboom") })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body["error"] != "internal server error" || !strings.HasPrefix(body["correlation_id"], correlation.Prefix) {
		t.Fatalf("unexpected body: %v", body)
	}
	if !strings.Contains(buf.String(), "panic recovered") || !strings.Contains(buf.String(), "kaboom") {
		t.Fatalf("panic was not logged: %s", buf.String())
	}
}

func TestRequestLoggerLevels(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r := newEngine(middleware.Correlation(), middleware.RequestLogger(logger, "/health"))
	r.GET("/health", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/ok", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/missing", func(c *gin.Context) { c.Status(http.StatusNotFound) })
	r.GET("/broken", func(c *gin.Context) { c.Status(http.StatusInternalServerError) })

	tests := []struct {
		path      string
		wantLevel string
	}{
		{"/health", "DEBUG"},
		{"/ok", "INFO"},
		{"/missing", "WARN"},
		{"/broken", "ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			buf.Reset()
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tt.path+"?token=secret", nil))

			var entry map[string]any
			if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
				t.Fatalf("log line is not JSON: %v (%s)", err, buf.String())
			}
			if entry["level"] != tt.wantLevel {
				t.Fatalf("level = %v, want %s", entry["level"], tt.wantLevel)
			}
			if entry["path"] != tt.path {
				t.Fatalf("path = %v, want %s", entry["path"], tt.path)
			}
			if strings.Contains(buf.String(), "secret") {
				t.Fatalf("query string leaked into log: %s", buf.String())
			}
			if id, _ := entry["correlation_id"].(string); !strings.HasPrefix(id, correlation.Prefix) {
				t.Fatalf("correlation_id missing from log entry: %v", entry)
			}
		})
	}
}
