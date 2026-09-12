package apperror_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/gluzo/integration-gateway/app/apperror"
)

func TestFromHTTPStatus(t *testing.T) {
	tests := []struct {
		status    int
		category  apperror.Category
		retryable bool
	}{
		{400, apperror.Validation, false},
		{401, apperror.Authentication, false},
		{403, apperror.Authorization, false},
		{404, apperror.ExternalAPI, false},
		{408, apperror.Timeout, true},
		{409, apperror.ExternalAPI, false},
		{422, apperror.Validation, false},
		{429, apperror.RateLimit, true},
		{500, apperror.ExternalAPI, true},
		{502, apperror.ExternalAPI, true},
		{503, apperror.ExternalAPI, true},
		{504, apperror.ExternalAPI, true},
		{599, apperror.ExternalAPI, true},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.status), func(t *testing.T) {
			cat, retry := apperror.FromHTTPStatus(tt.status)
			if cat != tt.category || retry != tt.retryable {
				t.Fatalf("FromHTTPStatus(%d) = %s/%v, want %s/%v", tt.status, cat, retry, tt.category, tt.retryable)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	structured := apperror.New(apperror.Mapping, "bad sku")
	tests := []struct {
		name      string
		err       error
		category  apperror.Category
		retryable bool
	}{
		{"nil", nil, "", false},
		{"structured passthrough", structured, apperror.Mapping, false},
		{"structured wrapped with %w", fmt.Errorf("action: %w", structured), apperror.Mapping, false},
		{"deadline exceeded", context.DeadlineExceeded, apperror.Timeout, true},
		{"wrapped deadline", fmt.Errorf("call: %w", context.DeadlineExceeded), apperror.Timeout, true},
		{"cancelled", context.Canceled, apperror.Internal, false},
		{"net timeout", &net.DNSError{Err: "lookup", IsTimeout: true}, apperror.Timeout, true},
		{"net error", &net.OpError{Op: "dial", Err: errors.New("refused")}, apperror.Network, true},
		{"plain error", errors.New("boom"), apperror.Internal, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := apperror.Classify(tt.err)
			if tt.err == nil {
				if e != nil {
					t.Fatalf("Classify(nil) = %v", e)
				}
				if apperror.CategoryOf(nil) != "" || apperror.IsRetryable(nil) {
					t.Fatal("nil error must have no category and not be retryable")
				}
				return
			}
			if e.Category != tt.category || e.Retryable != tt.retryable {
				t.Fatalf("Classify = %s/%v, want %s/%v", e.Category, e.Retryable, tt.category, tt.retryable)
			}
			if apperror.CategoryOf(tt.err) != tt.category || apperror.IsRetryable(tt.err) != tt.retryable {
				t.Fatal("CategoryOf/IsRetryable disagree with Classify")
			}
			// Either the classified error wraps the original, or (for
			// pass-through) the original wraps the classified error.
			if !errors.Is(e, tt.err) && !errors.Is(tt.err, e) {
				t.Fatalf("classified error is unrelated to the original: %v", e)
			}
		})
	}
}

func TestErrorFormatting(t *testing.T) {
	e := &apperror.Error{
		Category:        apperror.ExternalAPI,
		Message:         "unexpected status",
		Integration:     "dabur",
		Operation:       "CreateSaleOrder",
		HTTPStatus:      502,
		ExternalCode:    "E42",
		ExternalMessage: "upstream unavailable",
		Err:             errors.New("cause"),
	}
	got := e.Error()
	for _, want := range []string{"external_api_error", "unexpected status", "[dabur CreateSaleOrder]", "http 502", "code E42", "upstream unavailable", "cause"} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() = %q, missing %q", got, want)
		}
	}
	if !errors.Is(e, e.Err) {
		t.Fatal("Unwrap does not expose the cause")
	}
}

func TestDefaultRetryability(t *testing.T) {
	tests := map[apperror.Category]bool{
		apperror.Network:        true,
		apperror.Timeout:        true,
		apperror.RateLimit:      true,
		apperror.Validation:     false,
		apperror.Authentication: false,
		apperror.Authorization:  false,
		apperror.ExternalAPI:    false,
		apperror.Mapping:        false,
		apperror.Workflow:       false,
		apperror.NonRetryable:   false,
		apperror.Internal:       false,
	}
	for cat, want := range tests {
		if got := apperror.New(cat, "x").Retryable; got != want {
			t.Errorf("New(%s).Retryable = %v, want %v", cat, got, want)
		}
	}
}
