package scheduler_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/gluzo/integration-gateway/app/queue"
	"github.com/gluzo/integration-gateway/app/scheduler"
)

// clock is a manually advanced clock. Every test in this file moves time
// explicitly: a scheduler test that waits for real time is a slow test that
// fails on a loaded machine.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock {
	return &clock{t: time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)}
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// recorder captures published jobs and can be made to fail.
type recorder struct {
	mu        sync.Mutex
	published []queue.Job
	failAfter int // publish this many, then fail; zero means never fail
	calls     int
}

func (r *recorder) Publish(_ context.Context, job queue.Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.failAfter > 0 && r.calls > r.failAfter {
		return errors.New("queue unavailable")
	}
	r.published = append(r.published, job)
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.published)
}

func (r *recorder) jobs() []queue.Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]queue.Job, len(r.published))
	copy(out, r.published)
	return out
}

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// oneJob builds a single queue job and records the window it was given.
func oneJob(windows *[]scheduler.Window, mu *sync.Mutex) func(context.Context, scheduler.Window) ([]queue.Job, error) {
	return func(_ context.Context, w scheduler.Window) ([]queue.Job, error) {
		mu.Lock()
		*windows = append(*windows, w)
		mu.Unlock()
		return []queue.Job{{Workflow: "STOCK_SYNC", EventType: "SCHEDULED"}}, nil
	}
}

func newScheduler(t *testing.T, store scheduler.Store, locker scheduler.Locker, pub queue.Publisher, c *clock) *scheduler.Scheduler {
	t.Helper()
	s, err := scheduler.New(store, locker, pub,
		scheduler.WithClock(c.now),
		scheduler.WithLogger(quiet()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// Exit criterion: two replicas fire a tick exactly once.
func TestTwoReplicasFireATickExactlyOnce(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	store := scheduler.NewMemoryStore() // shared, as PostgreSQL is shared
	locker := scheduler.NewMemoryLocker()
	locker.SetClock(c.now)
	pub := &recorder{}

	var mu sync.Mutex
	var windows []scheduler.Window
	job := scheduler.Job{
		Name:     "stock-sync",
		Interval: 15 * time.Minute,
		Build:    oneJob(&windows, &mu),
	}

	replicaA := newScheduler(t, store, locker, pub, c)
	replicaB := newScheduler(t, store, locker, pub, c)
	for _, s := range []*scheduler.Scheduler{replicaA, replicaB} {
		if err := s.Register(ctx, job); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}

	// Not due yet: Register schedules the first run one interval out so a
	// deployment does not fire every job at once.
	if fired := replicaA.RunDue(ctx) + replicaB.RunDue(ctx); fired != 0 {
		t.Fatalf("%d runs fired before the first interval elapsed", fired)
	}

	c.advance(15 * time.Minute)

	// Both replicas poll at the same instant, concurrently, several times
	// over. Exactly one tick must result.
	var wg sync.WaitGroup
	var fired [2]int
	for i, s := range []*scheduler.Scheduler{replicaA, replicaB} {
		wg.Add(1)
		go func(i int, s *scheduler.Scheduler) {
			defer wg.Done()
			for n := 0; n < 5; n++ {
				fired[i] += s.RunDue(ctx)
			}
		}(i, s)
	}
	wg.Wait()

	if total := fired[0] + fired[1]; total != 1 {
		t.Errorf("%d runs fired (A=%d, B=%d), want exactly 1", total, fired[0], fired[1])
	}
	if pub.count() != 1 {
		t.Errorf("published %d jobs, want 1", pub.count())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(windows) != 1 {
		t.Fatalf("built %d windows, want 1", len(windows))
	}
}

// Exit criterion: a process killed mid-tick resumes from the watermark
// rather than skipping the work.
func TestAFailedRunLeavesTheWatermarkAndTheNextRunCoversTheWholeGap(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	store := scheduler.NewMemoryStore()
	pub := &recorder{}

	var mu sync.Mutex
	var windows []scheduler.Window
	fail := true
	job := scheduler.Job{
		Name:            "shipment-sync",
		Interval:        10 * time.Minute,
		InitialLookback: 10 * time.Minute,
		Build: func(_ context.Context, w scheduler.Window) ([]queue.Job, error) {
			mu.Lock()
			windows = append(windows, w)
			mu.Unlock()
			if fail {
				// Stands in for the process dying mid-tick: the claim
				// happened, the watermark did not advance.
				return nil, errors.New("vendor unreachable")
			}
			return []queue.Job{{Workflow: "SHIPMENT_SYNC", EventType: "SCHEDULED"}}, nil
		},
	}

	s := newScheduler(t, store, scheduler.NopLocker{}, pub, c)
	if err := s.Register(ctx, job); err != nil {
		t.Fatalf("Register: %v", err)
	}

	before, err := store.Get(ctx, job.Name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	c.advance(10 * time.Minute)
	if fired := s.RunDue(ctx); fired != 0 {
		t.Fatalf("a failed run must not count as fired, got %d", fired)
	}

	state, err := store.Get(ctx, job.Name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !state.Watermark.Equal(before.Watermark) {
		t.Errorf("watermark moved from %s to %s despite the failure", before.Watermark, state.Watermark)
	}
	if state.LastFiredAt.IsZero() {
		t.Error("the attempt should still be recorded in last_fired_at")
	}

	// Two more intervals pass with the process down.
	fail = false
	c.advance(20 * time.Minute)
	if fired := s.RunDue(ctx); fired != 1 {
		t.Fatalf("expected the next run to fire, got %d", fired)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(windows) != 2 {
		t.Fatalf("built %d windows, want 2", len(windows))
	}
	// The second window must start where the first did, not where the
	// process woke up: the 20 minutes of outage are covered, not skipped.
	if !windows[1].Since.Equal(windows[0].Since) {
		t.Errorf("second window starts at %s, want the unchanged watermark %s",
			windows[1].Since, windows[0].Since)
	}
	if !windows[1].Until.Equal(c.now()) {
		t.Errorf("second window ends at %s, want the present %s", windows[1].Until, c.now())
	}
	if got, want := windows[1].Duration(), windows[0].Duration()+20*time.Minute; got != want {
		t.Errorf("second window covers %s, want %s: the first window plus the outage", got, want)
	}
}

func TestSuccessAdvancesTheWatermarkToTheWindowEnd(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	store := scheduler.NewMemoryStore()
	pub := &recorder{}

	var mu sync.Mutex
	var windows []scheduler.Window
	job := scheduler.Job{Name: "stock-sync", Interval: 5 * time.Minute, Build: oneJob(&windows, &mu)}

	s := newScheduler(t, store, nil, pub, c)
	if err := s.Register(ctx, job); err != nil {
		t.Fatalf("Register: %v", err)
	}

	c.advance(5 * time.Minute)
	s.RunDue(ctx)
	first := c.now()

	state, err := store.Get(ctx, job.Name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !state.Watermark.Equal(first) {
		t.Errorf("watermark = %s, want the window end %s", state.Watermark, first)
	}

	c.advance(5 * time.Minute)
	s.RunDue(ctx)

	mu.Lock()
	defer mu.Unlock()
	if len(windows) != 2 {
		t.Fatalf("built %d windows, want 2", len(windows))
	}
	// Consecutive windows must abut: no gap loses work, no overlap repeats it.
	if !windows[1].Since.Equal(windows[0].Until) {
		t.Errorf("window 2 starts at %s but window 1 ended at %s", windows[1].Since, windows[0].Until)
	}
}

func TestALongBacklogIsCoveredInCappedChunks(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	store := scheduler.NewMemoryStore()
	pub := &recorder{}

	var mu sync.Mutex
	var windows []scheduler.Window
	job := scheduler.Job{
		Name:            "shipment-sync",
		Interval:        time.Hour,
		MaxWindow:       6 * time.Hour,
		InitialLookback: time.Hour,
		Build:           oneJob(&windows, &mu),
	}

	s := newScheduler(t, store, nil, pub, c)
	if err := s.Register(ctx, job); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// The process is down for a day, then comes back.
	c.advance(25 * time.Hour)

	// Each pass covers at most MaxWindow and leaves the job due at once, so
	// the backlog drains faster than it accumulated instead of in real time.
	passes := 0
	for n := 0; n < 20; n++ {
		if s.RunDue(ctx) == 0 {
			break
		}
		passes++
	}

	mu.Lock()
	defer mu.Unlock()
	if passes < 2 {
		t.Fatalf("the backlog drained in %d pass(es); it should take several capped chunks", passes)
	}
	for i, w := range windows {
		if w.Duration() > job.MaxWindow {
			t.Errorf("window %d covers %s, over the %s cap", i, w.Duration(), job.MaxWindow)
		}
		if i > 0 && !w.Since.Equal(windows[i-1].Until) {
			t.Errorf("window %d starts at %s but window %d ended at %s", i, w.Since, i-1, windows[i-1].Until)
		}
	}
	// Caught up: the last window reaches the present.
	last := windows[len(windows)-1]
	if !last.Until.Equal(c.now()) {
		t.Errorf("last window ends at %s, want the present %s", last.Until, c.now())
	}
}

func TestALockedJobIsSkippedNotQueued(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	store := scheduler.NewMemoryStore()
	locker := scheduler.NewMemoryLocker()
	locker.SetClock(c.now)
	pub := &recorder{}

	var mu sync.Mutex
	var windows []scheduler.Window
	job := scheduler.Job{Name: "stock-sync", Interval: time.Minute, Build: oneJob(&windows, &mu)}

	s := newScheduler(t, store, locker, pub, c)
	if err := s.Register(ctx, job); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Another replica is mid-run and holds the lock.
	release, ok, err := locker.Acquire(ctx, job.Name, 10*time.Minute)
	if err != nil || !ok {
		t.Fatalf("Acquire: ok=%v err=%v", ok, err)
	}

	c.advance(time.Minute)
	if fired := s.RunDue(ctx); fired != 0 {
		t.Fatalf("the tick should have been skipped, got %d", fired)
	}
	if pub.count() != 0 {
		t.Errorf("published %d jobs while another replica held the lock", pub.count())
	}

	// Once the other replica finishes, the next due tick proceeds, and its
	// window still starts at the untouched watermark.
	release(ctx)
	c.advance(time.Minute)
	if fired := s.RunDue(ctx); fired != 1 {
		t.Fatalf("expected the next tick to fire, got %d", fired)
	}
}

func TestPublishFailureLeavesTheWatermark(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	store := scheduler.NewMemoryStore()
	pub := &recorder{failAfter: 1} // the second job of the batch fails

	job := scheduler.Job{
		Name:     "stock-sync",
		Interval: time.Minute,
		Build: func(context.Context, scheduler.Window) ([]queue.Job, error) {
			return []queue.Job{
				{Workflow: "STOCK_SYNC", EventType: "SCHEDULED", IntegrationID: "a"},
				{Workflow: "STOCK_SYNC", EventType: "SCHEDULED", IntegrationID: "b"},
			}, nil
		},
	}

	s := newScheduler(t, store, nil, pub, c)
	if err := s.Register(ctx, job); err != nil {
		t.Fatalf("Register: %v", err)
	}
	before, err := store.Get(ctx, job.Name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	c.advance(time.Minute)
	s.RunDue(ctx)

	state, err := store.Get(ctx, job.Name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !state.Watermark.Equal(before.Watermark) {
		t.Error("a partial publish must not advance the watermark")
	}
	if pub.count() != 1 {
		t.Errorf("published %d jobs, want the one that succeeded", pub.count())
	}
}

func TestStampedJobsCarryDistinctIdempotencyKeys(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	store := scheduler.NewMemoryStore()
	pub := &recorder{}

	job := scheduler.Job{
		Name:     "stock-sync",
		Interval: time.Minute,
		Build: func(context.Context, scheduler.Window) ([]queue.Job, error) {
			return []queue.Job{
				{Workflow: "STOCK_SYNC", EventType: "SCHEDULED", IntegrationID: "int-a"},
				{Workflow: "STOCK_SYNC", EventType: "SCHEDULED", IntegrationID: "int-b"},
			}, nil
		},
	}

	s := newScheduler(t, store, nil, pub, c)
	if err := s.Register(ctx, job); err != nil {
		t.Fatalf("Register: %v", err)
	}
	c.advance(time.Minute)
	s.RunDue(ctx)

	jobs := pub.jobs()
	if len(jobs) != 2 {
		t.Fatalf("published %d jobs, want 2", len(jobs))
	}
	if jobs[0].IdempotencyKey == jobs[1].IdempotencyKey {
		t.Errorf("both jobs carry %q; a fan-out would be deduplicated down to one",
			jobs[0].IdempotencyKey)
	}
	for i, j := range jobs {
		if j.ID == "" || j.CorrelationID == "" || j.EnqueuedAt.IsZero() {
			t.Errorf("job %d was published unstamped: %+v", i, j)
		}
		if err := j.Validate(); err != nil {
			t.Errorf("job %d does not validate: %v", i, err)
		}
	}
}

// A republished window must produce the same keys, so the idempotency store
// recognises the repeat rather than running the sweep twice.
func TestARepublishedWindowKeepsItsIdempotencyKeys(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	store := scheduler.NewMemoryStore()

	build := func(context.Context, scheduler.Window) ([]queue.Job, error) {
		return []queue.Job{{Workflow: "STOCK_SYNC", EventType: "SCHEDULED", IntegrationID: "int-a"}}, nil
	}
	job := scheduler.Job{Name: "stock-sync", Interval: time.Minute, Build: build}

	pub := &recorder{}
	s := newScheduler(t, store, nil, pub, c)
	if err := s.Register(ctx, job); err != nil {
		t.Fatalf("Register: %v", err)
	}
	before, err := store.Get(ctx, job.Name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	c.advance(time.Minute)
	s.RunDue(ctx)
	first := pub.jobs()[0].IdempotencyKey

	// Rewind the watermark as a failed run would have left it, and fire the
	// same window again.
	if err := store.Complete(ctx, job.Name, before.Watermark, c.now(), c.now()); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	s.RunDue(ctx)

	jobs := pub.jobs()
	if len(jobs) != 2 {
		t.Fatalf("published %d jobs, want 2", len(jobs))
	}
	if jobs[1].IdempotencyKey != first {
		t.Errorf("republished key %q differs from %q; the repeat would run twice",
			jobs[1].IdempotencyKey, first)
	}
}

func TestRegisterRejectsAnUnusableJob(t *testing.T) {
	ctx := context.Background()
	s := newScheduler(t, scheduler.NewMemoryStore(), nil, &recorder{}, newClock())

	build := func(context.Context, scheduler.Window) ([]queue.Job, error) { return nil, nil }
	tests := []struct {
		name string
		job  scheduler.Job
	}{
		{"no name", scheduler.Job{Interval: time.Minute, Build: build}},
		{"no interval", scheduler.Job{Name: "x", Build: build}},
		{"negative interval", scheduler.Job{Name: "x", Interval: -time.Minute, Build: build}},
		{"no build function", scheduler.Job{Name: "x", Interval: time.Minute}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.Register(ctx, tc.job); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestRegisterRejectsADuplicateName(t *testing.T) {
	ctx := context.Background()
	s := newScheduler(t, scheduler.NewMemoryStore(), nil, &recorder{}, newClock())
	job := scheduler.Job{
		Name:     "stock-sync",
		Interval: time.Minute,
		Build:    func(context.Context, scheduler.Window) ([]queue.Job, error) { return nil, nil },
	}
	if err := s.Register(ctx, job); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := s.Register(ctx, job); err == nil {
		t.Error("a second job under the same name would share its watermark")
	}
}

// Re-registering after a restart must not reset the watermark, or every
// deployment would re-sweep from scratch.
func TestRegisterKeepsAnExistingWatermark(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	store := scheduler.NewMemoryStore()
	job := scheduler.Job{
		Name:     "stock-sync",
		Interval: time.Minute,
		Build: func(context.Context, scheduler.Window) ([]queue.Job, error) {
			return []queue.Job{{Workflow: "STOCK_SYNC", EventType: "SCHEDULED"}}, nil
		},
	}

	first := newScheduler(t, store, nil, &recorder{}, c)
	if err := first.Register(ctx, job); err != nil {
		t.Fatalf("Register: %v", err)
	}
	c.advance(time.Minute)
	first.RunDue(ctx)
	before, err := store.Get(ctx, job.Name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Restart.
	second := newScheduler(t, store, nil, &recorder{}, c)
	if err := second.Register(ctx, job); err != nil {
		t.Fatalf("Register after restart: %v", err)
	}
	after, err := store.Get(ctx, job.Name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !after.Watermark.Equal(before.Watermark) {
		t.Errorf("watermark moved from %s to %s across a restart", before.Watermark, after.Watermark)
	}
}

func TestRunReturnsImmediatelyWithNoJobs(t *testing.T) {
	s := newScheduler(t, scheduler.NewMemoryStore(), nil, &recorder{}, newClock())
	done := make(chan struct{})
	go func() {
		s.Run(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run blocked although no job is registered")
	}
}

func TestRunStopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := newClock()
	s, err := scheduler.New(scheduler.NewMemoryStore(), nil, &recorder{},
		scheduler.WithClock(c.now),
		scheduler.WithPollInterval(10*time.Millisecond),
		scheduler.WithLogger(quiet()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Register(ctx, scheduler.Job{
		Name:     "stock-sync",
		Interval: time.Hour,
		Build:    func(context.Context, scheduler.Window) ([]queue.Job, error) { return nil, nil },
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	done := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop when its context was cancelled")
	}
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	if _, err := scheduler.New(nil, nil, &recorder{}); err == nil {
		t.Error("a scheduler without a store must not be built")
	}
	if _, err := scheduler.New(scheduler.NewMemoryStore(), nil, nil); err == nil {
		t.Error("a scheduler without a publisher must not be built")
	}
}
