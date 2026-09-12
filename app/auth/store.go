package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the PostgreSQL-backed credential store. It serves both request
// verification and the operator commands that provision credentials.
type Store struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// NewStore wraps pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, now: time.Now}
}

// VerifyPlatformKey implements PlatformKeyVerifier.
func (s *Store) VerifyPlatformKey(ctx context.Context, key string) (*Platform, error) {
	if key == "" {
		return nil, ErrInvalidCredentials
	}
	var p Platform
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, type, status, created_at FROM platforms WHERE api_key_hash = $1`,
		HashSecret(key),
	).Scan(&p.ID, &p.Name, &p.Type, &p.Status, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, fmt.Errorf("auth: verify platform key: %w", err)
	}
	if err := checkPlatform(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

// VerifyAccessToken implements AccessTokenVerifier.
func (s *Store) VerifyAccessToken(ctx context.Context, token string) (*Principal, error) {
	if token == "" {
		return nil, ErrInvalidCredentials
	}
	var t Token
	var integration Integration
	err := s.pool.QueryRow(ctx, `
		SELECT t.id, t.integration_id, t.name, t.expires_at, t.revoked_at, t.last_used_at, t.created_at,
		       i.id, i.name, i.source_platform_id, i.destination_platform_id, i.status, i.created_at
		FROM integration_access_tokens t
		JOIN integrations i ON i.id = t.integration_id
		WHERE t.token_hash = $1`,
		HashSecret(token),
	).Scan(
		&t.ID, &t.IntegrationID, &t.Name, &t.ExpiresAt, &t.RevokedAt, &t.LastUsedAt, &t.CreatedAt,
		&integration.ID, &integration.Name, &integration.SourcePlatformID, &integration.DestinationPlatformID,
		&integration.Status, &integration.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, fmt.Errorf("auth: verify access token: %w", err)
	}
	if err := checkToken(t, integration, s.now()); err != nil {
		return nil, err
	}

	// Best-effort usage stamp, throttled to one write per minute per token so
	// a busy webhook does not turn every request into an UPDATE.
	_, _ = s.pool.Exec(ctx, `
		UPDATE integration_access_tokens
		SET last_used_at = now()
		WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute')`,
		t.ID)

	return &Principal{Integration: integration, TokenID: t.ID}, nil
}

// CreatePlatform registers a platform. For platforms that send webhooks
// (type source or both) an API key is generated and returned exactly once;
// destination-only platforms get no key.
func (s *Store) CreatePlatform(ctx context.Context, name, platformType string) (Platform, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Platform{}, "", fmt.Errorf("%w: platform name is required", ErrInvalidArgument)
	}
	switch platformType {
	case PlatformTypeSource, PlatformTypeDestination, PlatformTypeBoth:
	default:
		return Platform{}, "", fmt.Errorf("%w: platform type must be source, destination or both", ErrInvalidArgument)
	}

	p := Platform{ID: uuid.New(), Name: name, Type: platformType, Status: StatusActive}
	var key string
	var keyHash *string
	if platformType != PlatformTypeDestination {
		var err error
		key, err = GenerateSecret(PlatformKeyPrefix)
		if err != nil {
			return Platform{}, "", err
		}
		h := HashSecret(key)
		keyHash = &h
	}

	err := s.pool.QueryRow(ctx,
		`INSERT INTO platforms (id, name, type, api_key_hash) VALUES ($1, $2, $3, $4) RETURNING created_at`,
		p.ID, p.Name, p.Type, keyHash,
	).Scan(&p.CreatedAt)
	if isUniqueViolation(err) {
		return Platform{}, "", fmt.Errorf("%w: platform %q", ErrAlreadyExists, name)
	}
	if err != nil {
		return Platform{}, "", fmt.Errorf("auth: create platform: %w", err)
	}
	return p, key, nil
}

// RotatePlatformKey replaces the platform's API key and returns the new key
// exactly once. The previous key stops working immediately.
func (s *Store) RotatePlatformKey(ctx context.Context, name string) (string, error) {
	key, err := GenerateSecret(PlatformKeyPrefix)
	if err != nil {
		return "", err
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE platforms SET api_key_hash = $1, updated_at = now() WHERE name = $2`,
		HashSecret(key), name)
	if err != nil {
		return "", fmt.Errorf("auth: rotate platform key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return "", fmt.Errorf("%w: platform %q", ErrNotFound, name)
	}
	return key, nil
}

// SetPlatformStatus enables or disables a platform.
func (s *Store) SetPlatformStatus(ctx context.Context, name, status string) error {
	if status != StatusActive && status != StatusDisabled {
		return fmt.Errorf("%w: status must be active or disabled", ErrInvalidArgument)
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE platforms SET status = $1, updated_at = now() WHERE name = $2`, status, name)
	if err != nil {
		return fmt.Errorf("auth: set platform status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: platform %q", ErrNotFound, name)
	}
	return nil
}

// ListPlatforms returns every platform ordered by name.
func (s *Store) ListPlatforms(ctx context.Context) ([]Platform, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, type, status, created_at FROM platforms ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("auth: list platforms: %w", err)
	}
	defer rows.Close()

	var out []Platform
	for rows.Next() {
		var p Platform
		if err := rows.Scan(&p.ID, &p.Name, &p.Type, &p.Status, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("auth: scan platform: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth: list platforms: %w", err)
	}
	return out, nil
}

// CreateIntegration registers a pipeline between two platforms identified by
// name.
func (s *Store) CreateIntegration(ctx context.Context, name, sourcePlatform, destinationPlatform string) (Integration, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Integration{}, fmt.Errorf("%w: integration name is required", ErrInvalidArgument)
	}
	integration := Integration{ID: uuid.New(), Name: name}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO integrations (id, name, source_platform_id, destination_platform_id)
		SELECT $1, $2, src.id, dst.id
		FROM platforms src, platforms dst
		WHERE src.name = $3 AND dst.name = $4
		RETURNING source_platform_id, destination_platform_id, status, created_at`,
		integration.ID, integration.Name, sourcePlatform, destinationPlatform,
	).Scan(&integration.SourcePlatformID, &integration.DestinationPlatformID, &integration.Status, &integration.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Integration{}, fmt.Errorf("%w: platform %q or %q", ErrNotFound, sourcePlatform, destinationPlatform)
	}
	if isUniqueViolation(err) {
		return Integration{}, fmt.Errorf("%w: integration %q", ErrAlreadyExists, name)
	}
	if err != nil {
		return Integration{}, fmt.Errorf("auth: create integration: %w", err)
	}
	return integration, nil
}

// SetIntegrationStatus enables or disables an integration.
func (s *Store) SetIntegrationStatus(ctx context.Context, name, status string) error {
	if status != StatusActive && status != StatusDisabled {
		return fmt.Errorf("%w: status must be active or disabled", ErrInvalidArgument)
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE integrations SET status = $1, updated_at = now() WHERE name = $2`, status, name)
	if err != nil {
		return fmt.Errorf("auth: set integration status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: integration %q", ErrNotFound, name)
	}
	return nil
}

// ListIntegrations returns every integration ordered by name.
func (s *Store) ListIntegrations(ctx context.Context) ([]Integration, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, source_platform_id, destination_platform_id, status, created_at
		FROM integrations ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("auth: list integrations: %w", err)
	}
	defer rows.Close()

	var out []Integration
	for rows.Next() {
		var i Integration
		if err := rows.Scan(&i.ID, &i.Name, &i.SourcePlatformID, &i.DestinationPlatformID, &i.Status, &i.CreatedAt); err != nil {
			return nil, fmt.Errorf("auth: scan integration: %w", err)
		}
		out = append(out, i)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth: list integrations: %w", err)
	}
	return out, nil
}

// IssueToken creates an access token for the named integration and returns
// the plaintext exactly once. A zero ttl means the token never expires.
func (s *Store) IssueToken(ctx context.Context, integrationName, tokenName string, ttl time.Duration) (Token, string, error) {
	tokenName = strings.TrimSpace(tokenName)
	if tokenName == "" {
		return Token{}, "", fmt.Errorf("%w: token name is required", ErrInvalidArgument)
	}
	if ttl < 0 {
		return Token{}, "", fmt.Errorf("%w: ttl must not be negative", ErrInvalidArgument)
	}
	plaintext, err := GenerateSecret(AccessTokenPrefix)
	if err != nil {
		return Token{}, "", err
	}

	t := Token{ID: uuid.New(), Name: tokenName}
	if ttl > 0 {
		expires := s.now().Add(ttl)
		t.ExpiresAt = &expires
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO integration_access_tokens (id, integration_id, name, token_hash, expires_at)
		SELECT $1, i.id, $2, $3, $4 FROM integrations i WHERE i.name = $5
		RETURNING integration_id, created_at`,
		t.ID, t.Name, HashSecret(plaintext), t.ExpiresAt, integrationName,
	).Scan(&t.IntegrationID, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Token{}, "", fmt.Errorf("%w: integration %q", ErrNotFound, integrationName)
	}
	if err != nil {
		return Token{}, "", fmt.Errorf("auth: issue token: %w", err)
	}
	return t, plaintext, nil
}

// RevokeToken permanently disables a token. Revoking an already revoked
// token is a no-op.
func (s *Store) RevokeToken(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE integration_access_tokens SET revoked_at = COALESCE(revoked_at, now()) WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("auth: revoke token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: token %s", ErrNotFound, id)
	}
	return nil
}

// ListTokens returns the tokens of the named integration, newest first.
func (s *Store) ListTokens(ctx context.Context, integrationName string) ([]Token, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.id, t.integration_id, t.name, t.expires_at, t.revoked_at, t.last_used_at, t.created_at
		FROM integration_access_tokens t
		JOIN integrations i ON i.id = t.integration_id
		WHERE i.name = $1
		ORDER BY t.created_at DESC`, integrationName)
	if err != nil {
		return nil, fmt.Errorf("auth: list tokens: %w", err)
	}
	defer rows.Close()

	var out []Token
	for rows.Next() {
		var t Token
		if err := rows.Scan(&t.ID, &t.IntegrationID, &t.Name, &t.ExpiresAt, &t.RevokedAt, &t.LastUsedAt, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("auth: scan token: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth: list tokens: %w", err)
	}
	return out, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
