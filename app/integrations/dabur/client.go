package dabur

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/httpclient"
	"github.com/gluzo/integration-gateway/app/integrations/dabur/dto"
)

// FacilityHeader carries the facility code on facility-level APIs.
const FacilityHeader = "Facility"

// Client calls the Uniware API. Endpoint methods live in the endpoint_*.go
// files; this file owns transport, authentication and envelope handling.
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

// WithHTTPOptions passes options to the underlying httpclient.
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
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()
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
	if o.tokens != nil {
		c.tokens = o.tokens
	} else {
		c.tokens = newOAuthTokenSource(hc, cfg)
	}
	return c, nil
}

// Config returns the effective configuration (defaults applied).
func (c *Client) Config() Config { return c.cfg }

// call performs an authenticated request, optionally at facility level, and
// decodes the JSON body into out. A 401 invalidates the cached token and the
// call is repeated once.
func (c *Client) call(ctx context.Context, req httpclient.Request, facility string, out any) error {
	for attempt := 0; attempt < 2; attempt++ {
		token, err := c.tokens.Token(ctx)
		if err != nil {
			return err
		}
		if req.Header == nil {
			req.Header = http.Header{}
		}
		// Uniware documents the scheme in lower case.
		req.Header.Set("Authorization", "bearer "+token)
		if facility != "" {
			req.Header.Set(FacilityHeader, facility)
		}

		_, err = c.http.DoJSON(ctx, req, out)
		if err == nil {
			return nil
		}
		var aerr *apperror.Error
		if attempt == 0 && errors.As(err, &aerr) && aerr.HTTPStatus == http.StatusUnauthorized {
			c.logger.WarnContext(ctx, "uniware rejected the access token; re-authenticating", slog.String("operation", req.Operation))
			c.tokens.Invalidate()
			continue
		}
		return err
	}
	return nil
}

// checkResponse turns a Uniware application-level failure (HTTP 200 with
// successful=false) into a categorised, non-retryable error carrying the
// first reported error code and description.
func checkResponse(operation string, r dto.Response) error {
	if r.Successful {
		return nil
	}
	e := &apperror.Error{
		Category:    apperror.ExternalAPI,
		Message:     "uniware reported failure",
		Retryable:   false,
		Integration: PlatformName,
		Operation:   operation,
	}
	if first := r.FirstError(); first != nil {
		e.ExternalCode = fmt.Sprint(first.Code)
		e.ExternalMessage = first.Text()
	} else if strings.TrimSpace(r.Message) != "" {
		e.ExternalMessage = strings.TrimSpace(r.Message)
	}
	return e
}
