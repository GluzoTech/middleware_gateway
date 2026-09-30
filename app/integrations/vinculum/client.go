package vinculum

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/httpclient"
	"github.com/gluzo/integration-gateway/app/integrations/vinculum/dto"
)

// Client calls the Vinculum eRetail API. Endpoint methods live in the
// endpoint_*.go files; this file owns transport, the static credential
// headers and envelope handling.
type Client struct {
	http   *httpclient.Client
	cfg    Config
	logger *slog.Logger
}

// Option configures a Client.
type Option func(*options)

type options struct {
	httpOptions []httpclient.Option
	logger      *slog.Logger
}

// WithHTTPOptions passes options to the underlying httpclient (transport,
// retry policy, response limits).
func WithHTTPOptions(opts ...httpclient.Option) Option {
	return func(o *options) { o.httpOptions = append(o.httpOptions, opts...) }
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

	return &Client{http: hc, cfg: cfg, logger: o.logger}, nil
}

// post performs an authenticated JSON request and decodes the body into out.
//
// There is no retry-on-401 loop here, unlike the EasyEcom client: the
// credentials are static, so a 401 means they are wrong and repeating the
// call with the same headers would only ask again.
func (c *Client) post(ctx context.Context, path, operation string, body any, out any) error {
	header := http.Header{}
	header.Set("ApiOwner", c.cfg.APIOwner)
	header.Set("ApiKey", c.cfg.APIKey)

	_, err := c.http.DoJSON(ctx, httpclient.Request{
		Method:    http.MethodPost,
		Path:      path,
		Header:    header,
		Body:      body,
		Operation: operation,
	}, out)
	return err
}

// checkEnvelope turns a Vinculum application-level error (HTTP 200 with a
// non-success responseCode in the body) into a categorised error.
//
// Vinculum reports business failures this way rather than through the HTTP
// status, so a caller that only checked the status would treat a rejection
// as a success carrying no rows. The error is non-retryable: the envelope
// describes a decision about the request, not a transient condition, and a
// transient condition arrives as a 5xx instead.
func checkEnvelope(operation string, env dto.Envelope) error {
	if !env.Failed() {
		return nil
	}
	return &apperror.Error{
		Category:        apperror.ExternalAPI,
		Message:         "vinculum rejected the request",
		Retryable:       false,
		Integration:     PlatformName,
		Operation:       operation,
		ExternalCode:    strconv.Itoa(int(env.ResponseCode)),
		ExternalMessage: strings.TrimSpace(env.ResponseMessage),
	}
}

// invalidRequest reports a request this package refused to send.
func invalidRequest(operation string, err error) error {
	e := apperror.Wrap(apperror.Validation, "invalid request", err)
	e.Integration, e.Operation = PlatformName, operation
	return e
}

// Location reports the default orderLocation, used when a call is made
// outside a route.
func (c *Client) Location() string { return strings.TrimSpace(c.cfg.Location) }

// SellableBucket reports the configured sellable stock bucket. Empty means
// every bucket is accepted.
func (c *Client) SellableBucket() string { return strings.TrimSpace(c.cfg.SellableBucket) }

// Timeout reports the per-attempt timeout in use.
func (c *Client) Timeout() time.Duration { return c.cfg.Timeout }
