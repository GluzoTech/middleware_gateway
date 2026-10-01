package shipmentsync

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

// JobName as it appears in scheduler_state, the Redis lock and the log.
const JobName = "shipment-sync"

// LocationLister supplies the vendor locations to sweep.
type LocationLister interface {
	ActiveLocations(ctx context.Context) ([]routing.Location, error)
}

// ScheduleOptions tunes the scheduled job.
type ScheduleOptions struct {
	// Interval is how often dispatch records are read.
	Interval time.Duration
	// MaxWindow caps how much time one run may cover, so a backlog after an
	// outage drains in chunks rather than in one enormous request.
	MaxWindow time.Duration
	// Lookback is the period the first run covers.
	Lookback time.Duration
	// VendorPlatform limits the sweep to one vendor's locations.
	VendorPlatform string
}

// Job builds the scheduler job for dispatch synchronisation.
//
// One job, unlike stock's two. A stock sweep needs a periodic full push
// because it compares against a record that can drift; a dispatch sweep reads
// a period bounded by its watermark, so a missed run is closed by widening
// the next window rather than by ignoring the record.
func Job(lister LocationLister, opts ScheduleOptions) scheduler.Job {
	if opts.Interval <= 0 {
		opts.Interval = 15 * time.Minute
	}
	if opts.MaxWindow <= 0 {
		opts.MaxWindow = 24 * time.Hour
	}
	if opts.Lookback <= 0 {
		opts.Lookback = opts.Interval
	}
	return scheduler.Job{
		Name:            JobName,
		Interval:        opts.Interval,
		MaxWindow:       opts.MaxWindow,
		InitialLookback: opts.Lookback,
		Build:           build(lister, opts.VendorPlatform),
	}
}

func build(lister LocationLister, vendorPlatform string) func(context.Context, scheduler.Window) ([]queue.Job, error) {
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
				// The scheduler's window is the sweep's period. This is the
				// case the watermark exists for: a run missed during a
				// deployment leaves a real gap in dispatch history, and the
				// next window covers it rather than starting from now.
				From: w.Since,
				To:   w.Until,
			}
			payload, err := json.Marshal(sweep)
			if err != nil {
				return nil, fmt.Errorf("encode sweep for %s/%s: %w", loc.IntegrationName, loc.VendorReference, err)
			}

			id := correlation.New()
			key := fmt.Sprintf("scheduler:%s:%d:%s:%s", JobName, w.Until.UTC().Unix(), loc.IntegrationID, loc.VendorReference)
			ev := event.Event{
				Platform:       loc.VendorPlatform,
				EventType:      event.ShipmentSyncDue,
				CorrelationID:  id,
				IntegrationID:  sweep.IntegrationID,
				RoutingKey:     event.RoutingKey{Type: "vendor_location", Value: loc.VendorReference},
				IdempotencyKey: key,
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
				EventType:      event.ShipmentSyncDue,
				Platform:       loc.VendorPlatform,
				IntegrationID:  sweep.IntegrationID,
				IdempotencyKey: key,
				Payload:        body,
			})
		}
		return jobs, nil
	}
}
