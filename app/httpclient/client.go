// Package httpclient is the resilient HTTP foundation shared by every
// integration client.
//
// It owns the generic concerns of talking to an external API: base URL
// resolution, per-attempt timeouts, bounded retries with exponential backoff
// and jitter, Retry-After handling, response size limits, correlation ID
// propagation and structured errors. Business operations (which endpoint,
// which payload, which DTO) belong in the integration packages.
package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/correlation"
)

// Defaults applied by New.
const (
	DefaultTimeout          = 15 * time.Second
	DefaultMaxResponseBytes = 4 << 20
	DefaultMaxAttempts      = 3
	DefaultBaseDelay        = 500 * time.Millisecond
	DefaultMaxDelay         = 10 * time.Second
	DefaultUserAgent        = "gluzo-integration-gateway"

	// maxRetryAfter caps how long a Retry-After header can make us wait.
	maxRetryAfter = 60 * time.Second
	// errorSnippetBytes bounds how much of an error body is kept.
	errorSnippetBytes = 512
)

// RetryPolicy controls how failed attempts are repeated.
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts including the first.
	MaxAttempts int
	// BaseDelay is the backoff before the second attempt; it doubles each
	// attempt (with jitter) up to MaxDelay.
	BaseDelay time.Duration
	MaxDelay  time.Duration
	// RetryStatus decides whether a response status warrants another
	// attempt. Nil uses apperror.FromHTTPStatus.
	RetryStatus func(status int) bool
}

// DefaultRetryPolicy retries transient failures three times in total.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxAttempts: DefaultMaxAttempts, BaseDelay: DefaultBaseDelay, MaxDelay: DefaultMaxDelay}
}

// NoRetry performs exactly one attempt.
func NoRetry() RetryPolicy {
	return RetryPolicy{MaxAttempts: 1, BaseDelay: DefaultBaseDelay, MaxDelay: DefaultMaxDelay}
}

// Client performs requests against one external API.
type Client struct {
	name             string
	baseURL          *url.URL
	http             *http.Client
	timeout          time.Duration
	retry            RetryPolicy
	maxResponseBytes int64
	logger           *slog.Logger
	userAgent        string
	sleep            func(ctx context.Context, d time.Duration) error
	randFloat        func() float64
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient replaces the underlying transport client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithTimeout sets the per-attempt timeout.
func WithTimeout(d time.Duration) Option { return func(c *Client) { c.timeout = d } }

// WithRetryPolicy sets the retry policy.
func WithRetryPolicy(p RetryPolicy) Option { return func(c *Client) { c.retry = p } }

// WithMaxResponseBytes caps the response body size.
func WithMaxResponseBytes(n int64) Option { return func(c *Client) { c.maxResponseBytes = n } }

// WithLogger sets the logger used for per-attempt debug records.
func WithLogger(l *slog.Logger) Option { return func(c *Client) { c.logger = l } }

// WithUserAgent sets the User-Agent header.
func WithUserAgent(ua string) Option { return func(c *Client) { c.userAgent = ua } }

// WithSleep replaces the backoff sleeper; intended for tests.
func WithSleep(fn func(ctx context.Context, d time.Duration) error) Option {
	return func(c *Client) { c.sleep = fn }
}

// New builds a client for the API at baseURL. name identifies the
// integration in errors and logs.
func New(name, baseURL string, opts ...Option) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("httpclient %s: base URL must be an absolute http(s) URL", name)
	}
	c := &Client{
		name:             name,
		baseURL:          u,
		http:             &http.Client{},
		timeout:          DefaultTimeout,
		retry:            DefaultRetryPolicy(),
		maxResponseBytes: DefaultMaxResponseBytes,
		logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		userAgent:        DefaultUserAgent,
		sleep:            defaultSleep,
		randFloat:        rand.Float64,
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.retry.MaxAttempts < 1 {
		c.retry.MaxAttempts = 1
	}
	if c.retry.BaseDelay <= 0 {
		c.retry.BaseDelay = DefaultBaseDelay
	}
	if c.retry.MaxDelay < c.retry.BaseDelay {
		c.retry.MaxDelay = c.retry.BaseDelay
	}
	if c.timeout <= 0 {
		return nil, fmt.Errorf("httpclient %s: timeout must be positive", name)
	}
	if c.maxResponseBytes <= 0 {
		return nil, fmt.Errorf("httpclient %s: max response bytes must be positive", name)
	}
	return c, nil
}

// Name returns the integration name.
func (c *Client) Name() string { return c.name }

// Request describes one API call.
type Request struct {
	Method string
	// Path is resolved relative to the base URL.
	Path  string
	Query url.Values
	// Header entries are added after the defaults, so they can override
	// Accept or Content-Type. Credentials are supplied here by the
	// integration client.
	Header http.Header
	// Body is JSON-encoded when non-nil. Use RawBody for other encodings.
	Body        any
	RawBody     []byte
	ContentType string
	// Operation names the business call for logs and errors, e.g. "GetOrderDetails".
	Operation string
}

// Response is a received HTTP response with its body fully read.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	Attempts   int
	Duration   time.Duration
}

// Do performs req, retrying transient failures per the policy.
//
// On a non-2xx status the returned error is an *apperror.Error carrying the
// status and a bounded body snippet, and the Response is also returned so
// callers can decode a structured error body. On transport failures the
// Response is nil.
func (c *Client) Do(ctx context.Context, req Request) (*Response, error) {
	body, contentType, err := encodeBody(req)
	if err != nil {
		return nil, c.fail(req, apperror.Wrap(apperror.Internal, "encode request body", err))
	}
	target := c.resolve(req.Path, req.Query)

	start := time.Now()
	var lastResp *Response
	var lastErr *apperror.Error
	for attempt := 1; attempt <= c.retry.MaxAttempts; attempt++ {
		resp, aerr := c.attempt(ctx, req, target, body, contentType, attempt)
		if aerr == nil {
			resp.Attempts = attempt
			resp.Duration = time.Since(start)
			return resp, nil
		}
		lastResp, lastErr = resp, aerr
		if lastResp != nil {
			lastResp.Attempts = attempt
			lastResp.Duration = time.Since(start)
		}
		if !aerr.Retryable || attempt == c.retry.MaxAttempts || ctx.Err() != nil {
			break
		}

		delay := c.backoff(attempt, aerr.RetryAfter)
		c.logger.DebugContext(ctx, "external call retry scheduled",
			slog.String("integration", c.name),
			slog.String("operation", req.Operation),
			slog.Int("attempt", attempt),
			slog.Int64("delay_ms", delay.Milliseconds()),
			slog.String("reason", aerr.Message),
			slog.String("correlation_id", correlation.FromContext(ctx)),
		)
		if err := c.sleep(ctx, delay); err != nil {
			return lastResp, c.fail(req, apperror.Classify(err))
		}
	}
	return lastResp, lastErr
}

// DoJSON performs req and decodes a 2xx JSON body into out.
func (c *Client) DoJSON(ctx context.Context, req Request, out any) (*Response, error) {
	resp, err := c.Do(ctx, req)
	if err != nil {
		return resp, err
	}
	if out == nil || len(bytes.TrimSpace(resp.Body)) == 0 {
		return resp, nil
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		e := apperror.Wrap(apperror.ExternalAPI, "invalid JSON response", err)
		e.HTTPStatus = resp.StatusCode
		return resp, c.fail(req, e)
	}
	return resp, nil
}

func (c *Client) attempt(ctx context.Context, req Request, target string, body []byte, contentType string, attempt int) (*Response, *apperror.Error) {
	attemptCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	httpReq, err := http.NewRequestWithContext(attemptCtx, req.Method, target, reader)
	if err != nil {
		return nil, c.fail(req, apperror.Wrap(apperror.Internal, "build request", err))
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		httpReq.Header.Set("Content-Type", contentType)
	}
	if id := correlation.FromContext(ctx); id != "" {
		httpReq.Header.Set(correlation.HeaderName, id)
	}
	for k, vs := range req.Header {
		httpReq.Header.Del(k)
		for _, v := range vs {
			httpReq.Header.Add(k, v)
		}
	}

	started := time.Now()
	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		aerr := c.classifyTransport(ctx, attemptCtx, err)
		c.logAttempt(ctx, req, attempt, 0, time.Since(started), aerr)
		return nil, c.fail(req, aerr)
	}
	defer httpResp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(httpResp.Body, c.maxResponseBytes+1))
	if err != nil {
		aerr := c.classifyTransport(ctx, attemptCtx, err)
		c.logAttempt(ctx, req, attempt, httpResp.StatusCode, time.Since(started), aerr)
		return nil, c.fail(req, aerr)
	}
	if int64(len(data)) > c.maxResponseBytes {
		aerr := apperror.New(apperror.ExternalAPI, fmt.Sprintf("response body exceeds %d bytes", c.maxResponseBytes))
		aerr.HTTPStatus = httpResp.StatusCode
		c.logAttempt(ctx, req, attempt, httpResp.StatusCode, time.Since(started), aerr)
		return nil, c.fail(req, aerr)
	}

	resp := &Response{StatusCode: httpResp.StatusCode, Header: httpResp.Header.Clone(), Body: data}
	if httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
		c.logAttempt(ctx, req, attempt, httpResp.StatusCode, time.Since(started), nil)
		return resp, nil
	}

	category, retryable := apperror.FromHTTPStatus(httpResp.StatusCode)
	if c.retry.RetryStatus != nil {
		retryable = c.retry.RetryStatus(httpResp.StatusCode)
	}
	aerr := &apperror.Error{
		Category:        category,
		Message:         fmt.Sprintf("unexpected status %d", httpResp.StatusCode),
		Retryable:       retryable,
		HTTPStatus:      httpResp.StatusCode,
		ExternalMessage: snippet(data),
		RetryAfter:      parseRetryAfter(httpResp.Header.Get("Retry-After"), time.Now()),
	}
	c.logAttempt(ctx, req, attempt, httpResp.StatusCode, time.Since(started), aerr)
	return resp, c.fail(req, aerr)
}

// classifyTransport turns a transport-level failure into a categorised error.
// A cancellation coming from the caller's context is never retried; an
// attempt timing out under its own deadline is.
func (c *Client) classifyTransport(ctx, attemptCtx context.Context, err error) *apperror.Error {
	err = redactURLError(err)
	if ctx.Err() != nil {
		return apperror.Classify(ctx.Err())
	}
	if errors.Is(attemptCtx.Err(), context.DeadlineExceeded) {
		return apperror.Wrap(apperror.Timeout, fmt.Sprintf("attempt exceeded %s", c.timeout), err)
	}
	return apperror.Classify(err)
}

// redactURLError strips the query string and user info from the URL that
// net/http embeds in transport errors, so credentials passed as query
// parameters (as some OAuth endpoints require) never reach logs or state.
func redactURLError(err error) error {
	var uerr *url.Error
	if !errors.As(err, &uerr) {
		return err
	}
	u, perr := url.Parse(uerr.URL)
	if perr != nil {
		return &url.Error{Op: uerr.Op, URL: "[redacted]", Err: uerr.Err}
	}
	u.RawQuery = ""
	u.User = nil
	return &url.Error{Op: uerr.Op, URL: u.String(), Err: uerr.Err}
}

// fail stamps integration context onto an error.
func (c *Client) fail(req Request, e *apperror.Error) *apperror.Error {
	e.Integration = c.name
	e.Operation = req.Operation
	return e
}

func (c *Client) logAttempt(ctx context.Context, req Request, attempt, status int, took time.Duration, aerr *apperror.Error) {
	attrs := []any{
		slog.String("integration", c.name),
		slog.String("operation", req.Operation),
		slog.String("method", req.Method),
		slog.String("path", req.Path),
		slog.Int("status", status),
		slog.Int("attempt", attempt),
		slog.Int64("duration_ms", took.Milliseconds()),
		slog.String("correlation_id", correlation.FromContext(ctx)),
	}
	if aerr != nil {
		attrs = append(attrs, slog.String("error_category", string(aerr.Category)), slog.String("error", aerr.Message))
		c.logger.LogAttrs(ctx, slog.LevelWarn, "external call failed", toAttrs(attrs)...)
		return
	}
	c.logger.LogAttrs(ctx, slog.LevelDebug, "external call", toAttrs(attrs)...)
}

func toAttrs(args []any) []slog.Attr {
	out := make([]slog.Attr, 0, len(args))
	for _, a := range args {
		if attr, ok := a.(slog.Attr); ok {
			out = append(out, attr)
		}
	}
	return out
}

// backoff returns the delay before the next attempt: exponential growth with
// equal jitter, never shorter than a server-provided Retry-After (capped).
func (c *Client) backoff(attempt int, retryAfter time.Duration) time.Duration {
	exp := float64(c.retry.BaseDelay) * math.Pow(2, float64(attempt-1))
	d := time.Duration(math.Min(exp, float64(c.retry.MaxDelay)))
	d = d/2 + time.Duration(c.randFloat()*float64(d/2))
	if retryAfter > d {
		d = min(retryAfter, maxRetryAfter)
	}
	return d
}

// resolve joins the base URL path with the request path and query.
func (c *Client) resolve(path string, query url.Values) string {
	u := *c.baseURL
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + strings.TrimPrefix(path, "/")
	u.RawQuery = ""
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	return u.String()
}

func encodeBody(req Request) ([]byte, string, error) {
	switch {
	case req.RawBody != nil:
		ct := req.ContentType
		if ct == "" {
			ct = "application/json"
		}
		return req.RawBody, ct, nil
	case req.Body != nil:
		data, err := json.Marshal(req.Body)
		if err != nil {
			return nil, "", err
		}
		return data, "application/json", nil
	default:
		return nil, "", nil
	}
}

// parseRetryAfter accepts both delay-seconds and HTTP-date forms.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > errorSnippetBytes {
		return s[:errorSnippetBytes] + "..."
	}
	return s
}

func defaultSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
