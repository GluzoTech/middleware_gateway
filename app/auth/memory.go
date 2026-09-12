package auth

import (
	"context"
	"sync"
	"time"
)

// MemoryStore is an in-memory verifier for tests. It applies exactly the
// same status, expiry and revocation rules as the PostgreSQL store.
type MemoryStore struct {
	mu        sync.RWMutex
	platforms map[string]Platform    // keyed by API key hash
	tokens    map[string]memoryToken // keyed by token hash

	// Now supplies the current time; tests override it to exercise expiry.
	Now func() time.Time
}

type memoryToken struct {
	token       Token
	integration Integration
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		platforms: make(map[string]Platform),
		tokens:    make(map[string]memoryToken),
		Now:       time.Now,
	}
}

// AddPlatform registers p, authenticated by apiKey.
func (m *MemoryStore) AddPlatform(p Platform, apiKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.platforms[HashSecret(apiKey)] = p
}

// AddToken registers t for integration, authenticated by plaintext.
func (m *MemoryStore) AddToken(t Token, integration Integration, plaintext string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[HashSecret(plaintext)] = memoryToken{token: t, integration: integration}
}

// VerifyPlatformKey implements PlatformKeyVerifier.
func (m *MemoryStore) VerifyPlatformKey(_ context.Context, key string) (*Platform, error) {
	if key == "" {
		return nil, ErrInvalidCredentials
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	want := HashSecret(key)
	for hash, p := range m.platforms {
		if hashesEqual(hash, want) {
			platform := p
			if err := checkPlatform(&platform); err != nil {
				return nil, err
			}
			return &platform, nil
		}
	}
	return nil, ErrInvalidCredentials
}

// VerifyAccessToken implements AccessTokenVerifier.
func (m *MemoryStore) VerifyAccessToken(_ context.Context, token string) (*Principal, error) {
	if token == "" {
		return nil, ErrInvalidCredentials
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	want := HashSecret(token)
	for hash, entry := range m.tokens {
		if hashesEqual(hash, want) {
			if err := checkToken(entry.token, entry.integration, m.Now()); err != nil {
				return nil, err
			}
			return &Principal{Integration: entry.integration, TokenID: entry.token.ID}, nil
		}
	}
	return nil, ErrInvalidCredentials
}
