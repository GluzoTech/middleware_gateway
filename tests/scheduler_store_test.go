package tests

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/gluzo/integration-gateway/app/queue"
	"github.com/gluzo/integration-gateway/app/scheduler"
)

func TestSchedulerStore(t *testing.T) {
	ctx := context.Background()
	store := scheduler.NewStore(pool)
	name := "stock-sync-" + uuid.NewString()[:8]

	start := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	watermark := start.Add(-time.Hour)
	interval := 15 * time.Minute

	if err := store.Ensure(ctx, name, start.Add(interval), watermark); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	st, err := store.Get(ctx, name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !st.Watermark.Equal(watermark) || !st.NextDueAt.Equal(start.Add(interval)) {
		t.Fatalf("new row = %+v", st)
	}
	if !st.LastFiredAt.IsZero() || !st.LastSuccessAt.IsZero() {
		t.Errorf("a job that has never run must have no fired or success time: %+v", st)
	}

	// Ensure is idempotent and must not reset an existing watermark: a
	// redeploy re-registers every job.
	if err := store.Ensure(ctx, name, start, start); err != nil {
		t.Fatalf("Ensure again: %v", err)
	}
	if again, err := store.Get(ctx, name); err != nil {
		t.Fatalf("Get: %v", err)
	} else if !again.Watermark.Equal(watermark) || !again.NextDueAt.Equal(start.Add(interval)) {
		t.Errorf("re-registering changed the row: %+v", again)
	}

	t.Run("a job that is not due is not claimed", func(t *testing.T) {
		if _, ok, err := store.Claim(ctx, name, start, interval); err != nil {
			t.Fatalf("Claim: %v", err)
		} else if ok {
			t.Error("claimed a job that is not due yet")
		}
	})

	t.Run("claim advances the due time and records the attempt", func(t *testing.T) {
		now := start.Add(interval)
		claimed, ok, err := store.Claim(ctx, name, now, interval)
		if err != nil || !ok {
			t.Fatalf("Claim: ok=%v err=%v", ok, err)
		}
		if !claimed.Watermark.Equal(watermark) {
			t.Errorf("claim returned watermark %s, want %s", claimed.Watermark, watermark)
		}
		if !claimed.NextDueAt.Equal(now.Add(interval)) {
			t.Errorf("next due = %s, want %s", claimed.NextDueAt, now.Add(interval))
		}
		if !claimed.LastFiredAt.Equal(now) {
			t.Errorf("last fired = %s, want %s", claimed.LastFiredAt, now)
		}

		// A second claim at the same instant finds the job no longer due.
		if _, ok, err := store.Claim(ctx, name, now, interval); err != nil {
			t.Fatalf("second Claim: %v", err)
		} else if ok {
			t.Error("the same tick was claimed twice")
		}
	})

	t.Run("complete advances the watermark", func(t *testing.T) {
		now := start.Add(interval)
		if err := store.Complete(ctx, name, now, now.Add(interval), now); err != nil {
			t.Fatalf("Complete: %v", err)
		}
		st, err := store.Get(ctx, name)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !st.Watermark.Equal(now) {
			t.Errorf("watermark = %s, want %s", st.Watermark, now)
		}
		if !st.LastSuccessAt.Equal(now) {
			t.Errorf("last success = %s, want %s", st.LastSuccessAt, now)
		}
	})

	t.Run("unknown jobs are reported, not silently ignored", func(t *testing.T) {
		if _, err := store.Get(ctx, "no-such-job"); !errors.Is(err, scheduler.ErrNotFound) {
			t.Errorf("Get: %v, want ErrNotFound", err)
		}
		if err := store.Complete(ctx, "no-such-job", start, start, start); !errors.Is(err, scheduler.ErrNotFound) {
			t.Errorf("Complete: %v, want ErrNotFound", err)
		}
		if _, ok, err := store.Claim(ctx, "no-such-job", start, interval); err != nil || ok {
			t.Errorf("Claim on an unknown job: ok=%v err=%v", ok, err)
		}
	})
}

// The claim is the whole single-firing mechanism, and the in-memory store
// proves nothing about it: only a real database shows whether two concurrent
// conditional updates serialise. Ten goroutines race for one tick; exactly
// one must win.
func TestSchedulerStoreClaimIsExclusiveUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	store := scheduler.NewStore(pool)
	name := "race-" + uuid.NewString()[:8]

	now := time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC)
	interval := 15 * time.Minute
	if err := store.Ensure(ctx, name, now, now.Add(-time.Hour)); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	const racers = 10
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	var failures []error

	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, ok, err := store.Claim(ctx, name, now, interval)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, err)
				return
			}
			if ok {
				wins++
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(failures) > 0 {
		t.Fatalf("claims failed: %v", failures)
	}
	if wins != 1 {
		t.Errorf("%d of %d racers claimed the same tick, want exactly 1", wins, racers)
	}
}

// countingPublisher records how many jobs reached the queue.
type countingPublisher struct {
	mu   sync.Mutex
	jobs []queue.Job
}

func (p *countingPublisher) Publish(_ context.Context, j queue.Job) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.jobs = append(p.jobs, j)
	return nil
}

func (p *countingPublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.jobs)
}

// The definition-of-done for Phase 2, against real infrastructure: two
// replicas sharing one PostgreSQL row and one Redis lock fire a tick exactly
// once. The unit test proves the logic; this proves the two pieces of
// infrastructure agree with it.
func TestTwoReplicasFireATickOnceAgainstRealInfrastructure(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := scheduler.NewStore(pool)
	locker := scheduler.NewRedisLocker(rdb, quiet)
	pub := &countingPublisher{}

	name := "two-replicas-" + uuid.NewString()[:8]
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	var builds atomic.Int32
	job := scheduler.Job{
		Name:     name,
		Interval: 15 * time.Minute,
		Build: func(context.Context, scheduler.Window) ([]queue.Job, error) {
			builds.Add(1)
			return []queue.Job{{Workflow: "STOCK_SYNC", EventType: "SCHEDULED"}}, nil
		},
	}

	replicas := make([]*scheduler.Scheduler, 2)
	for i := range replicas {
		s, err := scheduler.New(store, locker, pub,
			scheduler.WithClock(clock), scheduler.WithLogger(quiet))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := s.Register(ctx, job); err != nil {
			t.Fatalf("Register: %v", err)
		}
		replicas[i] = s
	}

	// Register schedules the first run one interval out, so nothing is due
	// at the registration instant. Move the shared clock past it.
	now = now.Add(15 * time.Minute)

	var wg sync.WaitGroup
	var fired atomic.Int32
	for _, s := range replicas {
		wg.Add(1)
		go func(s *scheduler.Scheduler) {
			defer wg.Done()
			for n := 0; n < 5; n++ {
				fired.Add(int32(s.RunDue(ctx)))
			}
		}(s)
	}
	wg.Wait()

	if got := fired.Load(); got != 1 {
		t.Errorf("%d runs fired across two replicas, want exactly 1", got)
	}
	if got := builds.Load(); got != 1 {
		t.Errorf("the job was built %d times, want 1", got)
	}
	if pub.count() != 1 {
		t.Errorf("published %d jobs, want 1", pub.count())
	}

	st, err := store.Get(ctx, name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !st.Watermark.Equal(now) {
		t.Errorf("watermark = %s, want the window end %s", st.Watermark, now)
	}
}
