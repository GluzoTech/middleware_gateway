package easyecom

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/httpclient"
)

// Client calls the EasyEcom API. Endpoint methods live in the endpoint_*.go
// files; this file owns transport, authentication headers and envelope
// handling.
type Client struct {
	http   *httpclient.Client
	cfg    Config
	tokens TokenSource
	logger *slog.Logger
}

// Option configures a Client.
type Option func(*options)

type options struct {
	httpOptions []httpclient.Option
	tokens      TokenSource
	logger      *slog.Logger
}

// WithHTTPOptions passes options to the underlying httpclient (transport,
// retry policy, response limits).
func WithHTTPOptions(opts ...httpclient.Option) Option {
	return func(o *options) { o.httpOptions = append(o.httpOptions, opts...) }
}

// WithTokenSource replaces the token source; intended for tests.
func WithTokenSource(ts TokenSource) Option {
	return func(o *options) { o.tokens = ts }
}

// WithLogger sets the logger used for call diagnostics.
func WithLogger(l *slog.Logger) Option {
	return func(o *options) { o.logger = l }
}

// NewClient validates cfg and builds a Client.
func NewClient(cfg Config, opts ...Option) (*Client, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = httpclient.DefaultTimeout
	}

	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if o.logger == nil {
		o.logger = slog.Default()
	}

	httpOpts := append([]httpclient.Option{
		httpclient.WithTimeout(cfg.Timeout),
		httpclient.WithLogger(o.logger),
	}, o.httpOptions...)
	hc, err := httpclient.New(PlatformName, cfg.BaseURL, httpOpts...)
	if err != nil {
		return nil, err
	}

	c := &Client{http: hc, cfg: cfg, logger: o.logger}
	switch {
	case o.tokens != nil:
		c.tokens = o.tokens
	case strings.TrimSpace(cfg.JWTToken) != "":
		c.tokens = staticTokenSource{token: strings.TrimSpace(cfg.JWTToken)}
	default:
		c.tokens = newLoginTokenSource(hc, cfg)
	}
	return c, nil
}

// call performs an authenticated request against the process default
// location.
func (c *Client) call(ctx context.Context, req httpclient.Request, out any) error {
	return c.callAs(ctx, "", req, out)
}

// callAs performs an authenticated request scoped to one EasyEcom location
// and decodes the JSON body into out. A 401 invalidates that location's
// cached JWT and the call is repeated once with a fresh token, which covers
// token expiry and server-side revocation.
//
// The location is a per-call argument rather than client state because one
// process writes to several locations. EasyEcom scopes the JWT to the
// location it was issued for, so passing the wrong one here does not write
// the wrong warehouse — it is rejected.
func (c *Client) callAs(ctx context.Context, locationKey string, req httpclient.Request, out any) error {
	for attempt := 0; attempt < 2; attempt++ {
		token, err := c.tokens.Token(ctx, locationKey)
		if err != nil {
			return err
		}
		if req.Header == nil {
			req.Header = http.Header{}
		}
		req.Header.Set("X-API-Key", c.cfg.APIKey)
		req.Header.Set("Authorization", "Bearer "+token)

		_, err = c.http.DoJSON(ctx, req, out)
		if err == nil {
			return nil
		}
		var aerr *apperror.Error
		if attempt == 0 && errors.As(err, &aerr) && aerr.HTTPStatus == http.StatusUnauthorized {
			c.logger.WarnContext(ctx, "easyecom rejected the JWT; refreshing",
				slog.String("operation", req.Operation),
				slog.String("location", locationKey))
			c.tokens.Invalidate(locationKey)
			continue
		}
		return err
	}
	return nil
}

// checkEnvelope turns an EasyEcom application-level error (HTTP 200 with a
// non-success code in the body) into a categorised error.
func checkEnvelope(operation string, code int, message string) error {
	if code == 0 || (code >= 200 && code < 300) {
		return nil
	}
	category, retryable := apperror.FromHTTPStatus(code)
	e := &apperror.Error{
		Category:        category,
		Message:         fmt.Sprintf("easyecom reported code %d", code),
		Retryable:       retryable,
		Integration:     PlatformName,
		Operation:       operation,
		ExternalCode:    fmt.Sprint(code),
		ExternalMessage: strings.TrimSpace(message),
	}
	return e
}

// Timeout reports the per-attempt timeout in use.
func (c *Client) Timeout() time.Duration { return c.cfg.Timeout }
