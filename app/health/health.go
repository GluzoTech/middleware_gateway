// Package health exposes liveness and readiness endpoints.
//
// Liveness answers "is the process up" and never touches dependencies.
// Readiness answers "can this process do useful work" by probing each
// registered Checker with a short timeout. Neither endpoint reveals
// configuration, connection strings or dependency error details.
package health

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Checker probes one dependency.
type Checker interface {
	Name() string
	Check(ctx context.Context) error
}

// Handler serves the health endpoints.
type Handler struct {
	service  string
	version  string
	timeout  time.Duration
	logger   *slog.Logger
	checkers []Checker
}

// NewHandler builds a Handler. timeout bounds the readiness probe as a whole.
func NewHandler(service, version string, timeout time.Duration, logger *slog.Logger, checkers ...Checker) *Handler {
	return &Handler{
		service:  service,
		version:  version,
		timeout:  timeout,
		logger:   logger,
		checkers: checkers,
	}
}

// Live reports that the process is running.
func (h *Handler) Live(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"service": h.service,
		"version": h.version,
	})
}

// Ready reports whether every dependency responds. Checks run concurrently so
// one slow dependency does not delay the others.
func (h *Handler) Ready(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), h.timeout)
	defer cancel()

	results := make(map[string]string, len(h.checkers))
	healthy := true
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, chk := range h.checkers {
		wg.Add(1)
		go func(chk Checker) {
			defer wg.Done()
			err := chk.Check(ctx)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				healthy = false
				results[chk.Name()] = "failed"
				h.logger.WarnContext(ctx, "readiness check failed",
					slog.String("check", chk.Name()),
					slog.String("error", err.Error()),
				)
				return
			}
			results[chk.Name()] = "ok"
		}(chk)
	}
	wg.Wait()

	status, code := "ready", http.StatusOK
	if !healthy {
		status, code = "not_ready", http.StatusServiceUnavailable
	}
	c.JSON(code, gin.H{
		"status": status,
		"checks": results,
	})
}
