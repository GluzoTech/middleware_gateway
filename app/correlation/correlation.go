// Package correlation defines the correlation ID that ties together every
// record produced while processing one inbound event: the HTTP request, the
// queued job, the workflow state, each action attempt, every outbound API call
// and every log line.
package correlation

import (
	"context"

	"github.com/google/uuid"
)

// HeaderName is the HTTP header carrying the correlation ID on responses and
// on outbound calls to external systems.
const HeaderName = "X-Correlation-ID"

// Prefix distinguishes gateway correlation IDs from external identifiers when
// they appear side by side in logs or support tickets.
const Prefix = "INT-"

// MaxLength bounds identifiers accepted from outside (for example, path
// parameters on the admin API) so they can safely be used as file names.
const MaxLength = 64

type contextKey struct{}

// New generates a fresh correlation ID.
func New() string {
	return Prefix + uuid.NewString()
}

// WithID returns a context carrying id.
func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// FromContext returns the correlation ID stored in ctx, or "" if none is set.
func FromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(contextKey{}).(string)
	return id
}

// IsValid reports whether id is a well-formed correlation ID that is safe to
// use in file paths and log queries. Only ASCII letters, digits, '-', '_', '.'
// and ':' are allowed, and the value must be non-empty and at most MaxLength.
func IsValid(id string) bool {
	if id == "" || len(id) > MaxLength || id == "." || id == ".." {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case c == '-', c == '_', c == '.', c == ':':
		default:
			return false
		}
	}
	return true
}
