package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/gluzo/integration-gateway/app/correlation"
)

// PlatformKeyHeader carries the tier-1 credential.
const PlatformKeyHeader = "X-API-Key"

// RequirePlatformKey rejects requests that do not carry a valid platform API
// key and stores the platform on the request context for later handlers.
func RequirePlatformKey(verifier PlatformKeyVerifier, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := strings.TrimSpace(c.GetHeader(PlatformKeyHeader))
		if key == "" {
			reject(c, logger, "platform", http.StatusUnauthorized, "missing platform API key", nil)
			return
		}
		platform, err := verifier.VerifyPlatformKey(c.Request.Context(), key)
		if err != nil {
			status, reason := classify(err)
			reject(c, logger, "platform", status, reason, err)
			return
		}
		c.Request = c.Request.WithContext(WithPlatform(c.Request.Context(), platform))
		c.Next()
	}
}

// RequireAccessToken rejects requests that do not carry a valid integration
// access token. When RequirePlatformKey ran earlier in the chain, the token's
// integration must belong to that platform.
func RequireAccessToken(verifier AccessTokenVerifier, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := bearerToken(c.GetHeader("Authorization"))
		if !ok {
			reject(c, logger, "integration", http.StatusUnauthorized, "missing or malformed bearer token", nil)
			return
		}
		principal, err := verifier.VerifyAccessToken(c.Request.Context(), token)
		if err != nil {
			status, reason := classify(err)
			reject(c, logger, "integration", status, reason, err)
			return
		}
		if platform, ok := PlatformFromContext(c.Request.Context()); ok && principal.Integration.SourcePlatformID != platform.ID {
			reject(c, logger, "integration", http.StatusForbidden, "integration is not bound to the authenticated platform", nil)
			return
		}
		c.Request = c.Request.WithContext(WithPrincipal(c.Request.Context(), principal))
		c.Next()
	}
}

// bearerToken extracts the credential from an "Authorization: Bearer <token>"
// header. The scheme is matched case-insensitively as RFC 6750 allows.
func bearerToken(header string) (string, bool) {
	parts := strings.SplitN(strings.TrimSpace(header), " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}
	token := strings.TrimSpace(parts[1])
	return token, token != ""
}

// classify maps a verifier error to an HTTP status and a reason that is safe
// to return to the caller. Unknown errors are infrastructure failures; the
// caller should retry later rather than conclude its credentials are wrong.
func classify(err error) (int, string) {
	switch {
	case errors.Is(err, ErrInvalidCredentials), errors.Is(err, ErrTokenExpired), errors.Is(err, ErrTokenRevoked):
		return http.StatusUnauthorized, err.Error()
	case errors.Is(err, ErrPlatformDisabled), errors.Is(err, ErrIntegrationDisabled):
		return http.StatusForbidden, err.Error()
	default:
		return http.StatusServiceUnavailable, "authentication temporarily unavailable"
	}
}

func reject(c *gin.Context, logger *slog.Logger, tier string, status int, reason string, cause error) {
	ctx := c.Request.Context()
	attrs := []any{
		slog.String("tier", tier),
		slog.String("reason", reason),
		slog.Int("status", status),
		slog.String("client_ip", c.ClientIP()),
		slog.String("correlation_id", correlation.FromContext(ctx)),
	}
	if status == http.StatusServiceUnavailable {
		logger.ErrorContext(ctx, "authentication unavailable", append(attrs, slog.String("error", cause.Error()))...)
	} else {
		logger.WarnContext(ctx, "authentication failed", attrs...)
	}

	if status == http.StatusUnauthorized {
		c.Header("WWW-Authenticate", `Bearer realm="gluzo-integration-gateway"`)
	}
	c.AbortWithStatusJSON(status, gin.H{
		"error":          strings.ToLower(http.StatusText(status)),
		"reason":         reason,
		"correlation_id": correlation.FromContext(ctx),
	})
}
