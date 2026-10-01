package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Locker keeps two replicas from running the same job at the same time.
//
// It is not what makes a tick fire once — the store's claim does that. The
// lock covers the case the claim cannot: a run that takes longer than its
// interval, whose next tick would otherwise start on another replica while
// the first is still talking to the vendor. Two overlapping stock sweeps
// double the load on a vendor whose documented rate limit is 80 calls per
// five minutes.
type Locker interface {
	// Acquire takes the lock for key. ok is false when another holder has
	// it; that is an ordinary outcome, not an error.
	//
	// The returned release is safe to call exactly once and must be called
	// even when the run failed. It takes its own context because it usually
	// runs during shutdown, when the run's context is already cancelled.
	Acquire(ctx context.Context, key string, ttl time.Duration) (release func(context.Context), ok bool, err error)
}

// NopLocker grants every request. Correct only where a single process runs
// the scheduler, and used by tests that are not about contention.
type NopLocker struct{}

// Acquire implements Locker.
func (NopLocker) Acquire(context.Context, string, time.Duration) (func(context.Context), bool, error) {
	return func(context.Context) {}, true, nil
}

// RedisLocker implements Locker with a short-lived key per job name.
type RedisLocker struct {
	client *redis.Client
	prefix string
	logger *slog.Logger
}

// DefaultLockPrefix namespaces lock keys.
const DefaultLockPrefix = "gluzo:scheduler:lock:"

// NewRedisLocker wraps client.
func NewRedisLocker(client *redis.Client, logger *slog.Logger) *RedisLocker {
	if logger == nil {
		logger = slog.Default()
	}
	return &RedisLocker{client: client, prefix: DefaultLockPrefix, logger: logger}
}

// releaseScript deletes the key only when it still holds our token.
//
// A plain DEL would be wrong: if this replica stalled past the TTL, the lock
// has already expired and another replica may hold it. Deleting then would
// release someone else's lock and produce exactly the overlap the lock
// exists to prevent.
var releaseScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
	return redis.call("del", KEYS[1])
end
return 0
`)

// Acquire implements Locker with SET NX PX.
func (l *RedisLocker) Acquire(ctx context.Context, key string, ttl time.Duration) (func(context.Context), bool, error) {
	if ttl <= 0 {
		ttl = DefaultLockTTL
	}
	full := l.prefix + key
	token := uuid.NewString()

	ok, err := l.client.SetNX(ctx, full, token, ttl).Result()
	if err != nil {
		return nil, false, fmt.Errorf("scheduler: acquire lock %s: %w", key, err)
	}
	if !ok {
		return nil, false, nil
	}

	var once sync.Once
	release := func(rctx context.Context) {
		once.Do(func() {
			if err := releaseScript.Run(rctx, l.client, []string{full}, token).Err(); err != nil && err != redis.Nil {
				// The lock expires on its own, so a failed release costs at
				// most one skipped tick. Worth a log, not worth a failure.
				l.logger.Warn("scheduler could not release its lock",
					slog.String("job", key),
					slog.String("error", err.Error()))
			}
		})
	}
	return release, true, nil
}

// MemoryLocker is an in-process Locker for tests. Sharing one between two
// Schedulers reproduces the contention two replicas would have, without a
// Redis server.
type MemoryLocker struct {
	mu   sync.Mutex
	held map[string]time.Time
	now  func() time.Time
}

// NewMemoryLocker builds an empty locker.
func NewMemoryLocker() *MemoryLocker {
	return &MemoryLocker{held: make(map[string]time.Time), now: time.Now}
}

// SetClock replaces the clock; intended for tests of expiry.
func (m *MemoryLocker) SetClock(fn func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if fn != nil {
		m.now = fn
	}
}

// Acquire implements Locker.
func (m *MemoryLocker) Acquire(_ context.Context, key string, ttl time.Duration) (func(context.Context), bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if expiry, ok := m.held[key]; ok && expiry.After(now) {
		return nil, false, nil
	}
	if ttl <= 0 {
		ttl = DefaultLockTTL
	}
	m.held[key] = now.Add(ttl)

	var once sync.Once
	release := func(context.Context) {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			delete(m.held, key)
		})
	}
	return release, true, nil
}
