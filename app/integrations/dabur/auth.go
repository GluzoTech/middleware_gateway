package dabur

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/httpclient"
	dtoauth "github.com/gluzo/integration-gateway/app/integrations/dabur/dto/auth"
)

// tokenPath is Uniware's OAuth endpoint for both grants
// (documentation.unicommerce.com/docs/oauth.html).
const tokenPath = "/oauth/token"

// refreshMargin is subtracted from expires_in so a token is never used in
// its final moments.
const refreshMargin = 60 * time.Second

// TokenSource supplies the OAuth access token for outbound calls.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
	Invalidate()
}

// oauthTokenSource implements the password grant with refresh-token
// renewal, exactly as Uniware documents it: credentials travel as query
// parameters of a GET. The HTTP client redacts query strings from any error
// it reports, so the password cannot leak through logs.
type oauthTokenSource struct {
	http     *httpclient.Client
	clientID string
	username string
	password string
	now      func() time.Time

	mu           sync.Mutex
	accessToken  string
	refreshToken string
	expiresAt    time.Time
}

func newOAuthTokenSource(client *httpclient.Client, cfg Config) *oauthTokenSource {
	return &oauthTokenSource{
		http:     client,
		clientID: cfg.ClientID,
		username: cfg.Username,
		password: cfg.Password,
		now:      time.Now,
	}
}

// Token implements TokenSource.
func (s *oauthTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.accessToken != "" && s.now().Before(s.expiresAt) {
		return s.accessToken, nil
	}
	if s.refreshToken != "" {
		if err := s.grant(ctx, url.Values{
			"grant_type":    {"refresh_token"},
			"client_id":     {s.clientID},
			"refresh_token": {s.refreshToken},
		}, "RefreshToken"); err == nil {
			return s.accessToken, nil
		}
		// A failed refresh (expired or revoked refresh token) falls back to
		// the password grant below.
		s.refreshToken = ""
	}
	if err := s.grant(ctx, url.Values{
		"grant_type": {"password"},
		"client_id":  {s.clientID},
		"username":   {s.username},
		"password":   {s.password},
	}, "AccessToken"); err != nil {
		return "", err
	}
	return s.accessToken, nil
}

// Invalidate implements TokenSource.
func (s *oauthTokenSource) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accessToken, s.expiresAt = "", time.Time{}
}

func (s *oauthTokenSource) grant(ctx context.Context, params url.Values, operation string) error {
	var resp dtoauth.TokenResponse
	_, err := s.http.DoJSON(ctx, httpclient.Request{
		Method:    http.MethodGet,
		Path:      tokenPath,
		Query:     params,
		Operation: operation,
	}, &resp)
	if err != nil {
		var aerr *apperror.Error
		if apperror.Classify(err).HTTPStatus == http.StatusBadRequest {
			// Uniware answers a rejected grant with 400 invalid_grant; that
			// is an authentication problem, not a validation one.
			aerr = apperror.Classify(err)
			aerr.Category = apperror.Authentication
			aerr.Retryable = false
			return aerr
		}
		return err
	}
	if strings.TrimSpace(resp.AccessToken) == "" {
		e := apperror.New(apperror.Authentication, "token response carried no access_token")
		e.Integration, e.Operation = PlatformName, operation
		return e
	}
	s.accessToken = resp.AccessToken
	if resp.RefreshToken != "" {
		s.refreshToken = resp.RefreshToken
	}
	ttl := time.Duration(resp.ExpiresIn) * time.Second
	if ttl <= refreshMargin {
		ttl = 5 * time.Minute
	} else {
		ttl -= refreshMargin
	}
	s.expiresAt = s.now().Add(ttl)
	return nil
}
