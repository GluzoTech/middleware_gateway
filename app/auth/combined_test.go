package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/gluzo/integration-gateway/app/auth"
)

func TestAcceptCombinedCredential(t *testing.T) {
	tests := []struct {
		name     string
		headers  map[string]string
		wantKey  string
		wantAuth string
	}{
		{"splits combined header", map[string]string{"Access-Token": "gluzo_pk_a:gluzo_at_b"}, "gluzo_pk_a", "Bearer gluzo_at_b"},
		{"trims whitespace", map[string]string{"Access-Token": "  gluzo_pk_a : gluzo_at_b  "}, "gluzo_pk_a", "Bearer gluzo_at_b"},
		{"native headers win", map[string]string{"Access-Token": "x:y", "X-API-Key": "native", "Authorization": "Bearer native"}, "native", "Bearer native"},
		{"partial native header is not overwritten", map[string]string{"Access-Token": "x:y", "X-API-Key": "native"}, "native", ""},
		{"no separator is ignored", map[string]string{"Access-Token": "just-one-value"}, "", ""},
		{"empty parts are ignored", map[string]string{"Access-Token": ":token"}, "", ""},
		{"absent header", map[string]string{}, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			var gotKey, gotAuth string
			r := gin.New()
			r.Use(auth.AcceptCombinedCredential(""))
			r.POST("/", func(c *gin.Context) {
				gotKey, gotAuth = c.GetHeader(auth.PlatformKeyHeader), c.GetHeader("Authorization")
				c.Status(http.StatusOK)
			})
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			r.ServeHTTP(httptest.NewRecorder(), req)
			if gotKey != tt.wantKey || gotAuth != tt.wantAuth {
				t.Fatalf("key=%q auth=%q, want key=%q auth=%q", gotKey, gotAuth, tt.wantKey, tt.wantAuth)
			}
		})
	}
}
