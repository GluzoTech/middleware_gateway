package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
)

// Recovery converts a panic into a 500 response and a structured error log,
// keeping the process alive for other requests.
func Recovery(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			logger.ErrorContext(c.Request.Context(), "panic recovered",
				slog.String("panic", fmt.Sprint(r)),
				slog.String("correlation_id", CorrelationID(c)),
				slog.String("stack", string(debug.Stack())),
			)
			if c.Writer.Written() {
				c.Abort()
				return
			}
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":          "internal server error",
				"correlation_id": CorrelationID(c),
			})
		}()
		c.Next()
	}
}
