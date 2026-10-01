package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/gluzo/integration-gateway/app/auth"
	"github.com/gluzo/integration-gateway/app/httpserver/middleware"
)

const (
	goodKey         = "gluzo_pk_good"
	disabledKey     = "gluzo_pk_disabled"
	goodToken       = "gluzo_at_good"
	expiredToken    = "gluzo_at_expired"
	revokedToken    = "gluzo_at_revoked"
	disabledToken   = "gluzo_at_disabled_integration"
	foreignToken    = "gluzo_at_other_platform"
	neverSeenSecret = "gluzo_at_never_issued"
)

type fixture struct {
	store    *auth.MemoryStore
	platform auth.Platform
	integ    auth.Integration
	tokenID  uuid.UUID
}

func newFixture() fixture {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	store := auth.NewMemoryStore()
	store.Now = func() time.Time { return now }

	platform := auth.Platform{ID: uuid.New(), Name: "easyecom", Type: auth.PlatformTypeSource, Status: auth.StatusActive}
	otherPlatform := auth.Platform{ID: uuid.New(), Name: "shopify", Type: auth.PlatformTypeSource, Status: auth.StatusActive}
	disabledPlatform := auth.Platform{ID: uuid.New(), Name: "legacy", Type: auth.PlatformTypeSource, Status: auth.StatusDisabled}
	store.AddPlatform(platform, goodKey)
	store.AddPlatform(disabledPlatform, disabledKey)

	integ := auth.Integration{ID: uuid.New(), Name: "easyecom-vinculum", SourcePlatformID: platform.ID, Status: auth.StatusActive}
	disabledInteg := auth.Integration{ID: uuid.New(), Name: "paused", SourcePlatformID: platform.ID, Status: auth.StatusDisabled}
	foreignInteg := auth.Integration{ID: uuid.New(), Name: "shopify-vinculum", SourcePlatformID: otherPlatform.ID, Status: auth.StatusActive}

	tokenID := uuid.New()
	past := now.Add(-time.Minute)
	store.AddToken(auth.Token{ID: tokenID, IntegrationID: integ.ID}, integ, goodToken)
	store.AddToken(auth.Token{ID: uuid.New(), IntegrationID: integ.ID, ExpiresAt: &past}, integ, expiredToken)
	store.AddToken(auth.Token{ID: uuid.New(), IntegrationID: integ.ID, RevokedAt: &past}, integ, revokedToken)
	store.AddToken(auth.Token{ID: uuid.New(), IntegrationID: disabledInteg.ID}, disabledInteg, disabledToken)
	store.AddToken(auth.Token{ID: uuid.New(), IntegrationID: foreignInteg.ID}, foreignInteg, foreignToken)

	return fixture{store: store, platform: platform, integ: integ, tokenID: tokenID}
}

type failingVerifier struct{}

func (failingVerifier) VerifyPlatformKey(context.Context, string) (*auth.Platform, error) {
	return nil, errors.New("connection refused")
}

func (failingVerifier) VerifyAccessToken(context.Context, string) (*auth.Principal, error) {
	return nil, errors.New("connection refused")
}

func newProtectedRouter(platformVerifier auth.PlatformKeyVerifier, tokenVerifier auth.AccessTokenVerifier, logs *bytes.Buffer) (*gin.Engine, *auth.Principal, *auth.Platform) {
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	seenPrincipal := &auth.Principal{}
	seenPlatform := &auth.Platform{}

	r := gin.New()
	r.Use(middleware.Correlation())
	r.POST("/webhook",
		auth.RequirePlatformKey(platformVerifier, logger),
		auth.RequireAccessToken(tokenVerifier, logger),
		func(c *gin.Context) {
			if p, ok := auth.PrincipalFromContext(c.Request.Context()); ok {
				*seenPrincipal = *p
			}
			if p, ok := auth.PlatformFromContext(c.Request.Context()); ok {
				*seenPlatform = *p
			}
			c.Status(http.StatusAccepted)
		})
	return r, seenPrincipal, seenPlatform
}

func TestTwoTierAuthentication(t *testing.T) {
	fx := newFixture()

	tests := []struct {
		name       string
		apiKey     string
		authHeader string
		wantStatus int
		wantReason string
	}{
		{"no credentials", "", "", http.StatusUnauthorized, "missing platform API key"},
		{"unknown platform key", "gluzo_pk_wrong", "Bearer " + goodToken, http.StatusUnauthorized, "invalid credentials"},
		{"disabled platform", disabledKey, "Bearer " + goodToken, http.StatusForbidden, "platform is disabled"},
		{"platform ok but no token", goodKey, "", http.StatusUnauthorized, "missing or malformed bearer token"},
		{"wrong scheme", goodKey, "Basic abc", http.StatusUnauthorized, "missing or malformed bearer token"},
		{"empty bearer", goodKey, "Bearer   ", http.StatusUnauthorized, "missing or malformed bearer token"},
		{"unknown token", goodKey, "Bearer " + neverSeenSecret, http.StatusUnauthorized, "invalid credentials"},
		{"expired token", goodKey, "Bearer " + expiredToken, http.StatusUnauthorized, "access token has expired"},
		{"revoked token", goodKey, "Bearer " + revokedToken, http.StatusUnauthorized, "access token has been revoked"},
		{"disabled integration", goodKey, "Bearer " + disabledToken, http.StatusForbidden, "integration is disabled"},
		{"token bound to another platform", goodKey, "Bearer " + foreignToken, http.StatusForbidden, "integration is not bound to the authenticated platform"},
		{"valid credentials", goodKey, "bearer " + goodToken, http.StatusAccepted, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			r, seenPrincipal, seenPlatform := newProtectedRouter(fx.store, fx.store, &logs)

			req := httptest.NewRequest(http.MethodPost, "/webhook", nil)
			if tt.apiKey != "" {
				req.Header.Set(auth.PlatformKeyHeader, tt.apiKey)
			}
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tt.wantStatus, w.Body.String())
			}
			for _, secret := range []string{goodKey, goodToken, expiredToken, revokedToken, tt.apiKey, tt.authHeader} {
				if secret != "" && strings.Contains(logs.String(), secret) {
					t.Fatalf("credential %q leaked into logs: %s", secret, logs.String())
				}
			}

			if tt.wantStatus == http.StatusAccepted {
				if seenPrincipal.Integration.ID != fx.integ.ID || seenPrincipal.TokenID != fx.tokenID {
					t.Fatalf("handler saw principal %+v", seenPrincipal)
				}
				if seenPlatform.ID != fx.platform.ID {
					t.Fatalf("handler saw platform %+v", seenPlatform)
				}
				return
			}

			var body map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not JSON: %v", err)
			}
			if body["reason"] != tt.wantReason {
				t.Fatalf("reason = %q, want %q", body["reason"], tt.wantReason)
			}
			if body["correlation_id"] == "" {
				t.Fatal("correlation_id missing from rejection")
			}
			if tt.wantStatus == http.StatusUnauthorized && w.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("401 response lacks WWW-Authenticate")
			}
			if !strings.Contains(logs.String(), "authentication failed") {
				t.Fatalf("rejection was not logged: %s", logs.String())
			}
		})
	}
}

func TestVerifierOutageIsNotReportedAsBadCredentials(t *testing.T) {
	var logs bytes.Buffer
	r, _, _ := newProtectedRouter(failingVerifier{}, failingVerifier{}, &logs)

	req := httptest.NewRequest(http.MethodPost, "/webhook", nil)
	req.Header.Set(auth.PlatformKeyHeader, goodKey)
	req.Header.Set("Authorization", "Bearer "+goodToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if !strings.Contains(logs.String(), "authentication unavailable") || !strings.Contains(logs.String(), "connection refused") {
		t.Fatalf("outage not logged with cause: %s", logs.String())
	}
	if strings.Contains(logs.String(), goodKey) || strings.Contains(logs.String(), goodToken) {
		t.Fatalf("credential leaked into logs: %s", logs.String())
	}
}

func TestMemoryStoreRules(t *testing.T) {
	fx := newFixture()
	ctx := context.Background()

	if _, err := fx.store.VerifyPlatformKey(ctx, ""); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("empty key: err = %v", err)
	}
	if _, err := fx.store.VerifyAccessToken(ctx, ""); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("empty token: err = %v", err)
	}
	p, err := fx.store.VerifyPlatformKey(ctx, goodKey)
	if err != nil || p.ID != fx.platform.ID {
		t.Fatalf("good key: platform %+v err %v", p, err)
	}
	pr, err := fx.store.VerifyAccessToken(ctx, goodToken)
	if err != nil || pr.Integration.ID != fx.integ.ID {
		t.Fatalf("good token: principal %+v err %v", pr, err)
	}

	// A token that expires exactly now is expired.
	fx.store.Now = func() time.Time { return time.Date(2026, 9, 12, 11, 59, 0, 0, time.UTC) }
	if _, err := fx.store.VerifyAccessToken(ctx, expiredToken); !errors.Is(err, auth.ErrTokenExpired) {
		t.Fatalf("boundary expiry: err = %v", err)
	}
	fx.store.Now = func() time.Time { return time.Date(2026, 9, 12, 11, 58, 0, 0, time.UTC) }
	if _, err := fx.store.VerifyAccessToken(ctx, expiredToken); err != nil {
		t.Fatalf("not yet expired: err = %v", err)
	}
}
