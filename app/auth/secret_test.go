package auth

import (
	"regexp"
	"strings"
	"testing"
)

func TestGenerateSecret(t *testing.T) {
	a, err := GenerateSecret(AccessTokenPrefix)
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	b, err := GenerateSecret(AccessTokenPrefix)
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	if a == b {
		t.Fatal("two generated secrets are identical")
	}
	if !strings.HasPrefix(a, AccessTokenPrefix) {
		t.Fatalf("secret %q lacks prefix", a)
	}
	// 32 bytes in unpadded base64url is 43 characters.
	if got := len(strings.TrimPrefix(a, AccessTokenPrefix)); got != 43 {
		t.Fatalf("secret body length = %d, want 43", got)
	}
	if strings.ContainsAny(a, "+/=") {
		t.Fatalf("secret %q is not URL-safe", a)
	}
}

func TestHashSecret(t *testing.T) {
	h1 := HashSecret("gluzo_at_example")
	h2 := HashSecret("gluzo_at_example")
	h3 := HashSecret("gluzo_at_other")
	if h1 != h2 {
		t.Fatal("hash is not deterministic")
	}
	if h1 == h3 {
		t.Fatal("different secrets produced the same hash")
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(h1) {
		t.Fatalf("hash %q is not 64 hex characters", h1)
	}
	if !hashesEqual(h1, h2) || hashesEqual(h1, h3) {
		t.Fatal("hashesEqual disagrees with string equality")
	}
}
