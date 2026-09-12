// Package middleware contains the Gin middleware shared by every route.
package middleware

import (
	"github.com/gin-gonic/gin"

	"github.com/gluzo/integration-gateway/app/correlation"
)

// Correlation assigns a fresh correlation ID to every request.
//
// The ID is generated server-side rather than accepted from the caller so
// that an external system can never collide with, or replay, another
// request's identifier. It is stored on the request context, exposed through
// CorrelationID, and echoed in the response header so callers can quote it
// when raising support requests.
func Correlation() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := correlation.New()
		c.Request = c.Request.WithContext(correlation.WithID(c.Request.Context(), id))
		c.Writer.Header().Set(correlation.HeaderName, id)
		c.Next()
	}
}

// CorrelationID returns the correlation ID assigned to the current request.
func CorrelationID(c *gin.Context) string {
	return correlation.FromContext(c.Request.Context())
}
