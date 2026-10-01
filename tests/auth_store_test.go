package tests

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gluzo/integration-gateway/app/auth"
)

func TestAuthStorePlatforms(t *testing.T) {
	ctx := context.Background()
	store := auth.NewStore(pool)
	suffix := uuid.NewString()[:8]

	src, key, err := store.CreatePlatform(ctx, "easyecom-"+suffix, auth.PlatformTypeSource)
	if err != nil {
		t.Fatalf("CreatePlatform: %v", err)
	}
	if key == "" {
		t.Fatal("source platform received no API key")
	}
	dst, dstKey, err := store.CreatePlatform(ctx, "vinculum-"+suffix, auth.PlatformTypeDestination)
	if err != nil {
		t.Fatalf("CreatePlatform destination: %v", err)
	}
	if dstKey != "" {
		t.Fatal("destination platform must not receive an API key")
	}

	p, err := store.VerifyPlatformKey(ctx, key)
	if err != nil || p.ID != src.ID || p.Name != src.Name {
		t.Fatalf("VerifyPlatformKey: platform %+v err %v", p, err)
	}
	if _, err := store.VerifyPlatformKey(ctx, auth.PlatformKeyPrefix+"wrong"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("wrong key: err = %v", err)
	}

	if _, _, err := store.CreatePlatform(ctx, src.Name, auth.PlatformTypeSource); !errors.Is(err, auth.ErrAlreadyExists) {
		t.Fatalf("duplicate platform: err = %v", err)
	}
	if _, _, err := store.CreatePlatform(ctx, "bad-"+suffix, "sideways"); !errors.Is(err, auth.ErrInvalidArgument) {
		t.Fatalf("bad type: err = %v", err)
	}

	rotated, err := store.RotatePlatformKey(ctx, src.Name)
	if err != nil {
		t.Fatalf("RotatePlatformKey: %v", err)
	}
	if _, err := store.VerifyPlatformKey(ctx, key); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("old key still accepted after rotation: err = %v", err)
	}
	if _, err := store.VerifyPlatformKey(ctx, rotated); err != nil {
		t.Fatalf("rotated key rejected: %v", err)
	}
	if _, err := store.RotatePlatformKey(ctx, "missing-"+suffix); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("rotate missing: err = %v", err)
	}

	if err := store.SetPlatformStatus(ctx, src.Name, auth.StatusDisabled); err != nil {
		t.Fatalf("SetPlatformStatus: %v", err)
	}
	if _, err := store.VerifyPlatformKey(ctx, rotated); !errors.Is(err, auth.ErrPlatformDisabled) {
		t.Fatalf("disabled platform: err = %v", err)
	}
	if err := store.SetPlatformStatus(ctx, src.Name, "paused"); !errors.Is(err, auth.ErrInvalidArgument) {
		t.Fatalf("bad status: err = %v", err)
	}

	platforms, err := store.ListPlatforms(ctx)
	if err != nil {
		t.Fatalf("ListPlatforms: %v", err)
	}
	var found int
	for _, p := range platforms {
		if p.ID == src.ID || p.ID == dst.ID {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("ListPlatforms returned %d of the 2 created platforms", found)
	}
}

func TestAuthStoreIntegrationsAndTokens(t *testing.T) {
	ctx := context.Background()
	store := auth.NewStore(pool)
	suffix := uuid.NewString()[:8]

	src, _, err := store.CreatePlatform(ctx, "easyecom-"+suffix, auth.PlatformTypeSource)
	if err != nil {
		t.Fatalf("CreatePlatform: %v", err)
	}
	dst, _, err := store.CreatePlatform(ctx, "vinculum-"+suffix, auth.PlatformTypeDestination)
	if err != nil {
		t.Fatalf("CreatePlatform: %v", err)
	}

	integ, err := store.CreateIntegration(ctx, "easyecom-vinculum-"+suffix, src.Name, dst.Name)
	if err != nil {
		t.Fatalf("CreateIntegration: %v", err)
	}
	if integ.SourcePlatformID != src.ID || integ.DestinationPlatformID != dst.ID || integ.Status != auth.StatusActive {
		t.Fatalf("unexpected integration: %+v", integ)
	}
	if _, err := store.CreateIntegration(ctx, "x-"+suffix, "missing-"+suffix, dst.Name); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("missing platform: err = %v", err)
	}
	if _, err := store.CreateIntegration(ctx, integ.Name, src.Name, dst.Name); !errors.Is(err, auth.ErrAlreadyExists) {
		t.Fatalf("duplicate integration: err = %v", err)
	}

	tok, plaintext, err := store.IssueToken(ctx, integ.Name, "easyecom webhook", 0)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if plaintext == "" || tok.ExpiresAt != nil || tok.IntegrationID != integ.ID {
		t.Fatalf("unexpected token: %+v plaintext %q", tok, plaintext)
	}
	if _, _, err := store.IssueToken(ctx, "missing-"+suffix, "x", 0); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("issue for missing integration: err = %v", err)
	}

	principal, err := store.VerifyAccessToken(ctx, plaintext)
	if err != nil {
		t.Fatalf("VerifyAccessToken: %v", err)
	}
	if principal.Integration.ID != integ.ID || principal.TokenID != tok.ID {
		t.Fatalf("unexpected principal: %+v", principal)
	}
	if _, err := store.VerifyAccessToken(ctx, auth.AccessTokenPrefix+"nope"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("unknown token: err = %v", err)
	}

	tokens, err := store.ListTokens(ctx, integ.Name)
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(tokens) != 1 || tokens[0].ID != tok.ID || tokens[0].LastUsedAt == nil {
		t.Fatalf("ListTokens = %+v, want one used token", tokens)
	}

	_, shortLived, err := store.IssueToken(ctx, integ.Name, "short", 10*time.Millisecond)
	if err != nil {
		t.Fatalf("IssueToken short: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := store.VerifyAccessToken(ctx, shortLived); !errors.Is(err, auth.ErrTokenExpired) {
		t.Fatalf("expired token: err = %v", err)
	}

	if err := store.RevokeToken(ctx, tok.ID); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	if _, err := store.VerifyAccessToken(ctx, plaintext); !errors.Is(err, auth.ErrTokenRevoked) {
		t.Fatalf("revoked token: err = %v", err)
	}
	if err := store.RevokeToken(ctx, tok.ID); err != nil {
		t.Fatalf("second revoke should be a no-op: %v", err)
	}
	if err := store.RevokeToken(ctx, uuid.New()); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("revoke unknown: err = %v", err)
	}

	_, fresh, err := store.IssueToken(ctx, integ.Name, "fresh", 0)
	if err != nil {
		t.Fatalf("IssueToken fresh: %v", err)
	}
	if err := store.SetIntegrationStatus(ctx, integ.Name, auth.StatusDisabled); err != nil {
		t.Fatalf("SetIntegrationStatus: %v", err)
	}
	if _, err := store.VerifyAccessToken(ctx, fresh); !errors.Is(err, auth.ErrIntegrationDisabled) {
		t.Fatalf("disabled integration: err = %v", err)
	}

	integrations, err := store.ListIntegrations(ctx)
	if err != nil {
		t.Fatalf("ListIntegrations: %v", err)
	}
	var seen bool
	for _, i := range integrations {
		if i.ID == integ.ID && i.Status == auth.StatusDisabled {
			seen = true
		}
	}
	if !seen {
		t.Fatal("ListIntegrations did not return the disabled integration")
	}
}
