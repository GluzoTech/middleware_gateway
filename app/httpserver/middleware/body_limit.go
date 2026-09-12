package middleware

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

// BodyLimit caps the size of inbound request bodies.
//
// Requests that declare a larger Content-Length are rejected immediately.
// Chunked or under-declared bodies are wrapped in http.MaxBytesReader so a
// handler reading past the limit receives an error that IsBodyTooLarge
// recognises and can translate into a 413.
func BodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.ContentLength > maxBytes {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
				"error":          "request body too large",
				"correlation_id": CorrelationID(c),
			})
			return
		}
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
	}
}

// IsBodyTooLarge reports whether err was caused by exceeding the BodyLimit.
func IsBodyTooLarge(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}
