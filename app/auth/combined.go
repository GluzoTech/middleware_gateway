package auth

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// CombinedCredentialHeader is the header some platforms can populate when
// they cannot send two.
const CombinedCredentialHeader = "Access-Token"

// AcceptCombinedCredential lets a platform that can attach only one header
// (EasyEcom's webhook token) carry both tiers in it as
// "<platform key>:<integration token>". The header is split into X-API-Key
// and Authorization before the regular verifiers run, so both credentials
// are still checked independently and the binding check still applies.
//
// Requests that already carry X-API-Key or Authorization are left untouched,
// and a combined value without a ':' is ignored so the normal chain reports
// the missing platform key.
func AcceptCombinedCredential(header string) gin.HandlerFunc {
	if header == "" {
		header = CombinedCredentialHeader
	}
	return func(c *gin.Context) {
		if c.GetHeader(PlatformKeyHeader) != "" || c.GetHeader("Authorization") != "" {
			c.Next()
			return
		}
		combined := strings.TrimSpace(c.GetHeader(header))
		key, token, ok := strings.Cut(combined, ":")
		key, token = strings.TrimSpace(key), strings.TrimSpace(token)
		if ok && key != "" && token != "" {
			c.Request.Header.Set(PlatformKeyHeader, key)
			c.Request.Header.Set("Authorization", "Bearer "+token)
		}
		c.Next()
	}
}
