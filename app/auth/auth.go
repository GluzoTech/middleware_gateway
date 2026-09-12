// Package auth implements the gateway's two-tier authentication.
//
// Tier 1, the platform API key (X-API-Key header), proves that a request
// comes from a known source platform such as EasyEcom. Tier 2, the
// integration access token (Authorization: Bearer), proves that the caller
// may submit events for one configured integration. The tiers are verified
// independently; a request must pass both, and the token's integration must
// belong to the platform that presented the key.
//
// Only SHA-256 digests of keys and tokens are stored. Plaintext secrets are
// shown once at issue time and never logged.
package auth

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Entity statuses.
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// Platform types describe the direction of data relative to the gateway.
const (
	PlatformTypeSource      = "source"      // sends webhooks to the gateway
	PlatformTypeDestination = "destination" // receives data from the gateway
	PlatformTypeBoth        = "both"
)

// Sentinel errors. Handlers map these to HTTP statuses; anything else is an
// infrastructure failure.
var (
	ErrInvalidCredentials  = errors.New("invalid credentials")
	ErrPlatformDisabled    = errors.New("platform is disabled")
	ErrIntegrationDisabled = errors.New("integration is disabled")
	ErrTokenExpired        = errors.New("access token has expired")
	ErrTokenRevoked        = errors.New("access token has been revoked")
	ErrNotFound            = errors.New("not found")
	ErrAlreadyExists       = errors.New("already exists")
	ErrInvalidArgument     = errors.New("invalid argument")
)

// Platform is an external system known to the gateway.
type Platform struct {
	ID        uuid.UUID
	Name      string
	Type      string
	Status    string
	CreatedAt time.Time
}

// Integration is one configured source-to-destination pipeline.
type Integration struct {
	ID                    uuid.UUID
	Name                  string
	SourcePlatformID      uuid.UUID
	DestinationPlatformID uuid.UUID
	Status                string
	CreatedAt             time.Time
}

// Token is the metadata of an integration access token. The secret itself
// is never part of this struct.
type Token struct {
	ID            uuid.UUID
	IntegrationID uuid.UUID
	Name          string
	ExpiresAt     *time.Time
	RevokedAt     *time.Time
	LastUsedAt    *time.Time
	CreatedAt     time.Time
}

// Principal is the identity established by a valid access token.
type Principal struct {
	Integration Integration
	TokenID     uuid.UUID
}

// PlatformKeyVerifier validates tier-1 credentials.
type PlatformKeyVerifier interface {
	VerifyPlatformKey(ctx context.Context, key string) (*Platform, error)
}

// AccessTokenVerifier validates tier-2 credentials.
type AccessTokenVerifier interface {
	VerifyAccessToken(ctx context.Context, token string) (*Principal, error)
}

type platformContextKey struct{}
type principalContextKey struct{}

// WithPlatform stores the authenticated platform on ctx.
func WithPlatform(ctx context.Context, p *Platform) context.Context {
	return context.WithValue(ctx, platformContextKey{}, p)
}

// PlatformFromContext returns the platform authenticated by tier 1.
func PlatformFromContext(ctx context.Context) (*Platform, bool) {
	p, ok := ctx.Value(platformContextKey{}).(*Platform)
	return p, ok && p != nil
}

// WithPrincipal stores the authenticated principal on ctx.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, p)
}

// PrincipalFromContext returns the principal authenticated by tier 2.
func PrincipalFromContext(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(principalContextKey{}).(*Principal)
	return p, ok && p != nil
}

// checkPlatform applies the status rules shared by every verifier.
func checkPlatform(p *Platform) error {
	if p.Status != StatusActive {
		return ErrPlatformDisabled
	}
	return nil
}

// checkToken applies the expiry, revocation and status rules shared by every
// verifier. Revocation is checked before expiry so that a revoked token is
// always reported as revoked.
func checkToken(t Token, integration Integration, now time.Time) error {
	if t.RevokedAt != nil {
		return ErrTokenRevoked
	}
	if t.ExpiresAt != nil && !now.Before(*t.ExpiresAt) {
		return ErrTokenExpired
	}
	if integration.Status != StatusActive {
		return ErrIntegrationDisabled
	}
	return nil
}
