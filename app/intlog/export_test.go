package intlog_test

import (
	"time"

	"github.com/gluzo/integration-gateway/app/intlog"
)

// retentionAt returns a copy of r whose clock reports now.
func retentionAt(r *intlog.Retention, now time.Time) *intlog.Retention {
	return r.WithClock(func() time.Time { return now })
}
