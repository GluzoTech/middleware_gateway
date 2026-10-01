package shipmentsync_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gluzo/integration-gateway/app/event"
	"github.com/gluzo/integration-gateway/app/queue"
	"github.com/gluzo/integration-gateway/app/routing"
	"github.com/gluzo/integration-gateway/app/scheduler"
	"github.com/gluzo/integration-gateway/app/workflow/shipmentsync"
)

type stubLister struct {
	locations []routing.Location
	err       error
}

func (s stubLister) ActiveLocations(context.Context) ([]routing.Location, error) {
	return s.locations, s.err
}

func location(vendorRef, vendorPlatform string) routing.Location {
	return routing.Location{
		IntegrationID:   uuid.New(),
		IntegrationName: "easyecom-vinculum",
		OriginPlatform:  "easyecom",
		VendorPlatform:  vendorPlatform,
		VendorReference: vendorRef,
		OriginReference: "bcpl-key",
	}
}

func testWindow() scheduler.Window {
	until := time.Date(2026, 9, 30, 14, 0, 0, 0, time.UTC)
	return scheduler.Window{Since: until.Add(-15 * time.Minute), Until: until}
}

func build(t *testing.T, lister shipmentsync.LocationLister, opts shipmentsync.ScheduleOptions) []queue.Job {
	t.Helper()
	jobs, err := shipmentsync.Job(lister, opts).Build(context.Background(), testWindow())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return jobs
}

func decodeSweep(t *testing.T, j queue.Job) shipmentsync.Sweep {
	t.Helper()
	var ev event.Event
	if err := json.Unmarshal(j.Payload, &ev); err != nil {
		t.Fatalf("payload is not an event: %v", err)
	}
	if err := event.Validate(ev); err != nil {
		t.Fatalf("the worker would discard this event: %v", err)
	}
	var sweep shipmentsync.Sweep
	if err := json.Unmarshal(ev.Payload, &sweep); err != nil {
		t.Fatalf("event payload is not a sweep: %v", err)
	}
	if err := sweep.Validate(); err != nil {
		t.Fatalf("sweep is incomplete: %v", err)
	}
	return sweep
}

// The scheduler's window becomes the sweep's period. This is the case the
// watermark exists for: a run missed during a deployment leaves a real gap in
// dispatch history, and the next window must cover it rather than start now.
func TestTheSchedulerWindowBecomesTheSweepPeriod(t *testing.T) {
	jobs := build(t, stubLister{locations: []routing.Location{location("DEL", "vinculum")}},
		shipmentsync.ScheduleOptions{})
	if len(jobs) != 1 {
		t.Fatalf("built %d jobs, want 1", len(jobs))
	}

	sweep := decodeSweep(t, jobs[0])
	w := testWindow()
	if !sweep.From.Equal(w.Since) || !sweep.To.Equal(w.Until) {
		t.Errorf("sweep period = %s..%s, want the window %s", sweep.From, sweep.To, w)
	}
	if jobs[0].Workflow != shipmentsync.Name {
		t.Errorf("workflow = %q", jobs[0].Workflow)
	}
}

func TestJobsFanOutOnePerLocation(t *testing.T) {
	jobs := build(t, stubLister{locations: []routing.Location{
		location("DEL", "vinculum"),
		location("BLR", "vinculum"),
	}}, shipmentsync.ScheduleOptions{})

	if len(jobs) != 2 {
		t.Fatalf("built %d jobs, want one per location", len(jobs))
	}
	// A shared key would have the idempotency store swallow every location
	// but the first, and the fan-out would silently become one sweep.
	if jobs[0].IdempotencyKey == jobs[1].IdempotencyKey {
		t.Errorf("both locations carry %q", jobs[0].IdempotencyKey)
	}
	seen := map[string]bool{}
	for _, j := range jobs {
		if err := j.Validate(); err != nil {
			t.Errorf("job does not validate: %v", err)
		}
		seen[decodeSweep(t, j).VendorReference] = true
	}
	if !seen["DEL"] || !seen["BLR"] {
		t.Errorf("locations swept = %v, want both", seen)
	}
}

func TestARepublishedWindowKeepsItsKeys(t *testing.T) {
	lister := stubLister{locations: []routing.Location{location("DEL", "vinculum")}}
	first := build(t, lister, shipmentsync.ScheduleOptions{})
	second := build(t, lister, shipmentsync.ScheduleOptions{})
	if first[0].IdempotencyKey != second[0].IdempotencyKey {
		t.Errorf("keys differ across a republish: %q vs %q", first[0].IdempotencyKey, second[0].IdempotencyKey)
	}
}

// The job caps its window so a backlog after an outage drains in chunks
// rather than becoming one enormous request to the vendor.
func TestTheJobCapsItsWindow(t *testing.T) {
	job := shipmentsync.Job(stubLister{}, shipmentsync.ScheduleOptions{
		Interval: time.Minute, MaxWindow: 2 * time.Hour,
	})
	if job.MaxWindow != 2*time.Hour {
		t.Errorf("max window = %s, want 2h", job.MaxWindow)
	}
	if job.Name != shipmentsync.JobName {
		t.Errorf("name = %q", job.Name)
	}
}

func TestJobsSkipOtherVendorsWhenLimited(t *testing.T) {
	jobs := build(t, stubLister{locations: []routing.Location{
		location("DEL", "vinculum"),
		location("XYZ", "someone-else"),
	}}, shipmentsync.ScheduleOptions{VendorPlatform: "vinculum"})

	if len(jobs) != 1 {
		t.Fatalf("built %d jobs, want only the vinculum location", len(jobs))
	}
}

// "No locations" and "could not read the locations" are different, and
// treating the second as the first would silently stop all tracking updates.
func TestAListingFailureFailsTheTick(t *testing.T) {
	_, err := shipmentsync.Job(stubLister{err: errors.New("database down")}, shipmentsync.ScheduleOptions{}).
		Build(context.Background(), testWindow())
	if err == nil {
		t.Fatal("expected an error")
	}
}
