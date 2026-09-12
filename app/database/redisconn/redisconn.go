// Package redisconn owns the Redis client used by the job queue.
package redisconn

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Connect parses a redis:// or rediss:// URL, opens a client and verifies it
// with a bounded ping.
//
// URL parse failures are reported without the original string because
// net/url error text embeds the full URL, password included.
func Connect(ctx context.Context, url string, timeout time.Duration) (*redis.Client, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, errors.New("redis: REDIS_URL is not a valid redis:// or rediss:// URL")
	}
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
		return nil, fmt.Errorf("redis: ping: %w", err)
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
