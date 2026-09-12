package middleware

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
)

// RequestLogger emits one structured log line per request.
//
// Only the path is logged, never the query string or headers, so credentials
// passed by mistake in a URL or header cannot end up in the log stream. Paths
// listed in quietPaths (typically health probes) are logged at debug level so
// routine probing does not drown out real traffic.
func RequestLogger(logger *slog.Logger, quietPaths ...string) gin.HandlerFunc {
	quiet := make(map[string]struct{}, len(quietPaths))
	for _, p := range quietPaths {
		quiet[p] = struct{}{}
	}

	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		status := c.Writer.Status()
		level := slog.LevelInfo
		switch {
		case status >= 500:
			level = slog.LevelError
		case status >= 400:
			level = slog.LevelWarn
		}
		if _, ok := quiet[c.Request.URL.Path]; ok && level == slog.LevelInfo {
			level = slog.LevelDebug
		}

		attrs := []any{
			slog.String("method", c.Request.Method),
			slog.String("path", c.Request.URL.Path),
			slog.Int("status", status),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			slog.Int("bytes", c.Writer.Size()),
			slog.String("client_ip", c.ClientIP()),
			slog.String("correlation_id", CorrelationID(c)),
		}
		if len(c.Errors) > 0 {
			attrs = append(attrs, slog.String("errors", c.Errors.String()))
		}
		logger.Log(c.Request.Context(), level, "http request", attrs...)
	}
}
