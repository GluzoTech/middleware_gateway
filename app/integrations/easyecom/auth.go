package easyecom

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/httpclient"
	dtoauth "github.com/gluzo/integration-gateway/app/integrations/easyecom/dto/auth"
)

// accessTokenPath is the login endpoint.
// VERIFY against api-docs.easyecom.io (Authentication).
const accessTokenPath = "/access/token"

// TokenSource supplies the JWT for outbound calls.
type TokenSource interface {
	// Token returns a JWT believed to be valid.
	Token(ctx context.Context) (string, error)
	// Invalidate discards the cached JWT so the next Token call obtains a
	// fresh one. Called after the API rejects a token with 401.
	Invalidate()
}

// staticTokenSource serves a pre-issued JWT. It cannot recover from a 401;
// operators rotate the token through configuration.
type staticTokenSource struct {
	token string
}

func (s staticTokenSource) Token(context.Context) (string, error) { return s.token, nil }
func (s staticTokenSource) Invalidate()                           {}

// loginTokenSource logs in with account credentials and caches the JWT until
// shortly before it expires. Concurrent callers share one login.
type loginTokenSource struct {
	http   *httpclient.Client
	apiKey string
	creds  dtoauth.AccessTokenRequest
	now    func() time.Time

	// cacheTTL bounds how long a token is reused even when its exp claim
	// allows longer; a daily login is cheap insurance against revocation.
	cacheTTL time.Duration
	// refreshMargin is subtracted from the exp claim so a token is never
	// used in its final moments.
	refreshMargin time.Duration

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

func newLoginTokenSource(client *httpclient.Client, cfg Config) *loginTokenSource {
	return &loginTokenSource{
		http:   client,
		apiKey: cfg.APIKey,
		creds: dtoauth.AccessTokenRequest{
			Email:       cfg.Email,
			Password:    cfg.Password,
			LocationKey: cfg.LocationKey,
		},
		now:           time.Now,
		cacheTTL:      24 * time.Hour,
		refreshMargin: 5 * time.Minute,
	}
}

// Token implements TokenSource.
func (l *loginTokenSource) Token(ctx context.Context) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.token != "" && l.now().Before(l.expiresAt) {
		return l.token, nil
	}

	var resp dtoauth.AccessTokenResponse
	_, err := l.http.DoJSON(ctx, httpclient.Request{
		Method:    http.MethodPost,
		Path:      accessTokenPath,
		Header:    http.Header{"X-API-Key": {l.apiKey}},
		Body:      l.creds,
		Operation: "AccessToken",
	}, &resp)
	if err != nil {
		return "", err
	}
	if err := checkEnvelope("AccessToken", resp.Code, resp.Message); err != nil {
		return "", err
	}
	if strings.TrimSpace(resp.Data.Token.JWT) == "" {
		e := apperror.New(apperror.Authentication, "login response carried no jwt_token")
		e.Integration, e.Operation = PlatformName, "AccessToken"
		return "", e
	}

	l.token = resp.Data.Token.JWT
	l.expiresAt = l.now().Add(l.cacheTTL)
	if exp, ok := jwtExpiry(l.token); ok && exp.Add(-l.refreshMargin).Before(l.expiresAt) {
		l.expiresAt = exp.Add(-l.refreshMargin)
	}
	return l.token, nil
}

// Invalidate implements TokenSource.
func (l *loginTokenSource) Invalidate() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.token, l.expiresAt = "", time.Time{}
}

// jwtExpiry reads the exp claim of a JWT without verifying the signature.
// The gateway does not trust the claim for security, only to avoid sending a
// token the issuer will reject.
func jwtExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0), true
}
