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
//
// Every method takes a location key because EasyEcom's JWT is scoped to one
// location: a token obtained for BCPL's location cannot write quantities into
// Gluzo's own, whatever the request body says. That is the guarantee the
// stock sink rests on, so the location cannot be a property of the client —
// one process writes to several locations and must never confuse them.
//
// An empty location means the process default from configuration.
type TokenSource interface {
	// Token returns a JWT believed to be valid for locationKey.
	Token(ctx context.Context, locationKey string) (string, error)
	// Invalidate discards the cached JWT for locationKey so the next Token
	// call obtains a fresh one. Called after the API rejects a token with
	// 401.
	Invalidate(locationKey string)
}

// staticTokenSource serves a pre-issued JWT. It cannot recover from a 401;
// operators rotate the token through configuration.
type staticTokenSource struct {
	token string
}

// Token implements TokenSource. The token's scope is whatever it was issued
// for; a static token cannot be obtained per location, so a deployment that
// writes to more than one location must log in rather than supply one.
func (s staticTokenSource) Token(context.Context, string) (string, error) { return s.token, nil }
func (s staticTokenSource) Invalidate(string)                             {}

// loginTokenSource logs in with account credentials and caches the JWT until
// shortly before it expires. Concurrent callers share one login.
type loginTokenSource struct {
	http            *httpclient.Client
	apiKey          string
	email           string
	password        string
	defaultLocation string
	now             func() time.Time

	// cacheTTL bounds how long a token is reused even when its exp claim
	// allows longer; a daily login is cheap insurance against revocation.
	cacheTTL time.Duration
	// refreshMargin is subtracted from the exp claim so a token is never
	// used in its final moments.
	refreshMargin time.Duration

	// One entry per location. Keyed rather than single because a token is
	// scoped to a location: caching one token for the process would hand
	// BCPL's token to a write meant for Gluzo's warehouse, which is the
	// precise failure the per-location design exists to prevent.
	mu     sync.Mutex
	tokens map[string]cachedToken
}

type cachedToken struct {
	token     string
	expiresAt time.Time
}

func newLoginTokenSource(client *httpclient.Client, cfg Config) *loginTokenSource {
	return &loginTokenSource{
		http:            client,
		apiKey:          cfg.APIKey,
		email:           cfg.Email,
		password:        cfg.Password,
		defaultLocation: cfg.LocationKey,
		now:             time.Now,
		cacheTTL:        24 * time.Hour,
		refreshMargin:   5 * time.Minute,
		tokens:          make(map[string]cachedToken),
	}
}

// resolve maps an empty location key to the configured default.
func (l *loginTokenSource) resolve(locationKey string) string {
	if v := strings.TrimSpace(locationKey); v != "" {
		return v
	}
	return l.defaultLocation
}

// Token implements TokenSource.
func (l *loginTokenSource) Token(ctx context.Context, locationKey string) (string, error) {
	location := l.resolve(locationKey)

	l.mu.Lock()
	defer l.mu.Unlock()

	if c, ok := l.tokens[location]; ok && c.token != "" && l.now().Before(c.expiresAt) {
		return c.token, nil
	}

	creds := dtoauth.AccessTokenRequest{Email: l.email, Password: l.password, LocationKey: location}
	var resp dtoauth.AccessTokenResponse
	_, err := l.http.DoJSON(ctx, httpclient.Request{
		Method:    http.MethodPost,
		Path:      accessTokenPath,
		Header:    http.Header{"X-API-Key": {l.apiKey}},
		Body:      creds,
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

	entry := cachedToken{token: resp.Data.Token.JWT, expiresAt: l.now().Add(l.cacheTTL)}
	if exp, ok := jwtExpiry(entry.token); ok && exp.Add(-l.refreshMargin).Before(entry.expiresAt) {
		entry.expiresAt = exp.Add(-l.refreshMargin)
	}
	l.tokens[location] = entry
	return entry.token, nil
}

// Invalidate implements TokenSource. Only the named location's token is
// discarded: a 401 for one location says nothing about another's.
func (l *loginTokenSource) Invalidate(locationKey string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.tokens, l.resolve(locationKey))
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
