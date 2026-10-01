package vinculum

import (
	"context"
	"time"
)

// SetOrderLimiterClockForTest replaces the order limiter's clock and sleeper.
//
// The limiter is deliberately unexported — it is an implementation detail of
// the client — but a test that waited on the real clock would take five
// minutes to prove a five-minute window.
func SetOrderLimiterClockForTest(c *Client, now func() time.Time, sleep func(context.Context, time.Duration) error) {
	if c.orderLimiter == nil {
		return
	}
	c.orderLimiter.mu.Lock()
	defer c.orderLimiter.mu.Unlock()
	if now != nil {
		c.orderLimiter.now = now
	}
	if sleep != nil {
		c.orderLimiter.sleep = sleep
	}
}
