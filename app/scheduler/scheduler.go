// Package scheduler publishes periodic work into the job queue.
//
// It exists because a dropship vendor cannot call us. Vinculum's published
// specification has no callback registration, so stock and dispatch have to
// be pulled on a schedule rather than pushed. That is assumption A2 of the
// integration plan; if BCPL later confirm push is available, a receiving
// endpoint is added and this keeps running as the backstop. A pushed event
// dropped during a deployment leaves an order with no tracking forever,
// whereas a sweep recovers by itself.
//
// The scheduler does no work of its own. A due job builds queue jobs and
// publishes them, so scheduled work travels the same queue, worker, retry,
// resume and execution-log path as a webhook event. Nothing bypasses the
// log, and there is no second execution engine to reason about.
//
// Two problems the naive version gets wrong:
//
//   - Several replicas run this process. A timer in each would fire the same
//     sweep once per replica.
//   - A tick missed because the process was down must not be skipped. Work
//     is bounded by a watermark, not by "whatever happened since I woke up".
//
// Both are handled by the state row: the claim and the watermark are the
// same row, updated atomically. The Redis lock in lock.go then keeps a long
// run from being overlapped by the next tick on another replica.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/gluzo/integration-gateway/app/correlation"
	"github.com/gluzo/integration-gateway/app/queue"
)

// Defaults.
const (
	// DefaultPollInterval is how often the loop looks for due jobs. It is
	// not a job's interval: a job due every six hours is still checked every
	// poll, because a replica that has just started must not wait six hours
	// to discover the work is overdue.
	DefaultPollInterval = 30 * time.Second

	// DefaultLockTTL bounds how long a crashed replica can keep other
	// replicas out of a job. It must exceed a normal run and stay well under
	// the shortest interval.
	DefaultLockTTL = 5 * time.Minute

	// DefaultInitialLookback is the window of the very first run of a job
	// that has no watermark yet. Sweeping from the beginning of time would
	// ask the vendor for their entire history on first boot.
	DefaultInitialLookback = time.Hour
)

// Window is the period a scheduled run covers. Since is the watermark the
// previous successful run reached, Until is where this run stops.
type Window struct {
	Since time.Time
	Until time.Time
}

// Duration reports the length of the window.
func (w Window) Duration() time.Duration { return w.Until.Sub(w.Since) }

// String renders the window for logs.
func (w Window) String() string {
	return w.Since.UTC().Format(time.RFC3339) + ".." + w.Until.UTC().Format(time.RFC3339)
}

// Job is a named unit of periodic work.
type Job struct {
	// Name identifies the job in the state table, the Redis lock and the
	// log. It is configuration, not a display string: renaming it starts a
	// new watermark.
	Name string

	// Interval is how often the job runs.
	Interval time.Duration

	// MaxWindow caps how much time one run may cover. After a long outage
	// the window would otherwise widen to the whole outage and ask the
	// vendor for everything at once. A capped run does not lose the
	// remainder: the watermark advances only as far as the run covered, and
	// the job becomes due again immediately, so it catches up in chunks.
	// Zero means no cap.
	MaxWindow time.Duration

	// InitialLookback is the window of the first run, before a watermark
	// exists. Zero uses DefaultInitialLookback.
	InitialLookback time.Duration

	// Build renders the queue jobs for a window. It is called only on the
	// replica that won the tick.
	//
	// Returning no jobs is success: a window with nothing in it is the
	// ordinary case for a quiet period, and the watermark still advances.
	// Returning an error leaves the watermark where it was, so the next run
	// covers this window again.
	Build func(ctx context.Context, w Window) ([]queue.Job, error)
}

// lookback reports the window of a job's first run.
func (j Job) lookback() time.Duration {
	if j.InitialLookback > 0 {
		return j.InitialLookback
	}
	return DefaultInitialLookback
}

// Validate reports whether the job can be registered.
func (j Job) Validate() error {
	var errs []error
	if strings.TrimSpace(j.Name) == "" {
		errs = append(errs, errors.New("scheduler: job name is required"))
	}
	if j.Interval <= 0 {
		errs = append(errs, errors.New("scheduler: job interval must be positive"))
	}
	if j.MaxWindow < 0 {
		errs = append(errs, errors.New("scheduler: max window must not be negative"))
	}
	if j.Build == nil {
		errs = append(errs, errors.New("scheduler: job has no Build function"))
	}
	return errors.Join(errs...)
}

// Scheduler runs registered jobs against a shared state store.
type Scheduler struct {
	store  Store
	locker Locker
	pub    queue.Publisher
	logger *slog.Logger

	jobs    []Job
	poll    time.Duration
	lockTTL time.Duration
	now     func() time.Time
}

// Option configures a Scheduler.
type Option func(*Scheduler)

// WithPollInterval sets how often the loop looks for due jobs.
func WithPollInterval(d time.Duration) Option {
	return func(s *Scheduler) {
		if d > 0 {
			s.poll = d
		}
	}
}

// WithLockTTL sets how long a run holds its lock.
func WithLockTTL(d time.Duration) Option {
	return func(s *Scheduler) {
		if d > 0 {
			s.lockTTL = d
		}
	}
}

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option {
	return func(s *Scheduler) {
		if l != nil {
			s.logger = l
		}
	}
}

// WithClock replaces the clock; intended for tests.
func WithClock(fn func() time.Time) Option {
	return func(s *Scheduler) {
		if fn != nil {
			s.now = fn
		}
	}
}

// New builds a Scheduler. A nil locker runs without mutual exclusion, which
// is correct only for a single replica.
func New(store Store, locker Locker, pub queue.Publisher, opts ...Option) (*Scheduler, error) {
	if store == nil {
		return nil, errors.New("scheduler: a state store is required")
	}
	if pub == nil {
		return nil, errors.New("scheduler: a queue publisher is required")
	}
	s := &Scheduler{
		store:   store,
		locker:  locker,
		pub:     pub,
		logger:  slog.Default(),
		poll:    DefaultPollInterval,
		lockTTL: DefaultLockTTL,
		now:     time.Now,
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.locker == nil {
		s.locker = NopLocker{}
	}
	return s, nil
}

// Register adds a job. Its state row is created if it does not exist, due
// one interval from now: a freshly deployed job should not fire during the
// deployment itself.
func (s *Scheduler) Register(ctx context.Context, job Job) error {
	if err := job.Validate(); err != nil {
		return err
	}
	for _, existing := range s.jobs {
		if existing.Name == job.Name {
			return fmt.Errorf("scheduler: job %q is already registered", job.Name)
		}
	}
	now := s.now().UTC()
	if err := s.store.Ensure(ctx, job.Name, now.Add(job.Interval), now.Add(-job.lookback())); err != nil {
		return err
	}
	s.jobs = append(s.jobs, job)
	return nil
}

// Jobs reports the registered job names.
func (s *Scheduler) Jobs() []string {
	names := make([]string, 0, len(s.jobs))
	for _, j := range s.jobs {
		names = append(names, j.Name)
	}
	return names
}

// Run polls for due jobs until ctx is cancelled.
//
// A failing job is logged and the loop continues: one vendor being
// unreachable must not stop every other schedule.
func (s *Scheduler) Run(ctx context.Context) {
	if len(s.jobs) == 0 {
		s.logger.Info("scheduler has no jobs registered; nothing periodic will run")
		return
	}
	s.logger.Info("scheduler started",
		slog.Any("jobs", s.Jobs()),
		slog.Duration("poll", s.poll))

	ticker := time.NewTicker(s.poll)
	defer ticker.Stop()
	for {
		s.RunDue(ctx)
		select {
		case <-ctx.Done():
			s.logger.Info("scheduler stopped")
			return
		case <-ticker.C:
		}
	}
}

// RunDue runs one pass over every registered job and reports how many fired.
// It is the unit a test drives directly.
func (s *Scheduler) RunDue(ctx context.Context) int {
	fired := 0
	for _, job := range s.jobs {
		if ctx.Err() != nil {
			return fired
		}
		ran, err := s.runJob(ctx, job)
		if err != nil {
			s.logger.ErrorContext(ctx, "scheduled job failed",
				slog.String("job", job.Name),
				slog.String("error", err.Error()))
		}
		if ran {
			fired++
		}
	}
	return fired
}

// runJob claims, locks, builds and publishes one job's tick.
//
// The claim comes first and the lock second, which is the deliberate order.
// The claim is the record of "this tick is taken" and lives in the same row
// as the watermark, so the two can never disagree; the lock only keeps a
// slow run from being overlapped. Taking the lock first would mean a replica
// that crashed after locking but before claiming leaves no trace of the
// attempt.
func (s *Scheduler) runJob(ctx context.Context, job Job) (bool, error) {
	now := s.now().UTC()

	state, claimed, err := s.store.Claim(ctx, job.Name, now, job.Interval)
	if err != nil {
		return false, fmt.Errorf("claim %s: %w", job.Name, err)
	}
	if !claimed {
		return false, nil
	}

	release, locked, err := s.locker.Acquire(ctx, job.Name, s.lockTTL)
	if err != nil {
		return false, fmt.Errorf("lock %s: %w", job.Name, err)
	}
	if !locked {
		// Another replica is still inside the previous run. The claim has
		// already moved this job's due time on, so the tick is skipped
		// rather than queued behind the run in progress; the watermark is
		// untouched, so the next run covers this window too.
		s.logger.InfoContext(ctx, "scheduled job skipped: the previous run is still in progress",
			slog.String("job", job.Name))
		return false, nil
	}
	defer release(context.WithoutCancel(ctx))

	window, capped := s.window(job, state.Watermark, now)

	jobs, err := job.Build(ctx, window)
	if err != nil {
		// The watermark stays where it was, so this window is covered again
		// on the next run rather than lost.
		return false, fmt.Errorf("build %s for %s: %w", job.Name, window, err)
	}

	published := 0
	for i, qj := range jobs {
		s.stamp(&qj, job, window, now, i)
		if err := qj.Validate(); err != nil {
			return false, fmt.Errorf("build %s for %s: %w", job.Name, window, err)
		}
		if err := s.pub.Publish(ctx, qj); err != nil {
			// Publishing is not transactional, so the jobs already on the
			// queue stay there and the watermark does not advance. The
			// window is republished next run; scheduled workflows are
			// idempotent precisely so that this is safe.
			return false, fmt.Errorf("publish %s (%d of %d) for %s: %w",
				job.Name, published+1, len(jobs), window, err)
		}
		published++
	}

	nextDue := state.NextDueAt
	if capped {
		// The run stopped short of now, so there is more to catch up on.
		// Waiting a full interval would make the backlog drain in real time
		// rather than faster than it accumulated.
		nextDue = now
	}
	if err := s.store.Complete(ctx, job.Name, window.Until, nextDue, now); err != nil {
		// The work is published and will run. Only the watermark failed to
		// advance, so the next run repeats this window.
		return true, fmt.Errorf("record completion of %s: %w", job.Name, err)
	}

	s.logger.InfoContext(ctx, "scheduled job published",
		slog.String("job", job.Name),
		slog.String("window", window.String()),
		slog.Int("jobs", published),
		slog.Bool("catching_up", capped))
	return true, nil
}

// window computes the period this run covers.
func (s *Scheduler) window(job Job, watermark, now time.Time) (Window, bool) {
	since := watermark.UTC()
	if since.IsZero() {
		// Register sets the watermark, so this is only reached for a row
		// written before that was true. Falling back to the lookback covers
		// the recent past rather than all of history.
		since = now.Add(-job.lookback())
	}
	if since.After(now) {
		// A watermark ahead of the clock means someone moved the clock back
		// or edited the row. Covering a negative window would be nonsense;
		// covering from now is the smallest safe reading.
		since = now
	}

	until := now
	capped := false
	if job.MaxWindow > 0 && until.Sub(since) > job.MaxWindow {
		until = since.Add(job.MaxWindow)
		capped = true
	}
	return Window{Since: since, Until: until}, capped
}

// stamp fills the fields a queue job needs that a Build function should not
// have to care about.
func (s *Scheduler) stamp(qj *queue.Job, job Job, w Window, now time.Time, index int) {
	if qj.ID == "" {
		qj.ID = correlation.New()
	}
	if qj.CorrelationID == "" {
		qj.CorrelationID = qj.ID
	}
	if qj.EnqueuedAt.IsZero() {
		qj.EnqueuedAt = now
	}
	if qj.IdempotencyKey == "" {
		// One key per job name, window end and target, so a window
		// republished after a partial failure is recognised as a duplicate
		// rather than executed twice.
		//
		// A Build function that fans out on anything other than the
		// integration should set its own key: the positional fallback is
		// only stable while the fan-out produces the same jobs in the same
		// order.
		target := strings.TrimSpace(qj.IntegrationID)
		if target == "" {
			target = strconv.Itoa(index)
		}
		qj.IdempotencyKey = fmt.Sprintf("scheduler:%s:%d:%s", job.Name, w.Until.UTC().Unix(), target)
	}
}
