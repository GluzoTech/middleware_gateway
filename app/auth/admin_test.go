package auth_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/gluzo/integration-gateway/app/auth"
)

func TestRequireAdminToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	newRouter := func(token string) *gin.Engine {
		r := gin.New()
		r.GET("/admin/logs", auth.RequireAdminToken(token, logger), func(c *gin.Context) { c.Status(http.StatusOK) })
		return r
	}

	tests := []struct {
		name       string
		configured string
		headers    map[string]string
		wantStatus int
	}{
		{"bearer token", "s3cret", map[string]string{"Authorization": "Bearer s3cret"}, http.StatusOK},
		{"header token", "s3cret", map[string]string{auth.AdminTokenHeader: "s3cret"}, http.StatusOK},
		{"wrong token", "s3cret", map[string]string{"Authorization": "Bearer nope"}, http.StatusUnauthorized},
		{"missing token", "s3cret", nil, http.StatusUnauthorized},
		{"empty configured token fails closed", "", map[string]string{"Authorization": "Bearer "}, http.StatusUnauthorized},
		{"empty configured token rejects everything", "   ", map[string]string{auth.AdminTokenHeader: "anything"}, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/admin/logs", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			newRouter(tt.configured).ServeHTTP(w, req)
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusOK && w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("admin responses must not be cached")
			}
		})
	}
}
