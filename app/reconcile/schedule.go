package reconcile

import (
	"context"
	"time"

	"github.com/gluzo/integration-gateway/app/queue"
	"github.com/gluzo/integration-gateway/app/scheduler"
)

// JobName as it appears in scheduler_state, the Redis lock and the log.
const JobName = "reconcile"

// DefaultInterval is how often the sweep runs by default.
const DefaultInterval = time.Hour

// Job builds the scheduler job that runs a reconciliation sweep.
//
// The scan is performed inside Build rather than published as a queue job,
// which is the opposite of how stock and dispatch work, and deliberately so.
// Those publish because the work is per location, may be slow, and benefits
// from the worker's retry and resume. Reconciliation reads local state,
// finishes in milliseconds, and produces a report rather than a change —
// there is nothing for a worker to retry and nothing to resume.
//
// It still goes through the scheduler so that it fires once across replicas.
// Three replicas each raising the same alert every hour would train an
// operator to ignore it.
func Job(alerter *Alerter, interval time.Duration) scheduler.Job {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return scheduler.Job{
		Name:     JobName,
		Interval: interval,
		// The sweep looks at the present state of every active run, not at
		// a period, so the window it is handed is irrelevant to it.
		InitialLookback: interval,
		Build: func(ctx context.Context, _ scheduler.Window) ([]queue.Job, error) {
			if _, err := alerter.Run(ctx); err != nil {
				return nil, err
			}
			return nil, nil
		},
	}
}
