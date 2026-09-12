package workflow

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"time"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/intlog"
)

// Executor runs workflows against persisted state.
//
// Every action attempt is recorded in the integration log; every successful
// action is persisted before the next one starts, so a crash loses at most
// the action in flight. Retries happen per action with exponential backoff
// and jitter, never by restarting the workflow.
type Executor struct {
	repo      Repository
	recorder  intlog.Recorder
	logger    *slog.Logger
	now       func() time.Time
	sleep     func(ctx context.Context, d time.Duration) error
	randFloat func() float64
}

// ExecutorOption configures an Executor.
type ExecutorOption func(*Executor)

// WithRecorder sets the integration log recorder.
func WithRecorder(r intlog.Recorder) ExecutorOption {
	return func(e *Executor) {
		if r != nil {
			e.recorder = r
		}
	}
}

// WithLogger sets the application logger.
func WithLogger(l *slog.Logger) ExecutorOption {
	return func(e *Executor) {
		if l != nil {
			e.logger = l
		}
	}
}

// WithClock replaces the clock; intended for tests.
func WithClock(now func() time.Time) ExecutorOption {
	return func(e *Executor) { e.now = now }
}

// WithSleep replaces the backoff sleeper; intended for tests.
func WithSleep(fn func(ctx context.Context, d time.Duration) error) ExecutorOption {
	return func(e *Executor) { e.sleep = fn }
}

// NewExecutor builds an Executor persisting through repo.
func NewExecutor(repo Repository, opts ...ExecutorOption) *Executor {
	e := &Executor{
		repo:      repo,
		recorder:  intlog.Nop{},
		logger:    slog.Default(),
		now:       time.Now,
		sleep:     defaultSleep,
		randFloat: rand.Float64,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Run executes wf against state starting at state.CurrentAction.
//
// It returns nil when the workflow completed or was skipped. It returns the
// failing action's error when the workflow failed (state is persisted as
// FAILED and can be resumed) and the context error when execution was
// interrupted (state stays RUNNING so a restart resumes it).
func (e *Executor) Run(ctx context.Context, wf Workflow, state *State) error {
	steps := wf.Steps()
	if err := e.prepare(wf, state); err != nil {
		return err
	}
	if state.Status.IsFinal() {
		return nil
	}

	resumed := state.Status != StatusPending
	if resumed {
		state.ResumeCount++
	}
	state.Status = StatusRunning
	if err := e.save(ctx, state); err != nil {
		return err
	}
	e.recordWorkflow(ctx, state, resumed)

	for i := state.CurrentAction; i < len(steps); i++ {
		step := steps[i]
		rec := &state.Actions[i]
		state.CurrentAction = i
		state.NextAction = step.Action.Name()

		err := e.runStep(ctx, state, step, rec)
		if err == nil {
			state.LastSuccessfulAction = step.Action.Name()
			state.CurrentAction = i + 1
			state.NextAction = ""
			if i+1 < len(steps) {
				state.NextAction = steps[i+1].Action.Name()
			}
			if err := e.save(ctx, state); err != nil {
				return err
			}
			continue
		}

		if reason, ok := IsSkip(err); ok {
			state.Status = StatusSkipped
			state.SkipReason = reason
			rec.Status = ActionSkipped
			for j := i + 1; j < len(steps); j++ {
				state.Actions[j].Status = ActionSkipped
			}
			state.NextAction = ""
			now := e.now()
			state.CompletedAt = &now
			if err := e.save(ctx, state); err != nil {
				return err
			}
			e.recordOutcome(ctx, state, intlog.ActionWorkflowSkipped, intlog.StatusSkipped, map[string]any{"reason": reason})
			return nil
		}

		if ctx.Err() != nil {
			// Interrupted (shutdown): leave the state RUNNING with the
			// attempt count intact so recovery resumes this action.
			_ = e.save(context.WithoutCancel(ctx), state)
			return apperror.Classify(ctx.Err())
		}

		if step.Policy.Optional {
			rec.Status = ActionSkipped
			e.logger.WarnContext(ctx, "optional action failed; continuing",
				slog.String("workflow", state.WorkflowName),
				slog.String("action", step.Action.Name()),
				slog.String("correlation_id", state.CorrelationID),
				slog.String("error", err.Error()),
			)
			state.CurrentAction = i + 1
			state.NextAction = ""
			if i+1 < len(steps) {
				state.NextAction = steps[i+1].Action.Name()
			}
			if err := e.save(ctx, state); err != nil {
				return err
			}
			continue
		}

		state.Status = StatusFailed
		state.LastError = apperror.InfoOf(err)
		if err := e.save(ctx, state); err != nil {
			return err
		}
		e.recordOutcome(ctx, state, intlog.ActionWorkflowFailed, intlog.StatusFailed, map[string]any{"failed_action": step.Action.Name()})
		return err
	}

	state.Status = StatusCompleted
	state.NextAction = ""
	state.LastError = nil
	now := e.now()
	state.CompletedAt = &now
	if err := e.save(ctx, state); err != nil {
		return err
	}
	e.recordOutcome(ctx, state, intlog.ActionWorkflowCompleted, intlog.StatusSuccess, nil)
	return nil
}

// prepare validates the definition against the state and initialises the
// action records on a first run.
func (e *Executor) prepare(wf Workflow, state *State) error {
	steps := wf.Steps()
	if err := validateSteps(steps); err != nil {
		return err
	}
	if state.WorkflowName == "" {
		state.WorkflowName = wf.Name()
	}
	if state.WorkflowName != wf.Name() {
		return apperror.New(apperror.Workflow, fmt.Sprintf("state belongs to workflow %q, not %q", state.WorkflowName, wf.Name()))
	}
	if len(state.Actions) == 0 {
		state.Actions = make([]ActionRecord, len(steps))
		for i, s := range steps {
			state.Actions[i] = ActionRecord{Name: s.Action.Name(), Status: ActionPending}
		}
		return nil
	}
	if len(state.Actions) != len(steps) {
		return apperror.New(apperror.Workflow, fmt.Sprintf("state has %d actions but workflow %q defines %d", len(state.Actions), wf.Name(), len(steps)))
	}
	for i, s := range steps {
		if state.Actions[i].Name != s.Action.Name() {
			return apperror.New(apperror.Workflow, fmt.Sprintf("state action %d is %q but workflow %q defines %q", i, state.Actions[i].Name, wf.Name(), s.Action.Name()))
		}
	}
	if state.CurrentAction < 0 || state.CurrentAction > len(steps) {
		return apperror.New(apperror.Workflow, fmt.Sprintf("state current action %d is out of range", state.CurrentAction))
	}
	return nil
}

// runStep executes one action with its retry policy. It returns nil on
// success, a SkipError when the action skipped, the context error when
// interrupted, and otherwise the last attempt's error.
func (e *Executor) runStep(ctx context.Context, state *State, step Step, rec *ActionRecord) error {
	policy := step.Policy.normalised()
	if rec.Status == ActionRunning && rec.Attempt > 0 {
		// The previous attempt was interrupted (crash or shutdown) before it
		// reported an outcome, so it must not consume retry budget: the same
		// attempt number runs again.
		rec.Attempt--
	}
	var lastErr error
	for {
		if rec.Attempt >= policy.MaxAttempts {
			if lastErr == nil && rec.LastError != nil {
				lastErr = apperror.New(rec.LastError.Category, rec.LastError.Message)
			}
			if lastErr == nil {
				lastErr = apperror.New(apperror.Workflow, "retry budget exhausted")
			}
			return lastErr
		}
		rec.Attempt++
		rec.Status = ActionRunning
		started := e.now()
		rec.StartedAt = &started
		rec.CompletedAt = nil
		if err := e.save(ctx, state); err != nil {
			return err
		}

		err := e.execute(ctx, step, policy, state)
		completed := e.now()
		rec.CompletedAt = &completed
		rec.DurationMS = completed.Sub(started).Milliseconds()

		if err == nil {
			rec.Status = ActionSucceeded
			rec.LastError = nil
			e.recordAction(ctx, state, step.Action.Name(), intlog.StatusSuccess, rec, nil)
			return nil
		}
		if reason, ok := IsSkip(err); ok {
			rec.Status = ActionSkipped
			e.recordAction(ctx, state, step.Action.Name(), intlog.StatusSkipped, rec, map[string]any{"reason": reason})
			return err
		}

		lastErr = err
		info := apperror.InfoOf(err)
		rec.Status = ActionFailed
		rec.LastError = info
		e.recordAction(ctx, state, step.Action.Name(), intlog.StatusFailed, rec, nil)

		if ctx.Err() != nil {
			return apperror.Classify(ctx.Err())
		}
		if !info.Retryable || rec.Attempt >= policy.MaxAttempts {
			return err
		}

		delay := e.backoff(policy, rec.Attempt)
		e.logger.InfoContext(ctx, "action retry scheduled",
			slog.String("workflow", state.WorkflowName),
			slog.String("action", step.Action.Name()),
			slog.Int("attempt", rec.Attempt),
			slog.Int64("delay_ms", delay.Milliseconds()),
			slog.String("error_category", string(info.Category)),
			slog.String("correlation_id", state.CorrelationID),
		)
		if err := e.save(ctx, state); err != nil {
			return err
		}
		if err := e.sleep(ctx, delay); err != nil {
			return apperror.Classify(err)
		}
	}
}

// execute runs one attempt under the policy's timeout, converting panics
// into internal errors so a buggy action cannot take the worker down.
func (e *Executor) execute(ctx context.Context, step Step, policy Policy, state *State) (err error) {
	attemptCtx := ctx
	if policy.Timeout > 0 {
		var cancel context.CancelFunc
		attemptCtx, cancel = context.WithTimeout(ctx, policy.Timeout)
		defer cancel()
	}
	defer func() {
		if r := recover(); r != nil {
			err = apperror.New(apperror.Internal, fmt.Sprintf("action %s panicked: %v", step.Action.Name(), r))
		}
	}()
	return step.Action.Execute(attemptCtx, state)
}

// backoff computes the delay before the next attempt: exponential growth
// with equal jitter, capped at the policy's maximum.
func (e *Executor) backoff(policy Policy, attempt int) time.Duration {
	exp := float64(policy.BaseDelay) * math.Pow(2, float64(attempt-1))
	d := time.Duration(math.Min(exp, float64(policy.MaxDelay)))
	return d/2 + time.Duration(e.randFloat()*float64(d/2))
}

func (e *Executor) save(ctx context.Context, state *State) error {
	state.UpdatedAt = e.now()
	if err := e.repo.Save(ctx, state); err != nil {
		return apperror.Wrap(apperror.Internal, "persist workflow state", err)
	}
	return nil
}

func (e *Executor) recordAction(ctx context.Context, state *State, action, status string, rec *ActionRecord, details map[string]any) {
	e.recorder.Record(ctx, intlog.Entry{
		Timestamp:       e.now(),
		CorrelationID:   state.CorrelationID,
		Workflow:        state.WorkflowName,
		Platform:        state.Platform,
		Integration:     state.DestinationPlatform(),
		IntegrationID:   state.IntegrationID,
		ExternalOrderID: state.Event.ExternalOrderID,
		OrderID:         state.Result(ResultDestinationOrderID),
		Action:          action,
		Status:          status,
		Attempt:         rec.Attempt,
		DurationMS:      rec.DurationMS,
		Error:           rec.LastError,
		Details:         details,
	})
}

func (e *Executor) recordWorkflow(ctx context.Context, state *State, resumed bool) {
	action := intlog.ActionWorkflowStarted
	details := map[string]any{"event_type": state.EventType}
	if resumed {
		action = intlog.ActionWorkflowResumed
		details["resume_count"] = state.ResumeCount
		details["next_action"] = state.Actions[min(state.CurrentAction, len(state.Actions)-1)].Name
	}
	e.recorder.Record(ctx, intlog.Entry{
		Timestamp:       e.now(),
		CorrelationID:   state.CorrelationID,
		Workflow:        state.WorkflowName,
		Platform:        state.Platform,
		Integration:     state.DestinationPlatform(),
		IntegrationID:   state.IntegrationID,
		ExternalOrderID: state.Event.ExternalOrderID,
		Action:          action,
		Status:          intlog.StatusStarted,
		Details:         details,
	})
}

func (e *Executor) recordOutcome(ctx context.Context, state *State, action, status string, details map[string]any) {
	e.recorder.Record(ctx, intlog.Entry{
		Timestamp:       e.now(),
		CorrelationID:   state.CorrelationID,
		Workflow:        state.WorkflowName,
		Platform:        state.Platform,
		Integration:     state.DestinationPlatform(),
		IntegrationID:   state.IntegrationID,
		ExternalOrderID: state.Event.ExternalOrderID,
		OrderID:         state.Result(ResultDestinationOrderID),
		Action:          action,
		Status:          status,
		Error:           state.LastError,
		Details:         details,
	})
}

func defaultSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
