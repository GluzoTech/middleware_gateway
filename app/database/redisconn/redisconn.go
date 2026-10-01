// Package redisconn owns the Redis client used by the job queue.
package redisconn

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Config describes how to reach Redis.
//
// Password is optional and overrides any password carried in the URL. A
// managed instance's password routinely contains characters that must be
// percent-encoded inside a URL, and getting that wrong fails as an
// authentication error rather than as a parse error, so supplying it
// separately is the safer route.
type Config struct {
	URL            string
	Password       string
	ConnectTimeout time.Duration
}

// Connect parses a redis:// or rediss:// URL, opens a client and verifies it
// with a bounded ping.
//
// URL parse failures are reported without the original string because
// net/url error text embeds the full URL, password included.
func Connect(ctx context.Context, cfg Config) (*redis.Client, error) {
	opts, err := redis.ParseURL(cfg.URL)
	if err != nil {
		return nil, errors.New("redis: REDIS_URL is not a valid redis:// or rediss:// URL")
	}
	if cfg.Password != "" {
		opts.Password = cfg.Password
	}
	timeout := cfg.ConnectTimeout
	if timeout > 0 {
		opts.DialTimeout = timeout
	}

	client := redis.NewClient(opts)

	pingCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		pingCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		// opts.Addr is host:port only — ParseURL puts the password in
		// opts.Password — so naming it here is safe and saves guessing which
		// endpoint a bare "context deadline exceeded" refers to.
		return nil, fmt.Errorf("redis: ping %s: %w", opts.Addr, err)
	}
	return client, nil
}

// Checker adapts a client to the readiness probe.
type Checker struct {
	Client *redis.Client
}

// Name implements health.Checker.
func (c Checker) Name() string { return "redis" }

// Check implements health.Checker.
func (c Checker) Check(ctx context.Context) error { return c.Client.Ping(ctx).Err() }
