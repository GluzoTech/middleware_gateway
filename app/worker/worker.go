// Package worker consumes queued jobs and drives workflows to completion.
//
// One job is one event. The worker loads (or creates) the workflow state
// for the job's correlation ID, runs the workflow through the executor, and
// acknowledges the job once the run has reached a durable outcome:
// completed, skipped, or failed with its state persisted for resume. Runs
// that were interrupted by a crash or a shutdown are picked up again by the
// recovery scan, as are failed runs whose last error was transient.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gluzo/integration-gateway/app/correlation"
	"github.com/gluzo/integration-gateway/app/event"
	"github.com/gluzo/integration-gateway/app/idempotency"
	"github.com/gluzo/integration-gateway/app/intlog"
	"github.com/gluzo/integration-gateway/app/queue"
	"github.com/gluzo/integration-gateway/app/workflow"
)

// Config tunes the worker.
type Config struct {
	// Concurrency is the number of jobs processed at once.
	Concurrency int
	// MaxAutoResumes caps automatic resumes of a failed run with a
	// retryable error; beyond it an operator must resume manually.
	MaxAutoResumes int
	// RecoveryInterval is how often active runs are scanned.
	RecoveryInterval time.Duration
	// StaleRunningAfter is how long a RUNNING run may go without a state
	// update before a periodic scan treats it as abandoned.
	StaleRunningAfter time.Duration
	// RetryFailedAfter is the minimum age of a retryable FAILED run before
	// it is resumed automatically.
	RetryFailedAfter time.Duration
}

func (c Config) withDefaults() Config {
	if c.Concurrency < 1 {
		c.Concurrency = 4
	}
	if c.MaxAutoResumes < 0 {
		c.MaxAutoResumes = 0
	}
	if c.RecoveryInterval <= 0 {
		c.RecoveryInterval = 5 * time.Minute
	}
	if c.StaleRunningAfter <= 0 {
		c.StaleRunningAfter = 10 * time.Minute
	}
	if c.RetryFailedAfter <= 0 {
		c.RetryFailedAfter = time.Minute
	}
	return c
}

// Dependencies wires the worker.
type Dependencies struct {
	Queue       queue.Consumer
	Registry    *workflow.Registry
	Executor    *workflow.Executor
	States      workflow.Repository
	Idempotency idempotency.Store
	Recorder    intlog.Recorder
	Logger      *slog.Logger
}

// Sentinel errors for Resume.
var (
	ErrRunFinished   = errors.New("worker: run already finished")
	ErrRunInProgress = errors.New("worker: run is in progress")
)

// Worker is the job consumer.
type Worker struct {
	deps Dependencies
	cfg  Config
	now  func() time.Time

	mu       sync.Mutex
	inflight map[string]bool
}

// New validates deps and builds a Worker.
func New(deps Dependencies, cfg Config) (*Worker, error) {
	switch {
	case deps.Queue == nil:
		return nil, errors.New("worker: queue is required")
	case deps.Registry == nil:
		return nil, errors.New("worker: registry is required")
	case deps.Executor == nil:
		return nil, errors.New("worker: executor is required")
	case deps.States == nil:
		return nil, errors.New("worker: state repository is required")
	case deps.Idempotency == nil:
		return nil, errors.New("worker: idempotency store is required")
	}
	if deps.Recorder == nil {
		deps.Recorder = intlog.Nop{}
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &Worker{deps: deps, cfg: cfg.withDefaults(), now: time.Now, inflight: make(map[string]bool)}, nil
}

// Run recovers interrupted runs, then consumes jobs until ctx is cancelled.
// It returns once every in-flight job has stopped.
func (w *Worker) Run(ctx context.Context) error {
	w.Recover(ctx, true)

	deliveries, err := w.deps.Queue.Consume(ctx)
	if err != nil {
		return fmt.Errorf("worker: consume: %w", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < w.cfg.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for d := range deliveries {
				w.handle(ctx, d)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(w.cfg.RecoveryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.Recover(ctx, false)
			}
		}
	}()
	wg.Wait()
	return nil
}

type outcome int

const (
	outcomeCompleted outcome = iota
	outcomeFailed
	outcomeInterrupted
	outcomeInfrastructure
)

// handle processes one delivery to a durable outcome.
func (w *Worker) handle(ctx context.Context, d queue.Delivery) {
	job := d.Job
	ctx = correlation.WithID(ctx, job.CorrelationID)
	log := w.deps.Logger.With(
		slog.String("correlation_id", job.CorrelationID),
		slog.String("job_id", job.ID),
		slog.String("event_type", job.EventType),
		slog.Int("delivery_attempt", job.Attempt),
	)

	var ev event.Event
	if err := json.Unmarshal(job.Payload, &ev); err != nil {
		log.Error("job payload is not an event; discarding", slog.String("error", err.Error()))
		w.markIdempotency(ctx, job.IdempotencyKey, idempotency.StatusFailed, log)
		w.ack(ctx, d, log)
		return
	}
	if err := event.Validate(ev); err != nil {
		log.Error("job event is invalid; discarding", slog.String("error", err.Error()))
		w.markIdempotency(ctx, ev.IdempotencyKey, idempotency.StatusFailed, log)
		w.ack(ctx, d, log)
		return
	}

	wf, ok := w.lookup(job)
	if !ok {
		reason := fmt.Sprintf("no workflow bound to event type %s", job.EventType)
		log.Warn("nothing to do for job", slog.String("reason", reason))
		w.deps.Recorder.Record(ctx, intlog.Entry{
			Timestamp:       w.now(),
			CorrelationID:   job.CorrelationID,
			Platform:        ev.Platform,
			IntegrationID:   ev.IntegrationID,
			ExternalOrderID: ev.ExternalOrderID,
			Action:          intlog.ActionWorkflowSkipped,
			Status:          intlog.StatusSkipped,
			Details:         map[string]any{"reason": reason, "event_type": ev.EventType},
		})
		w.markIdempotency(ctx, ev.IdempotencyKey, idempotency.StatusCompleted, log)
		w.ack(ctx, d, log)
		return
	}

	if !w.acquire(job.CorrelationID) {
		// A redelivery of a run this process is already executing. Wait for
		// it rather than bouncing the job through the queue, which would
		// burn delivery attempts; the state check below then acknowledges
		// a finished run or resumes an unfinished one.
		if !w.waitInflight(ctx, job.CorrelationID) || !w.acquire(job.CorrelationID) {
			log.Info("run still in progress; requeueing job")
			w.requeue(ctx, d, log)
			return
		}
	}
	defer w.release(job.CorrelationID)

	st, err := w.deps.States.Get(ctx, job.CorrelationID)
	switch {
	case errors.Is(err, workflow.ErrStateNotFound):
		st = workflow.NewState(wf.Name(), ev, job.ID, w.now())
	case err != nil:
		log.Error("load workflow state failed; requeueing", slog.String("error", err.Error()))
		w.requeue(ctx, d, log)
		return
	default:
		if st.Status.IsFinal() {
			log.Info("run already finished; acknowledging redelivery", slog.String("status", string(st.Status)))
			w.ack(ctx, d, log)
			return
		}
		if st.JobID == "" {
			st.JobID = job.ID
		}
	}

	switch w.execute(ctx, wf, st, ev.IdempotencyKey, log) {
	case outcomeCompleted, outcomeFailed:
		w.ack(ctx, d, log)
	case outcomeInterrupted:
		// Leave the job unacknowledged: the state is RUNNING on disk and
		// either recovery at the next start or a queue reclaim resumes it.
		log.Info("run interrupted by shutdown; leaving job pending")
	case outcomeInfrastructure:
		w.requeue(ctx, d, log)
	}
}

// execute runs the workflow and maintains the idempotency lifecycle.
func (w *Worker) execute(ctx context.Context, wf workflow.Workflow, st *workflow.State, idemKey string, log *slog.Logger) outcome {
	w.markIdempotency(ctx, idemKey, idempotency.StatusProcessing, log)

	err := w.deps.Executor.Run(ctx, wf, st)
	switch {
	case err == nil:
		w.markIdempotency(ctx, idemKey, idempotency.StatusCompleted, log)
		log.Info("workflow finished", slog.String("workflow", wf.Name()), slog.String("status", string(st.Status)))
		return outcomeCompleted
	case ctx.Err() != nil:
		return outcomeInterrupted
	case st.Status == workflow.StatusFailed:
		w.markIdempotency(ctx, idemKey, idempotency.StatusFailed, log)
		log.Warn("workflow failed; state persisted for resume",
			slog.String("workflow", wf.Name()),
			slog.String("next_action", st.NextAction),
			slog.String("error", err.Error()),
		)
		return outcomeFailed
	default:
		log.Error("workflow could not be executed", slog.String("workflow", wf.Name()), slog.String("error", err.Error()))
		return outcomeInfrastructure
	}
}

// Recover scans active runs and resumes those that were interrupted or
// failed transiently. At startup every RUNNING run is known to be abandoned;
// during periodic scans only stale ones are.
func (w *Worker) Recover(ctx context.Context, startup bool) {
	ids, err := w.deps.States.ListActive(ctx)
	if err != nil {
		w.deps.Logger.Error("recovery: list active runs failed", slog.String("error", err.Error()))
		return
	}
	if len(ids) == 0 {
		return
	}
	w.deps.Logger.Info("recovery: scanning active runs", slog.Int("count", len(ids)), slog.Bool("startup", startup))

	sem := make(chan struct{}, w.cfg.Concurrency)
	var wg sync.WaitGroup
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		st, err := w.deps.States.Get(ctx, id)
		if err != nil {
			w.deps.Logger.Error("recovery: load state failed", slog.String("correlation_id", id), slog.String("error", err.Error()))
			continue
		}
		reason, reset, ok := w.shouldResume(st, startup)
		if !ok {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(st *workflow.State, reason string, reset bool) {
			defer wg.Done()
			defer func() { <-sem }()
			w.resume(ctx, st, reason, reset)
		}(st, reason, reset)
	}
	wg.Wait()
}

// shouldResume decides whether a run is eligible for automatic resume.
func (w *Worker) shouldResume(st *workflow.State, startup bool) (reason string, reset bool, ok bool) {
	if w.isInflight(st.CorrelationID) {
		return "", false, false
	}
	age := w.now().Sub(st.UpdatedAt)
	switch st.Status {
	case workflow.StatusPending:
		return "run was never started", false, true
	case workflow.StatusRunning:
		if startup || age >= w.cfg.StaleRunningAfter {
			return "run was interrupted", false, true
		}
	case workflow.StatusFailed:
		if st.LastError != nil && st.LastError.Retryable && st.ResumeCount < w.cfg.MaxAutoResumes && age >= w.cfg.RetryFailedAfter {
			return "last error was transient", true, true
		}
	}
	return "", false, false
}

// resume runs a workflow from its persisted position.
func (w *Worker) resume(ctx context.Context, st *workflow.State, reason string, reset bool) {
	ctx = correlation.WithID(ctx, st.CorrelationID)
	log := w.deps.Logger.With(slog.String("correlation_id", st.CorrelationID), slog.String("workflow", st.WorkflowName))
	wf, ok := w.deps.Registry.ByName(st.WorkflowName)
	if !ok {
		log.Error("recovery: workflow is not registered; leaving run as is")
		return
	}
	if !w.acquire(st.CorrelationID) {
		return
	}
	defer w.release(st.CorrelationID)

	log.Info("resuming run", slog.String("reason", reason), slog.String("next_action", st.NextAction), slog.Int("resume_count", st.ResumeCount))
	if reset {
		st.ResetCurrentAttempts()
	}
	w.execute(ctx, wf, st, st.Event.IdempotencyKey, log)
}

// Resume restarts a run on an operator's request, granting the current
// action a fresh retry budget. It returns the executor's error when the run
// fails again.
func (w *Worker) Resume(ctx context.Context, correlationID string) error {
	st, err := w.deps.States.Get(ctx, correlationID)
	if err != nil {
		return err
	}
	if st.Status.IsFinal() {
		return ErrRunFinished
	}
	wf, ok := w.deps.Registry.ByName(st.WorkflowName)
	if !ok {
		return fmt.Errorf("worker: workflow %q is not registered", st.WorkflowName)
	}
	if !w.acquire(correlationID) {
		return ErrRunInProgress
	}
	defer w.release(correlationID)

	ctx = correlation.WithID(ctx, correlationID)
	log := w.deps.Logger.With(slog.String("correlation_id", correlationID), slog.String("workflow", st.WorkflowName))
	log.Info("resuming run on request", slog.String("next_action", st.NextAction))
	st.ResetCurrentAttempts()
	if w.execute(ctx, wf, st, st.Event.IdempotencyKey, log) == outcomeFailed {
		return fmt.Errorf("worker: run failed again at %s: %s", st.NextAction, st.LastError.String())
	}
	return nil
}

func (w *Worker) lookup(job queue.Job) (workflow.Workflow, bool) {
	if job.Workflow != "" {
		return w.deps.Registry.ByName(job.Workflow)
	}
	return w.deps.Registry.ForEvent(job.EventType)
}

func (w *Worker) markIdempotency(ctx context.Context, key string, status idempotency.Status, log *slog.Logger) {
	if key == "" {
		return
	}
	if err := w.deps.Idempotency.SetStatus(context.WithoutCancel(ctx), key, status); err != nil && !errors.Is(err, idempotency.ErrNotFound) {
		log.Warn("idempotency status update failed", slog.String("status", string(status)), slog.String("error", err.Error()))
	}
}

func (w *Worker) ack(ctx context.Context, d queue.Delivery, log *slog.Logger) {
	if err := d.Ack(context.WithoutCancel(ctx)); err != nil {
		log.Error("acknowledge job failed", slog.String("error", err.Error()))
	}
}

func (w *Worker) requeue(ctx context.Context, d queue.Delivery, log *slog.Logger) {
	if err := d.Requeue(context.WithoutCancel(ctx)); err != nil {
		log.Error("requeue job failed", slog.String("error", err.Error()))
	}
}

func (w *Worker) acquire(id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.inflight[id] {
		return false
	}
	w.inflight[id] = true
	return true
}

func (w *Worker) release(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.inflight, id)
}

func (w *Worker) isInflight(id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.inflight[id]
}

// inflightWait bounds how long a redelivery waits for the in-flight run.
const inflightWait = 10 * time.Second

// waitInflight reports true once id is no longer in flight, or false when
// the wait times out or ctx ends.
func (w *Worker) waitInflight(ctx context.Context, id string) bool {
	deadline := time.NewTimer(inflightWait)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if !w.isInflight(id) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-tick.C:
		}
	}
}
