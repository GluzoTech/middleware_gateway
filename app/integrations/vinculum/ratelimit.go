package vinculum

import (
	"context"
	"sync"
	"time"
)

// Vinculum's documented limit on order creation: 80 calls per 5 minutes,
// roughly one order every four seconds.
const (
	DefaultOrderRateLimit  = 80
	DefaultOrderRateWindow = 5 * time.Minute
)

// rateLimiter enforces a maximum number of calls in a sliding window.
//
// It is applied to order creation only, because that is where the limit is
// documented. Throttling the stock sweep on the same budget would be
// inventing a constraint, and a sweep that paced itself at one page every
// four seconds would take hours.
//
// The window slides rather than resetting on a fixed boundary: a fixed
// window lets 160 calls through across its edge — 80 at the end of one and 80
// at the start of the next — which is exactly the burst the limit exists to
// prevent.
//
// This bounds one process. Several replicas each hold their own budget, so a
// deployment that runs more than one worker needs either a shared limiter or
// a per-replica share of the ceiling; see the Phase 5 note.
type rateLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	// calls holds the timestamps of recent calls, oldest first. It is
	// bounded by max, so it stays small.
	calls []time.Time

	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
}

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	if max < 1 {
		max = DefaultOrderRateLimit
	}
	if window <= 0 {
		window = DefaultOrderRateWindow
	}
	return &rateLimiter{
		max:    max,
		window: window,
		calls:  make([]time.Time, 0, max),
		now:    time.Now,
		sleep:  sleepCtx,
	}
}

// Wait blocks until another call is permitted, then records it.
//
// It returns the context's error if the wait is cancelled, so a shutdown
// mid-wait does not consume the slot: the call never happened, so nothing is
// recorded for it.
func (l *rateLimiter) Wait(ctx context.Context) error {
	for {
		wait, ok := l.reserve()
		if ok {
			return nil
		}
		if err := l.sleep(ctx, wait); err != nil {
			return err
		}
	}
}

// reserve takes a slot if one is free, or reports how long until the oldest
// call leaves the window.
func (l *rateLimiter) reserve() (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	cutoff := now.Add(-l.window)
	// Drop calls that have aged out. The slice is ordered, so this is a
	// prefix.
	keep := 0
	for keep < len(l.calls) && !l.calls[keep].After(cutoff) {
		keep++
	}
	l.calls = append(l.calls[:0], l.calls[keep:]...)

	if len(l.calls) < l.max {
		l.calls = append(l.calls, now)
		return 0, true
	}
	// The oldest call leaves the window one full window after it was made.
	// A small margin avoids waking a hair early and looping.
	wait := l.calls[0].Add(l.window).Sub(now) + time.Millisecond
	if wait < 0 {
		wait = 0
	}
	return wait, false
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
