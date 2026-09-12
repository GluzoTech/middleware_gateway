package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// Secret prefixes make a leaked credential recognisable in code scanners and
// let operators tell the two tiers apart at a glance.
const (
	PlatformKeyPrefix = "gluzo_pk_"
	AccessTokenPrefix = "gluzo_at_"

	secretBytes = 32 // 256 bits of entropy
)

// GenerateSecret returns a new random credential carrying prefix.
func GenerateSecret(prefix string) (string, error) {
	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: generate secret: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashSecret returns the hex-encoded SHA-256 digest stored in place of a
// secret. Secrets carry 256 bits of entropy, so a fast hash is appropriate;
// slow password hashes exist to protect low-entropy human passwords.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// hashesEqual compares two digests in constant time.
func hashesEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
