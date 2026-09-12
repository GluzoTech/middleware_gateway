package middleware

import "github.com/gin-gonic/gin"

// SecureHeaders sets conservative browser-protection headers on every
// response. The gateway is an API, so nothing it returns should be framed,
// sniffed, cached or used as a referrer.
func SecureHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		c.Next()
	}
}
