package stocksync_test

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
	"github.com/gluzo/integration-gateway/app/workflow/stocksync"
)

type stubLister struct {
	locations []routing.Location
	err       error
}

func (s stubLister) ActiveLocations(context.Context) ([]routing.Location, error) {
	return s.locations, s.err
}

func location(name, vendorRef, originRef, vendorPlatform string) routing.Location {
	return routing.Location{
		IntegrationID:   uuid.New(),
		IntegrationName: name,
		OriginPlatform:  "easyecom",
		VendorPlatform:  vendorPlatform,
		VendorReference: vendorRef,
		OriginReference: originRef,
	}
}

func window() scheduler.Window {
	until := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	return scheduler.Window{Since: until.Add(-15 * time.Minute), Until: until}
}

func jobsFor(t *testing.T, name string, lister stocksync.LocationLister, opts stocksync.ScheduleOptions) []queue.Job {
	t.Helper()
	for _, j := range stocksync.Jobs(lister, opts) {
		if j.Name != name {
			continue
		}
		built, err := j.Build(context.Background(), window())
		if err != nil {
			t.Fatalf("build %s: %v", name, err)
		}
		return built
	}
	t.Fatalf("no job named %s", name)
	return nil
}

func TestJobsFanOutOnePerLocation(t *testing.T) {
	lister := stubLister{locations: []routing.Location{
		location("easyecom-vinculum", "DEL", "bcpl-key", "vinculum"),
		location("easyecom-vinculum", "BLR", "bcpl-key", "vinculum"),
	}}

	built := jobsFor(t, stocksync.JobIncremental, lister, stocksync.ScheduleOptions{})
	if len(built) != 2 {
		t.Fatalf("built %d jobs, want one per location", len(built))
	}

	// Fanning out here rather than inside the workflow means one location's
	// vendor outage does not fail every other location's sweep.
	seen := map[string]bool{}
	for _, j := range built {
		if j.Workflow != stocksync.Name {
			t.Errorf("job workflow = %q, want %s", j.Workflow, stocksync.Name)
		}
		if err := j.Validate(); err != nil {
			t.Errorf("job does not validate: %v", err)
		}
		var ev event.Event
		if err := json.Unmarshal(j.Payload, &ev); err != nil {
			t.Fatalf("payload is not an event: %v", err)
		}
		if err := event.Validate(ev); err != nil {
			t.Errorf("the worker would discard this event: %v", err)
		}
		var sweep stocksync.Sweep
		if err := json.Unmarshal(ev.Payload, &sweep); err != nil {
			t.Fatalf("event payload is not a sweep: %v", err)
		}
		if err := sweep.Validate(); err != nil {
			t.Errorf("sweep is incomplete: %v", err)
		}
		if sweep.Full {
			t.Error("the incremental job produced a full sweep")
		}
		seen[sweep.VendorReference] = true
	}
	if !seen["DEL"] || !seen["BLR"] {
		t.Errorf("locations swept = %v, want both", seen)
	}
}

func TestEachLocationGetsItsOwnIdempotencyKey(t *testing.T) {
	lister := stubLister{locations: []routing.Location{
		location("easyecom-vinculum", "DEL", "bcpl-key", "vinculum"),
		location("easyecom-vinculum", "BLR", "bcpl-key", "vinculum"),
	}}
	built := jobsFor(t, stocksync.JobIncremental, lister, stocksync.ScheduleOptions{})

	// A shared key would have the idempotency store swallow every location
	// but the first, and the fan-out would silently become a single sweep.
	if built[0].IdempotencyKey == built[1].IdempotencyKey {
		t.Fatalf("both locations carry %q", built[0].IdempotencyKey)
	}
}

// The same window republished after a failure must produce the same keys, so
// the repeat is recognised rather than swept twice.
func TestARepublishedWindowKeepsItsKeys(t *testing.T) {
	lister := stubLister{locations: []routing.Location{location("easyecom-vinculum", "DEL", "bcpl-key", "vinculum")}}
	first := jobsFor(t, stocksync.JobIncremental, lister, stocksync.ScheduleOptions{})
	second := jobsFor(t, stocksync.JobIncremental, lister, stocksync.ScheduleOptions{})
	if first[0].IdempotencyKey != second[0].IdempotencyKey {
		t.Errorf("keys differ across a republish: %q vs %q", first[0].IdempotencyKey, second[0].IdempotencyKey)
	}
}

// The two jobs are separate names so they have separate watermarks: a
// failing full push must not hold up the frequent incremental one.
func TestTheFullJobIsSeparateAndMarksItsSweepsFull(t *testing.T) {
	lister := stubLister{locations: []routing.Location{location("easyecom-vinculum", "DEL", "bcpl-key", "vinculum")}}

	jobs := stocksync.Jobs(lister, stocksync.ScheduleOptions{Interval: time.Minute, FullInterval: time.Hour})
	if len(jobs) != 2 {
		t.Fatalf("got %d jobs, want an incremental and a full one", len(jobs))
	}
	if jobs[0].Name == jobs[1].Name {
		t.Fatal("both jobs share a name and would share a watermark")
	}
	if jobs[0].Interval == jobs[1].Interval {
		t.Error("both jobs run on the same interval")
	}

	built := jobsFor(t, stocksync.JobFull, lister, stocksync.ScheduleOptions{})
	var ev event.Event
	_ = json.Unmarshal(built[0].Payload, &ev)
	var sweep stocksync.Sweep
	_ = json.Unmarshal(ev.Payload, &sweep)
	if !sweep.Full {
		t.Error("the full job did not mark its sweep full")
	}

	// Different keys from the incremental job, or one would suppress the
	// other when both fall due at the same moment.
	incremental := jobsFor(t, stocksync.JobIncremental, lister, stocksync.ScheduleOptions{})
	if built[0].IdempotencyKey == incremental[0].IdempotencyKey {
		t.Error("the full and incremental sweeps share an idempotency key")
	}
}

func TestJobsSkipOtherVendorsWhenLimited(t *testing.T) {
	lister := stubLister{locations: []routing.Location{
		location("easyecom-vinculum", "DEL", "bcpl-key", "vinculum"),
		location("easyecom-other", "XYZ", "other-key", "someone-else"),
	}}
	built := jobsFor(t, stocksync.JobIncremental, lister, stocksync.ScheduleOptions{VendorPlatform: "vinculum"})
	if len(built) != 1 {
		t.Fatalf("built %d jobs, want only the vinculum location", len(built))
	}
	if built[0].Platform != "vinculum" {
		t.Errorf("platform = %q", built[0].Platform)
	}
}

func TestNoLocationsProducesNoJobs(t *testing.T) {
	built := jobsFor(t, stocksync.JobIncremental, stubLister{}, stocksync.ScheduleOptions{})
	if len(built) != 0 {
		t.Errorf("built %d jobs with nothing configured", len(built))
	}
}

// A listing failure must fail the tick rather than publish an empty sweep:
// "no locations" and "could not read the locations" are different, and
// treating the second as the first would silently stop all stock updates.
func TestAListingFailureFailsTheTick(t *testing.T) {
	jobs := stocksync.Jobs(stubLister{err: errors.New("database down")}, stocksync.ScheduleOptions{})
	if _, err := jobs[0].Build(context.Background(), window()); err == nil {
		t.Fatal("expected an error")
	}
}
