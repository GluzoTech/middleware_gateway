package httpclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/correlation"
	"github.com/gluzo/integration-gateway/app/httpclient"
)

// recordingSleep captures backoff delays instead of waiting.
type recordingSleep struct {
	delays []time.Duration
	err    error
}

func (r *recordingSleep) sleep(_ context.Context, d time.Duration) error {
	r.delays = append(r.delays, d)
	return r.err
}

func newClient(t *testing.T, base string, sleeper *recordingSleep, opts ...httpclient.Option) *httpclient.Client {
	t.Helper()
	all := append([]httpclient.Option{httpclient.WithSleep(sleeper.sleep), httpclient.WithTimeout(2 * time.Second)}, opts...)
	c, err := httpclient.New("testapi", base, all...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNewRejectsBadBaseURL(t *testing.T) {
	for _, bad := range []string{"", "not a url", "ftp://x", "/relative", "https://"} {
		if _, err := httpclient.New("x", bad); err == nil {
			t.Errorf("New(%q) accepted an invalid base URL", bad)
		}
	}
}

func TestSuccessfulJSONCallPropagatesHeadersAndBody(t *testing.T) {
	var got struct {
		path, query, corr, accept, ua, ct, auth string
		body                                    map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.query = r.URL.Path, r.URL.RawQuery
		got.corr = r.Header.Get(correlation.HeaderName)
		got.accept, got.ua, got.ct, got.auth = r.Header.Get("Accept"), r.Header.Get("User-Agent"), r.Header.Get("Content-Type"), r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"id":"42"}`))
	}))
	defer srv.Close()

	c := newClient(t, srv.URL+"/api/", &recordingSleep{})
	ctx := correlation.WithID(context.Background(), "INT-test")
	var out struct {
		OK bool   `json:"ok"`
		ID string `json:"id"`
	}
	resp, err := c.DoJSON(ctx, httpclient.Request{
		Method:    http.MethodPost,
		Path:      "/orders",
		Query:     url.Values{"reference_code": {"R 1"}},
		Header:    http.Header{"Authorization": {"Bearer secret"}},
		Body:      map[string]string{"sku": "ABC"},
		Operation: "CreateOrder",
	}, &out)
	if err != nil {
		t.Fatalf("DoJSON: %v", err)
	}
	if resp.StatusCode != 200 || resp.Attempts != 1 || !out.OK || out.ID != "42" {
		t.Fatalf("unexpected response %+v / %+v", resp, out)
	}
	if got.path != "/api/orders" || got.query != "reference_code=R+1" {
		t.Fatalf("resolved URL = %s?%s", got.path, got.query)
	}
	if got.corr != "INT-test" || got.accept != "application/json" || got.ua != httpclient.DefaultUserAgent || got.ct != "application/json" || got.auth != "Bearer secret" {
		t.Fatalf("headers not propagated: %+v", got)
	}
	if got.body["sku"] != "ABC" {
		t.Fatalf("body not encoded: %v", got.body)
	}
}

func TestRetriesTransientStatusThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	sleeper := &recordingSleep{}
	c := newClient(t, srv.URL, sleeper, httpclient.WithRetryPolicy(httpclient.RetryPolicy{MaxAttempts: 3, BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second}))
	resp, err := c.Do(context.Background(), httpclient.Request{Method: http.MethodGet, Path: "/x", Operation: "X"})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.Attempts != 3 || calls.Load() != 3 {
		t.Fatalf("attempts = %d, calls = %d, want 3", resp.Attempts, calls.Load())
	}
	if len(sleeper.delays) != 2 {
		t.Fatalf("slept %d times, want 2", len(sleeper.delays))
	}
	// Equal jitter keeps each delay within [base/2, base] of the exponential step.
	if sleeper.delays[0] < 50*time.Millisecond || sleeper.delays[0] > 100*time.Millisecond {
		t.Errorf("first delay %s outside jitter window", sleeper.delays[0])
	}
	if sleeper.delays[1] < 100*time.Millisecond || sleeper.delays[1] > 200*time.Millisecond {
		t.Errorf("second delay %s outside jitter window", sleeper.delays[1])
	}
}

func TestDoesNotRetryClientErrorsAndReturnsBody(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"E1","message":"bad sku"}`))
	}))
	defer srv.Close()

	sleeper := &recordingSleep{}
	c := newClient(t, srv.URL, sleeper)
	resp, err := c.Do(context.Background(), httpclient.Request{Method: http.MethodPost, Path: "/x", Operation: "X"})
	if err == nil {
		t.Fatal("expected error")
	}
	var aerr *apperror.Error
	if !errors.As(err, &aerr) {
		t.Fatalf("error is not *apperror.Error: %T", err)
	}
	if aerr.Category != apperror.Validation || aerr.Retryable || aerr.HTTPStatus != 400 || aerr.Integration != "testapi" || aerr.Operation != "X" {
		t.Fatalf("unexpected error: %+v", aerr)
	}
	if !strings.Contains(aerr.ExternalMessage, "bad sku") {
		t.Fatalf("external message not captured: %q", aerr.ExternalMessage)
	}
	if resp == nil || !strings.Contains(string(resp.Body), "E1") {
		t.Fatal("response body not returned alongside error")
	}
	if calls.Load() != 1 || len(sleeper.delays) != 0 {
		t.Fatalf("client error was retried: calls=%d sleeps=%d", calls.Load(), len(sleeper.delays))
	}
}

func TestRateLimitRespectsRetryAfter(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	sleeper := &recordingSleep{}
	c := newClient(t, srv.URL, sleeper)
	if _, err := c.Do(context.Background(), httpclient.Request{Method: http.MethodGet, Path: "/x"}); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(sleeper.delays) != 1 || sleeper.delays[0] != 3*time.Second {
		t.Fatalf("delays = %v, want exactly the Retry-After of 3s", sleeper.delays)
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	sleeper := &recordingSleep{}
	c := newClient(t, srv.URL, sleeper)
	resp, err := c.Do(context.Background(), httpclient.Request{Method: http.MethodGet, Path: "/x"})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != 3 || len(sleeper.delays) != 2 {
		t.Fatalf("calls=%d sleeps=%d, want 3/2", calls.Load(), len(sleeper.delays))
	}
	if resp == nil || resp.Attempts != 3 {
		t.Fatalf("response attempts not recorded: %+v", resp)
	}
	if !apperror.IsRetryable(err) || apperror.CategoryOf(err) != apperror.ExternalAPI {
		t.Fatalf("final error should remain a retryable external error: %v", err)
	}
}

func TestPerAttemptTimeoutIsRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	sleeper := &recordingSleep{}
	c := newClient(t, srv.URL, sleeper, httpclient.WithTimeout(100*time.Millisecond))
	resp, err := c.Do(context.Background(), httpclient.Request{Method: http.MethodGet, Path: "/slow"})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", resp.Attempts)
	}
}

func TestCallerCancellationStopsRetrying(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	sleeper := &recordingSleep{err: context.Canceled}
	c := newClient(t, srv.URL, sleeper)
	_, err := c.Do(context.Background(), httpclient.Request{Method: http.MethodGet, Path: "/x"})
	if err == nil {
		t.Fatal("expected error")
	}
	if apperror.IsRetryable(err) {
		t.Fatalf("cancellation must not be reported as retryable: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}

func TestOversizedResponseIsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 100)))
	}))
	defer srv.Close()

	c := newClient(t, srv.URL, &recordingSleep{}, httpclient.WithMaxResponseBytes(50))
	_, err := c.Do(context.Background(), httpclient.Request{Method: http.MethodGet, Path: "/big"})
	if err == nil || apperror.IsRetryable(err) {
		t.Fatalf("oversized body must fail without retry: %v", err)
	}
}

func TestInvalidJSONIsNotRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "<html>not json</html>")
	}))
	defer srv.Close()

	c := newClient(t, srv.URL, &recordingSleep{})
	var out map[string]any
	_, err := c.DoJSON(context.Background(), httpclient.Request{Method: http.MethodGet, Path: "/x"}, &out)
	if err == nil || apperror.IsRetryable(err) || apperror.CategoryOf(err) != apperror.ExternalAPI {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTransportErrorsNeverEchoQueryCredentials(t *testing.T) {
	// Port 9 is reserved and closed, so the dial fails immediately.
	c := newClient(t, "http://127.0.0.1:9", &recordingSleep{}, httpclient.WithRetryPolicy(httpclient.NoRetry()), httpclient.WithTimeout(500*time.Millisecond))
	_, err := c.Do(context.Background(), httpclient.Request{
		Method: http.MethodGet, Path: "/oauth/token",
		Query: url.Values{"grant_type": {"password"}, "password": {"hunter2"}},
	})
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "grant_type") {
		t.Fatalf("query string leaked into error: %v", err)
	}
	if info := apperror.InfoOf(err); strings.Contains(info.Detail, "hunter2") {
		t.Fatalf("query string leaked into error detail: %+v", info)
	}
}

func TestRetryStatusOverride(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	policy := httpclient.RetryPolicy{MaxAttempts: 2, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond, RetryStatus: func(s int) bool { return s == 404 }}
	c := newClient(t, srv.URL, &recordingSleep{}, httpclient.WithRetryPolicy(policy))
	_, _ = c.Do(context.Background(), httpclient.Request{Method: http.MethodGet, Path: "/x"})
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2 with 404 marked retryable", calls.Load())
	}
}
