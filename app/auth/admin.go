package auth

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/gluzo/integration-gateway/app/correlation"
)

// AdminTokenHeader is an alternative carrier for the admin token when a
// browser or tool cannot set Authorization.
const AdminTokenHeader = "X-Admin-Token"

// RequireAdminToken protects operator endpoints with a single shared secret
// supplied as "Authorization: Bearer <token>" or X-Admin-Token. Comparison
// is constant-time over digests. An empty configured token rejects every
// request, so misconfiguration fails closed.
func RequireAdminToken(token string, logger *slog.Logger) gin.HandlerFunc {
	expected := HashSecret(strings.TrimSpace(token))
	configured := strings.TrimSpace(token) != ""
	return func(c *gin.Context) {
		presented, ok := bearerToken(c.GetHeader("Authorization"))
		if !ok {
			presented = strings.TrimSpace(c.GetHeader(AdminTokenHeader))
		}
		if !configured || presented == "" || !hashesEqual(HashSecret(presented), expected) {
			reason := "invalid admin token"
			if presented == "" {
				reason = "missing admin token"
			}
			reject(c, logger, "admin", http.StatusUnauthorized, reason, nil)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Next()
	}
}

// adminCorrelation is a small helper for handlers that want the request's
// correlation ID without importing the middleware package.
func adminCorrelation(c *gin.Context) string {
	return correlation.FromContext(c.Request.Context())
}
