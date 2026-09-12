package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/event"
	"github.com/gluzo/integration-gateway/app/idempotency"
	"github.com/gluzo/integration-gateway/app/intlog"
	"github.com/gluzo/integration-gateway/app/queue"
	"github.com/gluzo/integration-gateway/app/worker"
	"github.com/gluzo/integration-gateway/app/workflow"
	"github.com/gluzo/integration-gateway/app/workflowstate"
)

// scriptedAction fails a configurable number of times before succeeding.
type scriptedAction struct {
	name     string
	failures int32
	err      error
	calls    atomic.Int32
	block    chan struct{} // when set, Execute blocks until closed or ctx done
}

func (a *scriptedAction) Name() string { return a.name }
func (a *scriptedAction) Execute(ctx context.Context, _ *workflow.State) error {
	n := a.calls.Add(1)
	if a.block != nil {
		select {
		case <-a.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if n <= a.failures {
		return a.err
	}
	return nil
}

type fixture struct {
	q        *queue.Memory
	repo     *workflowstate.MemoryRepository
	idem     *idempotency.MemoryStore
	recorder *intlog.Memory
	registry *workflow.Registry
	a, b     *scriptedAction
	w        *worker.Worker
}

func newFixture(t *testing.T, cfg worker.Config) *fixture {
	t.Helper()
	f := &fixture{
		q:        queue.NewMemory(16),
		repo:     workflowstate.NewMemoryRepository(),
		idem:     idempotency.NewMemoryStore(),
		recorder: &intlog.Memory{},
		registry: workflow.NewRegistry(),
		a:        &scriptedAction{name: "A"},
		b:        &scriptedAction{name: "B"},
	}
	policy := workflow.Policy{MaxAttempts: 2, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	wf := workflow.NewDefinition("TEST", workflow.Step{Action: f.a, Policy: policy}, workflow.Step{Action: f.b, Policy: policy})
	if err := f.registry.Register(wf, event.OrderCreated); err != nil {
		t.Fatalf("Register: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	exec := workflow.NewExecutor(f.repo, workflow.WithRecorder(f.recorder), workflow.WithLogger(logger),
		workflow.WithSleep(func(context.Context, time.Duration) error { return nil }))
	w, err := worker.New(worker.Dependencies{
		Queue: f.q, Registry: f.registry, Executor: exec, States: f.repo, Idempotency: f.idem, Recorder: f.recorder, Logger: logger,
	}, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f.w = w
	return f
}

func (f *fixture) enqueue(t *testing.T, id, eventType string) event.Event {
	t.Helper()
	ev := event.Event{
		Platform: "easyecom", EventType: eventType, CorrelationID: id, IntegrationID: "integ", ExternalOrderID: "1001",
		RoutingKey: event.RoutingKey{Type: "warehouse_id", Value: "5"}, IdempotencyKey: "key-" + id, Payload: []byte(`{}`),
	}
	if _, err := f.idem.Claim(context.Background(), idempotency.Record{Key: ev.IdempotencyKey, CorrelationID: id}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	payload, _ := json.Marshal(ev)
	if err := f.q.Publish(context.Background(), queue.Job{ID: "job-" + id, CorrelationID: id, EventType: eventType, Platform: "easyecom", IdempotencyKey: ev.IdempotencyKey, Payload: payload}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	return ev
}

// runUntil runs the worker until cond holds or the timeout passes.
func (f *fixture) runUntil(t *testing.T, cond func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = f.w.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
}

func (f *fixture) state(t *testing.T, id string) *workflow.State {
	t.Helper()
	st, err := f.repo.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("state %s: %v", id, err)
	}
	return st
}

func (f *fixture) idemStatus(t *testing.T, ev event.Event) idempotency.Status {
	t.Helper()
	rec, err := f.idem.Get(context.Background(), ev.IdempotencyKey)
	if err != nil {
		t.Fatalf("idempotency %s: %v", ev.IdempotencyKey, err)
	}
	return rec.Status
}

func TestJobRunsWorkflowToCompletion(t *testing.T) {
	f := newFixture(t, worker.Config{Concurrency: 1})
	ev := f.enqueue(t, "INT-1", event.OrderCreated)

	f.runUntil(t, func() bool {
		st, err := f.repo.Get(context.Background(), "INT-1")
		return err == nil && st.Status == workflow.StatusCompleted
	})
	st := f.state(t, "INT-1")
	if st.JobID != "job-INT-1" || st.Event.ExternalOrderID != "1001" || f.a.calls.Load() != 1 || f.b.calls.Load() != 1 {
		t.Fatalf("state = %+v calls a=%d b=%d", st, f.a.calls.Load(), f.b.calls.Load())
	}
	if f.idemStatus(t, ev) != idempotency.StatusCompleted {
		t.Fatalf("idempotency status = %s", f.idemStatus(t, ev))
	}
	if f.q.Len() != 0 {
		t.Fatal("job was requeued")
	}
}

func TestFailedWorkflowIsAcknowledgedAndMarkedFailed(t *testing.T) {
	f := newFixture(t, worker.Config{Concurrency: 1})
	f.b.failures = 10
	f.b.err = apperror.New(apperror.Mapping, "cannot map")
	ev := f.enqueue(t, "INT-2", event.OrderCreated)

	f.runUntil(t, func() bool {
		rec, err := f.idem.Get(context.Background(), ev.IdempotencyKey)
		return err == nil && rec.Status == idempotency.StatusFailed
	})
	st := f.state(t, "INT-2")
	if st.Status != workflow.StatusFailed || st.NextAction != "B" || f.b.calls.Load() != 1 {
		t.Fatalf("state = %+v calls b=%d", st, f.b.calls.Load())
	}
	if f.q.Len() != 0 {
		t.Fatal("failed run must not be requeued; retries are at action level")
	}
}

func TestUnboundEventIsSkipped(t *testing.T) {
	f := newFixture(t, worker.Config{Concurrency: 1})
	ev := f.enqueue(t, "INT-3", event.OrderCancelled)

	f.runUntil(t, func() bool {
		rec, err := f.idem.Get(context.Background(), ev.IdempotencyKey)
		return err == nil && rec.Status == idempotency.StatusCompleted
	})
	if _, err := f.repo.Get(context.Background(), "INT-3"); !errors.Is(err, workflow.ErrStateNotFound) {
		t.Fatalf("no state expected for an unbound event, got %v", err)
	}
	entries := f.recorder.Entries()
	if len(entries) != 1 || entries[0].Action != intlog.ActionWorkflowSkipped {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestPoisonPayloadIsDiscarded(t *testing.T) {
	f := newFixture(t, worker.Config{Concurrency: 1})
	_, _ = f.idem.Claim(context.Background(), idempotency.Record{Key: "poison", CorrelationID: "INT-4"})
	_ = f.q.Publish(context.Background(), queue.Job{ID: "job", CorrelationID: "INT-4", EventType: event.OrderCreated, IdempotencyKey: "poison", Payload: []byte(`not json`)})

	f.runUntil(t, func() bool {
		rec, err := f.idem.Get(context.Background(), "poison")
		return err == nil && rec.Status == idempotency.StatusFailed
	})
	if f.q.Len() != 0 {
		t.Fatal("poison job was requeued")
	}
}

func TestRedeliveryOfFinishedRunIsAcknowledged(t *testing.T) {
	f := newFixture(t, worker.Config{Concurrency: 1})
	ev := f.enqueue(t, "INT-5", event.OrderCreated)
	f.runUntil(t, func() bool {
		st, err := f.repo.Get(context.Background(), "INT-5")
		return err == nil && st.Status == workflow.StatusCompleted
	})

	// The same job arrives again (reclaimed after a worker died post-run).
	payload, _ := json.Marshal(ev)
	_ = f.q.Publish(context.Background(), queue.Job{ID: "job-again", CorrelationID: "INT-5", EventType: event.OrderCreated, IdempotencyKey: ev.IdempotencyKey, Payload: payload})
	f.runUntil(t, func() bool { return f.q.Len() == 0 })
	time.Sleep(20 * time.Millisecond)
	if f.a.calls.Load() != 1 {
		t.Fatalf("finished run was re-executed: %d calls", f.a.calls.Load())
	}
}

func TestStartupRecoveryResumesInterruptedAndTransientRuns(t *testing.T) {
	f := newFixture(t, worker.Config{Concurrency: 2, MaxAutoResumes: 3, RetryFailedAfter: time.Millisecond})
	ctx := context.Background()
	now := time.Now().Add(-time.Hour)

	mk := func(id string, status workflow.Status, lastErr *apperror.Info, resumes int) {
		ev := event.Event{Platform: "easyecom", EventType: event.OrderCreated, CorrelationID: id, IntegrationID: "i", ExternalOrderID: "1", RoutingKey: event.RoutingKey{Type: "warehouse_id", Value: "5"}, IdempotencyKey: "key-" + id, Payload: []byte(`{}`)}
		st := workflow.NewState("TEST", ev, "job", now)
		// A RUNNING run died with B's final attempt in flight; a FAILED run
		// exhausted B's budget.
		bStatus := workflow.ActionFailed
		if status == workflow.StatusRunning {
			bStatus = workflow.ActionRunning
		}
		st.Actions = []workflow.ActionRecord{{Name: "A", Status: workflow.ActionSucceeded, Attempt: 1}, {Name: "B", Status: bStatus, Attempt: 2, LastError: lastErr}}
		st.CurrentAction, st.LastSuccessfulAction, st.NextAction = 1, "A", "B"
		st.Status, st.LastError, st.ResumeCount, st.UpdatedAt = status, lastErr, resumes, now
		if err := f.repo.Save(ctx, st); err != nil {
			t.Fatalf("Save %s: %v", id, err)
		}
		_, _ = f.idem.Claim(ctx, idempotency.Record{Key: "key-" + id, CorrelationID: id})
	}
	transient := &apperror.Info{Category: apperror.Timeout, Message: "timeout", Retryable: true}
	permanent := &apperror.Info{Category: apperror.Mapping, Message: "bad", Retryable: false}
	mk("INT-running", workflow.StatusRunning, nil, 0)
	mk("INT-transient", workflow.StatusFailed, transient, 0)
	mk("INT-permanent", workflow.StatusFailed, permanent, 0)
	mk("INT-exhausted", workflow.StatusFailed, transient, 3)

	f.w.Recover(ctx, true)

	if st := f.state(t, "INT-running"); st.Status != workflow.StatusCompleted || st.ResumeCount != 1 {
		t.Fatalf("interrupted run not resumed: %+v", st)
	}
	if st := f.state(t, "INT-transient"); st.Status != workflow.StatusCompleted || st.ResumeCount != 1 || st.Actions[1].Attempt != 1 {
		t.Fatalf("transient failure not resumed with a fresh budget: %+v", st)
	}
	if st := f.state(t, "INT-permanent"); st.Status != workflow.StatusFailed || st.ResumeCount != 0 {
		t.Fatalf("permanent failure must wait for an operator: %+v", st)
	}
	if st := f.state(t, "INT-exhausted"); st.Status != workflow.StatusFailed || st.ResumeCount != 3 {
		t.Fatalf("exhausted auto-resumes must not resume again: %+v", st)
	}
	if f.a.calls.Load() != 0 || f.b.calls.Load() != 2 {
		t.Fatalf("resume must only run the failed action: a=%d b=%d", f.a.calls.Load(), f.b.calls.Load())
	}
	rec, _ := f.idem.Get(ctx, "key-INT-transient")
	if rec.Status != idempotency.StatusCompleted {
		t.Fatalf("idempotency after resume = %s", rec.Status)
	}
}

func TestPeriodicScanLeavesFreshRunningRunsAlone(t *testing.T) {
	f := newFixture(t, worker.Config{Concurrency: 1, StaleRunningAfter: time.Hour})
	ctx := context.Background()
	ev := event.Event{Platform: "easyecom", EventType: event.OrderCreated, CorrelationID: "INT-fresh", IntegrationID: "i", ExternalOrderID: "1", RoutingKey: event.RoutingKey{Type: "warehouse_id", Value: "5"}, IdempotencyKey: "k", Payload: []byte(`{}`)}
	st := workflow.NewState("TEST", ev, "job", time.Now())
	st.Status = workflow.StatusRunning
	st.UpdatedAt = time.Now()
	_ = f.repo.Save(ctx, st)

	f.w.Recover(ctx, false)
	if got := f.state(t, "INT-fresh"); got.Status != workflow.StatusRunning {
		t.Fatalf("fresh running run was resumed by a periodic scan: %+v", got)
	}
	f.w.Recover(ctx, true)
	if got := f.state(t, "INT-fresh"); got.Status != workflow.StatusCompleted {
		t.Fatalf("startup scan must resume running runs: %+v", got)
	}
}

func TestManualResume(t *testing.T) {
	f := newFixture(t, worker.Config{Concurrency: 1})
	ctx := context.Background()
	f.b.failures = 1
	f.b.err = apperror.New(apperror.Authentication, "token expired")
	ev := f.enqueue(t, "INT-6", event.OrderCreated)
	f.runUntil(t, func() bool {
		rec, err := f.idem.Get(ctx, ev.IdempotencyKey)
		return err == nil && rec.Status == idempotency.StatusFailed
	})

	if err := f.w.Resume(ctx, "INT-6"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if st := f.state(t, "INT-6"); st.Status != workflow.StatusCompleted || st.ResumeCount != 1 {
		t.Fatalf("state after manual resume = %+v", st)
	}
	if err := f.w.Resume(ctx, "INT-6"); !errors.Is(err, worker.ErrRunFinished) {
		t.Fatalf("resume of finished run: %v", err)
	}
	if err := f.w.Resume(ctx, "INT-missing"); !errors.Is(err, workflow.ErrStateNotFound) {
		t.Fatalf("resume of unknown run: %v", err)
	}
}

func TestShutdownLeavesJobPendingAndStateRunning(t *testing.T) {
	f := newFixture(t, worker.Config{Concurrency: 1})
	f.a.block = make(chan struct{})
	ev := f.enqueue(t, "INT-7", event.OrderCreated)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = f.w.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for f.a.calls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("action never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel() // shutdown while A is blocked
	wg.Wait()

	st := f.state(t, "INT-7")
	if st.Status != workflow.StatusRunning || st.CurrentAction != 0 {
		t.Fatalf("interrupted state = %+v", st)
	}
	if rec, _ := f.idem.Get(context.Background(), ev.IdempotencyKey); rec.Status != idempotency.StatusProcessing {
		t.Fatalf("idempotency status = %s, want processing", rec.Status)
	}
}

func TestNewValidatesDependencies(t *testing.T) {
	if _, err := worker.New(worker.Dependencies{}, worker.Config{}); err == nil {
		t.Fatal("empty dependencies accepted")
	}
}
