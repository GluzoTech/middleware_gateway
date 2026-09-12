// Package httpserver assembles the Gin router and the hardened net/http
// server that fronts it.
package httpserver

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/gluzo/integration-gateway/app/health"
	"github.com/gluzo/integration-gateway/app/httpserver/middleware"
)

// Dependencies lists everything the router needs. Handlers for later phases
// (webhooks, admin log viewer) are added here as optional fields.
type Dependencies struct {
	Logger       *slog.Logger
	Health       *health.Handler
	MaxBodyBytes int64
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

	r.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found", "correlation_id": middleware.CorrelationID(c)})
	})
	r.NoMethod(func(c *gin.Context) {
		c.JSON(http.StatusMethodNotAllowed, gin.H{"error": "method not allowed", "correlation_id": middleware.CorrelationID(c)})
	})

	return r
}
