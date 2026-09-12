// Package apperror defines the error categories shared across the gateway
// and the retry semantics attached to them.
//
// Every failure that crosses a package boundary is, or can be normalised
// into, an *Error. The category drives logging and the admin log viewer; the
// Retryable flag drives action-level retries; the external fields preserve
// what a remote API said without ever carrying credentials.
package apperror

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// Category classifies a failure.
type Category string

// Categories. The set is deliberately small so that dashboards and retry
// policy stay understandable.
const (
	Validation     Category = "validation_error"
	Authentication Category = "authentication_error"
	Authorization  Category = "authorization_error"
	Network        Category = "network_error"
	Timeout        Category = "timeout_error"
	RateLimit      Category = "rate_limit_error"
	ExternalAPI    Category = "external_api_error"
	Mapping        Category = "mapping_error"
	Workflow       Category = "workflow_error"
	NonRetryable   Category = "non_retryable_error"
	Internal       Category = "internal_error"
)

// Error is the gateway's structured error.
type Error struct {
	Category  Category
	Message   string // safe to log and to return to callers
	Retryable bool

	// Context of the failing operation, when known.
	Integration string // e.g. "easyecom", "dabur"
	Operation   string // e.g. "GetOrderDetails"

	// What the remote API said, when the failure came from one.
	HTTPStatus      int
	ExternalCode    string
	ExternalMessage string
	RetryAfter      time.Duration

	Err error // wrapped cause
}

// Error implements error.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Category))
	b.WriteString(": ")
	b.WriteString(e.Message)
	if e.Integration != "" || e.Operation != "" {
		b.WriteString(" [")
		b.WriteString(strings.TrimSpace(e.Integration + " " + e.Operation))
		b.WriteString("]")
	}
	if e.HTTPStatus != 0 || e.ExternalCode != "" || e.ExternalMessage != "" {
		b.WriteString(" (")
		var parts []string
		if e.HTTPStatus != 0 {
			parts = append(parts, fmt.Sprintf("http %d", e.HTTPStatus))
		}
		if e.ExternalCode != "" {
			parts = append(parts, "code "+e.ExternalCode)
		}
		if e.ExternalMessage != "" {
			parts = append(parts, e.ExternalMessage)
		}
		b.WriteString(strings.Join(parts, "; "))
		b.WriteString(")")
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

// Unwrap exposes the cause to errors.Is and errors.As.
func (e *Error) Unwrap() error { return e.Err }

// New creates an error with the category's default retry behaviour.
func New(category Category, message string) *Error {
	return &Error{Category: category, Message: message, Retryable: defaultRetryable(category)}
}

// Wrap creates an error carrying cause.
func Wrap(category Category, message string, cause error) *Error {
	e := New(category, message)
	e.Err = cause
	return e
}

// defaultRetryable reports whether a category is transient by nature.
// External API failures are decided per status by FromHTTPStatus.
func defaultRetryable(c Category) bool {
	switch c {
	case Network, Timeout, RateLimit:
		return true
	default:
		return false
	}
}

// FromHTTPStatus maps a response status to a category and a retry decision.
//
//	400, 422       validation      no retry
//	401            authentication  no retry
//	403            authorization   no retry
//	404, 409, 4xx  external API    no retry
//	408            timeout         retry
//	429            rate limit      retry (respect Retry-After)
//	5xx            external API    retry
func FromHTTPStatus(status int) (Category, bool) {
	switch {
	case status == 408:
		return Timeout, true
	case status == 429:
		return RateLimit, true
	case status == 401:
		return Authentication, false
	case status == 403:
		return Authorization, false
	case status == 400 || status == 422:
		return Validation, false
	case status >= 500:
		return ExternalAPI, true
	default:
		return ExternalAPI, false
	}
}

// Classify normalises any error into *Error. Errors that already are, or
// wrap, an *Error pass through unchanged; context and network errors receive
// the matching category; anything else is an internal, non-retryable error.
func Classify(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return Wrap(Timeout, "operation timed out", err)
	case errors.Is(err, context.Canceled):
		return Wrap(Internal, "operation cancelled", err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return Wrap(Timeout, "network timeout", err)
		}
		return Wrap(Network, "network error", err)
	}
	return Wrap(Internal, "unexpected error", err)
}

// IsRetryable reports whether err may succeed if the operation is repeated.
func IsRetryable(err error) bool {
	e := Classify(err)
	return e != nil && e.Retryable
}

// CategoryOf returns the category of err, or "" for nil.
func CategoryOf(err error) Category {
	e := Classify(err)
	if e == nil {
		return ""
	}
	return e.Category
}
