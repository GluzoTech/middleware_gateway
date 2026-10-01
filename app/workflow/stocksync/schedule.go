package stocksync

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gluzo/integration-gateway/app/correlation"
	"github.com/gluzo/integration-gateway/app/event"
	"github.com/gluzo/integration-gateway/app/queue"
	"github.com/gluzo/integration-gateway/app/routing"
	"github.com/gluzo/integration-gateway/app/scheduler"
)

// Job names as they appear in scheduler_state, the Redis lock and the log.
// Renaming one starts a new watermark.
const (
	JobIncremental = "stock-sync"
	JobFull        = "stock-sync-full"
)

// LocationLister supplies the vendor locations to sweep.
type LocationLister interface {
	ActiveLocations(ctx context.Context) ([]routing.Location, error)
}

// ScheduleOptions tunes the two scheduled jobs.
type ScheduleOptions struct {
	// Interval is how often the incremental sweep runs.
	Interval time.Duration
	// FullInterval is how often every SKU is pushed regardless of what was
	// last recorded. This is the repair path for drift, so it is a period,
	// not an operator action: drift nobody looks for is drift that persists.
	FullInterval time.Duration
	// VendorPlatform limits the sweep to one vendor's locations. Empty
	// sweeps every vendor that has a registered stock provider.
	VendorPlatform string
}

// Jobs builds the scheduler jobs for stock synchronisation.
//
// Two jobs rather than one flag on a schedule, because they are genuinely
// different work with different costs: the incremental sweep runs often and
// usually pushes nothing, while the full push runs rarely and writes
// everything. Separate names mean separate watermarks, so a failing full push
// does not hold up the frequent one.
//
// Each returns one queue job per distinct vendor location. Fanning out here
// rather than inside the workflow keeps one location's vendor outage from
// failing every other location's sweep.
func Jobs(lister LocationLister, opts ScheduleOptions) []scheduler.Job {
	if opts.Interval <= 0 {
		opts.Interval = 15 * time.Minute
	}
	if opts.FullInterval <= 0 {
		opts.FullInterval = 24 * time.Hour
	}
	return []scheduler.Job{
		{
			Name:     JobIncremental,
			Interval: opts.Interval,
			// A stock sweep reads the vendor's current position, not a
			// period of history, so a missed run is not a gap to catch up
			// on — the next sweep reads the present and is complete. The
			// watermark still advances so the schedule stays honest.
			InitialLookback: opts.Interval,
			Build:           build(lister, opts.VendorPlatform, false),
		},
		{
			Name:            JobFull,
			Interval:        opts.FullInterval,
			InitialLookback: opts.FullInterval,
			Build:           build(lister, opts.VendorPlatform, true),
		},
	}
}

func build(lister LocationLister, vendorPlatform string, full bool) func(context.Context, scheduler.Window) ([]queue.Job, error) {
	return func(ctx context.Context, w scheduler.Window) ([]queue.Job, error) {
		locations, err := lister.ActiveLocations(ctx)
		if err != nil {
			return nil, fmt.Errorf("list active vendor locations: %w", err)
		}

		jobs := make([]queue.Job, 0, len(locations))
		for _, loc := range locations {
			if vendorPlatform != "" && loc.VendorPlatform != vendorPlatform {
				continue
			}
			sweep := Sweep{
				IntegrationID:   loc.IntegrationID.String(),
				IntegrationName: loc.IntegrationName,
				OriginPlatform:  loc.OriginPlatform,
				VendorPlatform:  loc.VendorPlatform,
				VendorReference: loc.VendorReference,
				OriginReference: loc.OriginReference,
				Full:            full,
			}
			payload, err := json.Marshal(sweep)
			if err != nil {
				return nil, fmt.Errorf("encode sweep for %s/%s: %w", loc.IntegrationName, loc.VendorReference, err)
			}

			id := correlation.New()
			ev := event.Event{
				Platform:      loc.VendorPlatform,
				EventType:     event.StockSyncDue,
				CorrelationID: id,
				IntegrationID: sweep.IntegrationID,
				RoutingKey:    event.RoutingKey{Type: "vendor_location", Value: loc.VendorReference},
				// One key per job, window end and location, so a window
				// republished after a partial failure is recognised as the
				// same work rather than run twice.
				IdempotencyKey: idempotencyKey(full, loc, w),
				ReceivedAt:     w.Until,
				Payload:        payload,
			}
			body, err := json.Marshal(ev)
			if err != nil {
				return nil, fmt.Errorf("encode event for %s/%s: %w", loc.IntegrationName, loc.VendorReference, err)
			}

			jobs = append(jobs, queue.Job{
				ID:             id,
				CorrelationID:  id,
				Workflow:       Name,
				EventType:      event.StockSyncDue,
				Platform:       loc.VendorPlatform,
				IntegrationID:  sweep.IntegrationID,
				IdempotencyKey: ev.IdempotencyKey,
				Payload:        body,
			})
		}
		return jobs, nil
	}
}

func idempotencyKey(full bool, loc routing.Location, w scheduler.Window) string {
	name := JobIncremental
	if full {
		name = JobFull
	}
	return fmt.Sprintf("scheduler:%s:%d:%s:%s", name, w.Until.UTC().Unix(), loc.IntegrationID, loc.VendorReference)
}
