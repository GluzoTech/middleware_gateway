package correlation_test

import (
	"context"
	"strings"
	"testing"

	"github.com/gluzo/integration-gateway/app/correlation"
)

func TestNewProducesUniquePrefixedIDs(t *testing.T) {
	a, b := correlation.New(), correlation.New()
	if a == b {
		t.Fatal("two generated IDs are identical")
	}
	for _, id := range []string{a, b} {
		if !strings.HasPrefix(id, correlation.Prefix) {
			t.Errorf("%q lacks prefix %q", id, correlation.Prefix)
		}
		if !correlation.IsValid(id) {
			t.Errorf("generated id %q is not valid", id)
		}
	}
}

func TestContextRoundTrip(t *testing.T) {
	if got := correlation.FromContext(context.Background()); got != "" {
		t.Fatalf("empty context returned %q", got)
	}
	ctx := correlation.WithID(context.Background(), "INT-1")
	if got := correlation.FromContext(ctx); got != "INT-1" {
		t.Fatalf("got %q, want INT-1", got)
	}
}

func TestIsValid(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"INT-8a2f", true},
		{"INT-8a2f-4c31_x.y:z", true},
		{"", false},
		{".", false},
		{"..", false},
		{"../etc/passwd", false},
		{"INT 1", false},
		{"INT-1\n", false},
		{"INT-1/2", false},
		{strings.Repeat("a", correlation.MaxLength), true},
		{strings.Repeat("a", correlation.MaxLength+1), false},
	}
	for _, tt := range tests {
		if got := correlation.IsValid(tt.id); got != tt.want {
			t.Errorf("IsValid(%q) = %v, want %v", tt.id, got, tt.want)
		}
	}
}
