package workflow_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/event"
	"github.com/gluzo/integration-gateway/app/intlog"
	"github.com/gluzo/integration-gateway/app/workflow"
	"github.com/gluzo/integration-gateway/app/workflowstate"
)

// harness wires an executor with an in-memory repository, a memory recorder
// and a recording sleeper.
type harness struct {
	repo     *workflowstate.MemoryRepository
	recorder *intlog.Memory
	delays   []time.Duration
	sleepErr error
	exec     *workflow.Executor
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{repo: workflowstate.NewMemoryRepository(), recorder: &intlog.Memory{}}
	h.exec = workflow.NewExecutor(h.repo,
		workflow.WithRecorder(h.recorder),
		workflow.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		workflow.WithSleep(func(_ context.Context, d time.Duration) error {
			h.delays = append(h.delays, d)
			return h.sleepErr
		}),
	)
	return h
}

func newState(id string) *workflow.State {
	return workflow.NewState("TEST", event.Event{
		Platform: "easyecom", EventType: event.OrderCreated, CorrelationID: id, ExternalOrderID: "1001",
		RoutingKey: event.RoutingKey{Type: "warehouse_id", Value: "5"}, IdempotencyKey: "k", Payload: []byte(`{}`),
	}, "job-1", time.Now())
}

func ok(name string, calls *atomic.Int32) workflow.Step {
	return workflow.Step{Action: workflow.NewAction(name, func(context.Context, *workflow.State) error {
		if calls != nil {
			calls.Add(1)
		}
		return nil
	}), Policy: workflow.DefaultPolicy()}
}

func actionsByName(entries []intlog.Entry, action string) []intlog.Entry {
	var out []intlog.Entry
	for _, e := range entries {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

func lastEntry(entries []intlog.Entry) intlog.Entry {
	return entries[len(entries)-1]
}

func TestAllActionsSucceed(t *testing.T) {
	h := newHarness(t)
	var a, b, c atomic.Int32
	wf := workflow.NewDefinition("TEST", ok("A", &a), ok("B", &b), ok("C", &c))
	st := newState("INT-1")

	if err := h.exec.Run(context.Background(), wf, st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Status != workflow.StatusCompleted || st.CurrentAction != 3 || st.LastSuccessfulAction != "C" || st.NextAction != "" || st.CompletedAt == nil {
		t.Fatalf("state = %+v", st)
	}
	for i, rec := range st.Actions {
		if rec.Status != workflow.ActionSucceeded || rec.Attempt != 1 || rec.LastError != nil {
			t.Fatalf("action %d = %+v", i, rec)
		}
	}
	if a.Load() != 1 || b.Load() != 1 || c.Load() != 1 {
		t.Fatal("each action must run exactly once")
	}
	entries := h.recorder.Entries()
	if len(actionsByName(entries, intlog.ActionWorkflowStarted)) != 1 || len(actionsByName(entries, "A")) != 1 || lastEntry(entries).Action != intlog.ActionWorkflowCompleted {
		t.Fatalf("entries = %+v", entries)
	}
	persisted, _ := h.repo.Get(context.Background(), "INT-1")
	if persisted.Status != workflow.StatusCompleted {
		t.Fatalf("persisted status = %s", persisted.Status)
	}
	if len(h.delays) != 0 {
		t.Fatal("no backoff expected")
	}
}

func TestActionFailsThenRetriesThenSucceeds(t *testing.T) {
	h := newHarness(t)
	var calls atomic.Int32
	flaky := workflow.Step{Action: workflow.NewAction("FLAKY", func(context.Context, *workflow.State) error {
		if calls.Add(1) < 3 {
			return apperror.New(apperror.Timeout, "upstream timed out")
		}
		return nil
	}), Policy: workflow.Policy{MaxAttempts: 3, BaseDelay: time.Second, MaxDelay: 10 * time.Second}}
	wf := workflow.NewDefinition("TEST", ok("A", nil), flaky, ok("C", nil))
	st := newState("INT-2")

	if err := h.exec.Run(context.Background(), wf, st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Status != workflow.StatusCompleted || st.Actions[1].Attempt != 3 || st.Actions[1].Status != workflow.ActionSucceeded || st.Actions[1].LastError != nil {
		t.Fatalf("state = %+v", st.Actions[1])
	}
	if len(h.delays) != 2 {
		t.Fatalf("delays = %v, want 2 backoffs", h.delays)
	}
	if h.delays[0] < 500*time.Millisecond || h.delays[0] > time.Second || h.delays[1] < time.Second || h.delays[1] > 2*time.Second {
		t.Fatalf("backoff outside jitter window: %v", h.delays)
	}
	flakyEntries := actionsByName(h.recorder.Entries(), "FLAKY")
	if len(flakyEntries) != 3 || flakyEntries[0].Status != intlog.StatusFailed || flakyEntries[0].Attempt != 1 || flakyEntries[2].Status != intlog.StatusSuccess || flakyEntries[2].Attempt != 3 {
		t.Fatalf("append-only attempt history wrong: %+v", flakyEntries)
	}
	if flakyEntries[0].Error == nil || flakyEntries[0].Error.Category != apperror.Timeout {
		t.Fatalf("failed attempt lacks error info: %+v", flakyEntries[0])
	}
}

func TestNonRetryableErrorFailsImmediately(t *testing.T) {
	h := newHarness(t)
	var after atomic.Int32
	bad := workflow.Step{Action: workflow.NewAction("BAD", func(context.Context, *workflow.State) error {
		return apperror.New(apperror.Mapping, "cannot map sku")
	}), Policy: workflow.DefaultPolicy()}
	wf := workflow.NewDefinition("TEST", ok("A", nil), bad, ok("C", &after))
	st := newState("INT-3")

	err := h.exec.Run(context.Background(), wf, st)
	if err == nil || apperror.CategoryOf(err) != apperror.Mapping {
		t.Fatalf("Run err = %v", err)
	}
	if st.Status != workflow.StatusFailed || st.CurrentAction != 1 || st.NextAction != "BAD" || st.LastSuccessfulAction != "A" || st.Actions[1].Attempt != 1 {
		t.Fatalf("state = %+v", st)
	}
	if st.LastError == nil || st.LastError.Category != apperror.Mapping {
		t.Fatalf("last error = %+v", st.LastError)
	}
	if after.Load() != 0 {
		t.Fatal("actions after the failure must not run")
	}
	if len(h.delays) != 0 {
		t.Fatal("non-retryable error must not back off")
	}
	if last := lastEntry(h.recorder.Entries()); last.Action != intlog.ActionWorkflowFailed || last.Error == nil {
		t.Fatalf("last entry = %+v", last)
	}
}

func TestRetryBudgetExhausted(t *testing.T) {
	h := newHarness(t)
	var calls atomic.Int32
	always := workflow.Step{Action: workflow.NewAction("DOWN", func(context.Context, *workflow.State) error {
		calls.Add(1)
		return apperror.New(apperror.Network, "connection refused")
	}), Policy: workflow.Policy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}}
	wf := workflow.NewDefinition("TEST", always)
	st := newState("INT-4")

	err := h.exec.Run(context.Background(), wf, st)
	if err == nil || !apperror.IsRetryable(err) {
		t.Fatalf("Run err = %v", err)
	}
	if calls.Load() != 3 || st.Actions[0].Attempt != 3 || st.Status != workflow.StatusFailed || len(h.delays) != 2 {
		t.Fatalf("calls=%d attempt=%d status=%s delays=%d", calls.Load(), st.Actions[0].Attempt, st.Status, len(h.delays))
	}
}

func TestSkipEndsWorkflowSuccessfully(t *testing.T) {
	h := newHarness(t)
	var after atomic.Int32
	resolve := workflow.Step{Action: workflow.NewAction("RESOLVE", func(context.Context, *workflow.State) error {
		return workflow.Skip("no route for warehouse_id=5")
	}), Policy: workflow.DefaultPolicy()}
	wf := workflow.NewDefinition("TEST", resolve, ok("B", &after))
	st := newState("INT-5")

	if err := h.exec.Run(context.Background(), wf, st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Status != workflow.StatusSkipped || st.SkipReason != "no route for warehouse_id=5" || st.Actions[0].Status != workflow.ActionSkipped || st.Actions[1].Status != workflow.ActionSkipped || st.CompletedAt == nil {
		t.Fatalf("state = %+v", st)
	}
	if after.Load() != 0 {
		t.Fatal("actions after a skip must not run")
	}
	if last := lastEntry(h.recorder.Entries()); last.Action != intlog.ActionWorkflowSkipped || last.Details["reason"] != "no route for warehouse_id=5" {
		t.Fatalf("last entry = %+v", last)
	}
}

func TestOptionalActionFailureDoesNotFailWorkflow(t *testing.T) {
	h := newHarness(t)
	tracking := workflow.Step{Action: workflow.NewAction("FETCH_TRACKING", func(context.Context, *workflow.State) error {
		return apperror.New(apperror.ExternalAPI, "not found")
	}), Policy: workflow.Policy{MaxAttempts: 1, Optional: true}}
	var after atomic.Int32
	wf := workflow.NewDefinition("TEST", ok("A", nil), tracking, ok("C", &after))
	st := newState("INT-6")

	if err := h.exec.Run(context.Background(), wf, st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Status != workflow.StatusCompleted || st.Actions[1].Status != workflow.ActionSkipped || st.Actions[1].LastError == nil || after.Load() != 1 {
		t.Fatalf("state = %+v", st)
	}
}

func TestResumeAfterCrashSkipsCompletedActions(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	var a, b, c atomic.Int32
	crashOnce := true
	unstable := workflow.Step{Action: workflow.NewAction("B", func(context.Context, *workflow.State) error {
		b.Add(1)
		if crashOnce {
			crashOnce = false
			return apperror.New(apperror.Timeout, "gateway timeout")
		}
		return nil
	}), Policy: workflow.Policy{MaxAttempts: 1}}
	wf := workflow.NewDefinition("TEST", ok("A", &a), unstable, ok("C", &c))

	// First run: A succeeds, B fails permanently (single attempt), workflow FAILED.
	st := newState("INT-7")
	if err := h.exec.Run(ctx, wf, st); err == nil {
		t.Fatal("first run should fail")
	}
	if st.Status != workflow.StatusFailed || st.CurrentAction != 1 {
		t.Fatalf("state after failure = %+v", st)
	}

	// Simulate a restart: load the persisted state with a fresh executor.
	h2 := newHarness(t)
	h2.repo = h.repo
	h2.exec = workflow.NewExecutor(h.repo, workflow.WithRecorder(h2.recorder), workflow.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	loaded, err := h.repo.Get(ctx, "INT-7")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	loaded.ResetCurrentAttempts()
	if err := h2.exec.Run(ctx, wf, loaded); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if loaded.Status != workflow.StatusCompleted || loaded.ResumeCount != 1 || loaded.Actions[1].Attempt != 1 {
		t.Fatalf("state after resume = %+v", loaded)
	}
	if a.Load() != 1 || b.Load() != 2 || c.Load() != 1 {
		t.Fatalf("calls a=%d b=%d c=%d; A must not be replayed", a.Load(), b.Load(), c.Load())
	}
	entries := h2.recorder.Entries()
	if entries[0].Action != intlog.ActionWorkflowResumed || entries[0].Details["next_action"] != "B" {
		t.Fatalf("resume not recorded: %+v", entries[0])
	}
}

func TestInterruptedRunLeavesStateRunningForRecovery(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	h.sleepErr = context.Canceled
	var calls atomic.Int32
	step := workflow.Step{Action: workflow.NewAction("A", func(context.Context, *workflow.State) error {
		calls.Add(1)
		cancel() // shutdown arrives while the action is failing
		return apperror.New(apperror.Network, "reset")
	}), Policy: workflow.DefaultPolicy()}
	wf := workflow.NewDefinition("TEST", step, ok("B", nil))
	st := newState("INT-8")

	err := h.exec.Run(ctx, wf, st)
	if err == nil || apperror.IsRetryable(err) {
		t.Fatalf("Run err = %v, want non-retryable cancellation", err)
	}
	persisted, _ := h.repo.Get(context.Background(), "INT-8")
	if persisted.Status != workflow.StatusRunning || persisted.CurrentAction != 0 || persisted.Actions[0].Attempt != 1 {
		t.Fatalf("persisted = %+v", persisted)
	}
	active, _ := h.repo.ListActive(context.Background())
	if len(active) != 1 {
		t.Fatalf("interrupted run must be listed for recovery: %v", active)
	}
}

func TestInterruptedAttemptDoesNotConsumeRetryBudget(t *testing.T) {
	h := newHarness(t)
	var calls atomic.Int32
	wf := workflow.NewDefinition("TEST", workflow.Step{Action: workflow.NewAction("A", func(context.Context, *workflow.State) error {
		calls.Add(1)
		return nil
	}), Policy: workflow.Policy{MaxAttempts: 2}})

	// A state persisted while the final attempt was in flight when the
	// process died: the attempt's outcome is unknown, so it runs again.
	st := newState("INT-15")
	st.Status = workflow.StatusRunning
	st.Actions = []workflow.ActionRecord{{Name: "A", Status: workflow.ActionRunning, Attempt: 2}}
	if err := h.exec.Run(context.Background(), wf, st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Status != workflow.StatusCompleted || st.Actions[0].Attempt != 2 || calls.Load() != 1 {
		t.Fatalf("state = %+v calls = %d", st.Actions[0], calls.Load())
	}
}

func TestPanicBecomesInternalError(t *testing.T) {
	h := newHarness(t)
	boom := workflow.Step{Action: workflow.NewAction("BOOM", func(context.Context, *workflow.State) error {
		panic("nil pointer somewhere")
	}), Policy: workflow.NoRetry()}
	wf := workflow.NewDefinition("TEST", boom)
	st := newState("INT-9")

	err := h.exec.Run(context.Background(), wf, st)
	if err == nil || apperror.CategoryOf(err) != apperror.Internal || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("Run err = %v", err)
	}
	if st.Status != workflow.StatusFailed {
		t.Fatalf("status = %s", st.Status)
	}
}

func TestPerAttemptTimeoutIsRetried(t *testing.T) {
	h := newHarness(t)
	var calls atomic.Int32
	slow := workflow.Step{Action: workflow.NewAction("SLOW", func(ctx context.Context, _ *workflow.State) error {
		if calls.Add(1) == 1 {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}), Policy: workflow.Policy{MaxAttempts: 2, Timeout: 20 * time.Millisecond, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}}
	wf := workflow.NewDefinition("TEST", slow)
	st := newState("INT-10")

	if err := h.exec.Run(context.Background(), wf, st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st.Actions[0].Attempt != 2 || st.Status != workflow.StatusCompleted {
		t.Fatalf("state = %+v", st.Actions[0])
	}
}

func TestDefinitionMismatchIsRejected(t *testing.T) {
	h := newHarness(t)
	wf := workflow.NewDefinition("TEST", ok("A", nil), ok("B", nil))
	st := newState("INT-11")
	if err := h.exec.Run(context.Background(), wf, st); err != nil {
		t.Fatalf("Run: %v", err)
	}

	changed := workflow.NewDefinition("TEST", ok("A", nil), ok("Z", nil))
	st.Status = workflow.StatusFailed
	st.CurrentAction = 1
	if err := h.exec.Run(context.Background(), changed, st); err == nil || apperror.CategoryOf(err) != apperror.Workflow {
		t.Fatalf("mismatched definition accepted: %v", err)
	}
	other := workflow.NewDefinition("OTHER", ok("A", nil), ok("B", nil))
	unnamed := newState("INT-12")
	unnamed.WorkflowName = ""
	if err := h.exec.Run(context.Background(), other, unnamed); err != nil || unnamed.WorkflowName != "OTHER" {
		t.Fatalf("fresh state adopts the workflow name: %v (%q)", err, unnamed.WorkflowName)
	}
	st2 := newState("INT-13")
	st2.WorkflowName = "TEST"
	if err := h.exec.Run(context.Background(), other, st2); err == nil {
		t.Fatal("state of another workflow accepted")
	}
}

func TestCompletedStateIsNotRerun(t *testing.T) {
	h := newHarness(t)
	var calls atomic.Int32
	wf := workflow.NewDefinition("TEST", ok("A", &calls))
	st := newState("INT-14")
	if err := h.exec.Run(context.Background(), wf, st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if err := h.exec.Run(context.Background(), wf, st); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("completed workflow re-executed actions: %d", calls.Load())
	}
}

func TestRegistry(t *testing.T) {
	r := workflow.NewRegistry()
	wf := workflow.NewDefinition("ORDER_SYNC", ok("A", nil))
	if err := r.Register(wf, event.OrderCreated, event.OrderConfirmed); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got, ok := r.ForEvent(event.OrderConfirmed); !ok || got.Name() != "ORDER_SYNC" {
		t.Fatalf("ForEvent = %v, %v", got, ok)
	}
	if got, ok := r.ByName("ORDER_SYNC"); !ok || got != wf {
		t.Fatalf("ByName = %v, %v", got, ok)
	}
	if _, ok := r.ForEvent(event.TrackingUpdated); ok {
		t.Fatal("unbound event resolved")
	}
	if err := r.Register(workflow.NewDefinition("ORDER_SYNC", ok("A", nil))); err == nil {
		t.Fatal("duplicate name accepted")
	}
	if err := r.Register(workflow.NewDefinition("OTHER", ok("A", nil)), event.OrderCreated); err == nil {
		t.Fatal("double-bound event accepted")
	}
	if err := r.Register(workflow.NewDefinition("EMPTY")); err == nil {
		t.Fatal("empty definition accepted")
	}
	if err := r.Register(workflow.NewDefinition("DUP", ok("A", nil), ok("A", nil))); err == nil {
		t.Fatal("duplicate action names accepted")
	}
}

func TestSkipHelpers(t *testing.T) {
	err := workflow.Skip("nothing to do")
	if reason, ok := workflow.IsSkip(err); !ok || reason != "nothing to do" {
		t.Fatalf("IsSkip = %q, %v", reason, ok)
	}
	if _, ok := workflow.IsSkip(errors.New("other")); ok {
		t.Fatal("plain error treated as skip")
	}
	if !strings.Contains(err.Error(), "nothing to do") {
		t.Fatalf("Error = %q", err.Error())
	}
}
