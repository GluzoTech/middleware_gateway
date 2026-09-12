// Package httpserver assembles the Gin router and the hardened net/http
// server that fronts it.
package httpserver

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/gluzo/integration-gateway/app/admin"
	"github.com/gluzo/integration-gateway/app/auth"
	"github.com/gluzo/integration-gateway/app/health"
	"github.com/gluzo/integration-gateway/app/httpserver/middleware"
	"github.com/gluzo/integration-gateway/app/webhook"
)

// Dependencies lists everything the router needs. Optional handlers are
// mounted only when supplied.
type Dependencies struct {
	Logger       *slog.Logger
	Health       *health.Handler
	MaxBodyBytes int64

	// Webhook intake. Each handler is mounted at /webhooks/<platform> and
	// /webhooks/<platform>/:event behind both authentication tiers.
	PlatformKeys auth.PlatformKeyVerifier
	AccessTokens auth.AccessTokenVerifier
	Webhooks     []*webhook.Handler

	// Operator endpoints under /admin, mounted only when both a handler and
	// a token are configured.
	Admin      *admin.Handler
	AdminToken string
}

// NewRouter builds the HTTP routing table with the shared middleware chain.
//
// Middleware order matters: correlation first so every later stage can tag
// its output; the request logger outside recovery so a recovered panic is
// still logged as a 500; the body limit innermost so it applies to handlers
// only.
func NewRouter(deps Dependencies) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	r := gin.New()
	r.HandleMethodNotAllowed = true
	// Trust no proxies by default: ClientIP falls back to the socket peer.
	// Deployments behind a load balancer configure this explicitly.
	_ = r.SetTrustedProxies(nil)

	r.Use(
		middleware.Correlation(),
		middleware.SecureHeaders(),
		middleware.RequestLogger(deps.Logger, "/health", "/ready"),
		middleware.Recovery(deps.Logger),
		middleware.BodyLimit(deps.MaxBodyBytes),
	)

	r.GET("/health", deps.Health.Live)
	r.GET("/ready", deps.Health.Ready)

	for _, h := range deps.Webhooks {
		group := r.Group("/webhooks/"+h.Platform(),
			auth.AcceptCombinedCredential(auth.CombinedCredentialHeader),
			auth.RequirePlatformKey(deps.PlatformKeys, deps.Logger),
			auth.RequireAccessToken(deps.AccessTokens, deps.Logger),
		)
		group.POST("", h.Handle)
		group.POST("/:event", h.Handle)
	}

	if deps.Admin != nil && strings.TrimSpace(deps.AdminToken) != "" {
		deps.Admin.Register(r.Group("/admin", auth.RequireAdminToken(deps.AdminToken, deps.Logger)))
	}

	r.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found", "correlation_id": middleware.CorrelationID(c)})
	})
	r.NoMethod(func(c *gin.Context) {
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "method not allowed", "correlation_id": middleware.CorrelationID(c)})
	})

	return r
}
