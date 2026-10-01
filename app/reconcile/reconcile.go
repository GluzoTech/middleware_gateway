// Package reconcile finds work the gateway has stopped making progress on.
//
// Every other safety net in the gateway is automatic: retries, auto-resume,
// dead-lettering, the nightly full stock push. Each of them handles a failure
// it recognises. This package exists for the ones nothing recognised — a run
// that failed permanently, a run whose worker died mid-action, a vendor
// rejection nobody has looked at.
//
// A rare failure that is invisible is worse than a common one: a common
// failure gets noticed and fixed, while a rare invisible one is discovered by
// a customer. So this finds nothing most of the time, and that is the point.
package reconcile

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/workflow"
)

// Reason classifies why a run is not progressing. It is what tells an
// operator whether to wait, to resume, or to fix configuration.
type Reason string

// Reasons.
const (
	// ReasonAwaitingRetry means the run failed with a transient error and
	// still has auto-resume budget. Nothing to do: it is expected to
	// recover on its own. Reported only when it has been waiting far longer
	// than it should.
	ReasonAwaitingRetry Reason = "AWAITING_RETRY"
	// ReasonPermanentFailure means the run failed with an error that will
	// not resolve itself, or exhausted its auto-resume budget. An operator
	// must act.
	ReasonPermanentFailure Reason = "PERMANENT_FAILURE"
	// ReasonAbandoned means the run is still marked RUNNING but has not
	// been touched: the worker holding it died, and recovery has not
	// reclaimed it.
	ReasonAbandoned Reason = "ABANDONED"
	// ReasonNeverStarted means the run was created and no action ever ran.
	ReasonNeverStarted Reason = "NEVER_STARTED"
)

// Finding is one run that is not progressing.
type Finding struct {
	CorrelationID string
	Workflow      string
	Status        workflow.Status
	Reason        Reason

	// Action is where the run stopped, which is usually the first thing an
	// operator wants to know.
	Action string

	IntegrationName string
	OriginPlatform  string
	VendorPlatform  string
	ExternalOrderID string

	// StuckFor is how long since the run was last touched.
	StuckFor  time.Duration
	UpdatedAt time.Time

	ResumeCount int
	// LastError carries the vendor's own code and message where there was
	// one. Preserving it is the difference between "the order did not go
	// through" and "the vendor said the SKU does not belong to this
	// location".
	LastError *apperror.Info
}

// NeedsOperator reports whether the finding will not resolve itself.
func (f Finding) NeedsOperator() bool {
	return f.Reason == ReasonPermanentFailure || f.Reason == ReasonAbandoned
}

// VendorMessage returns the vendor's own words, where the failure had any.
func (f Finding) VendorMessage() string {
	if f.LastError == nil {
		return ""
	}
	parts := make([]string, 0, 2)
	if code := strings.TrimSpace(f.LastError.ExternalCode); code != "" {
		parts = append(parts, code)
	}
	if msg := strings.TrimSpace(f.LastError.ExternalMessage); msg != "" {
		parts = append(parts, msg)
	}
	return strings.Join(parts, ": ")
}

// Options tune a scan.
type Options struct {
	// Threshold is how long a run may sit untouched before it is reported.
	// It must exceed the worker's retry and recovery intervals, or every
	// run in normal retry would be reported as stuck.
	Threshold time.Duration
	// MaxAutoResumes mirrors the worker's budget, so a run that will still
	// be resumed automatically is classified as awaiting retry rather than
	// as a permanent failure.
	MaxAutoResumes int
	// Limit caps how many findings are returned. Zero means all of them.
	Limit int
}

// DefaultThreshold is a conservative default: long enough that ordinary
// retries and auto-resumes have had their chance.
const DefaultThreshold = 30 * time.Minute

// Scanner reads workflow state and reports runs that are not progressing.
type Scanner struct {
	states workflow.Repository
	opts   Options
	now    func() time.Time
}

// NewScanner builds a Scanner.
func NewScanner(states workflow.Repository, opts Options) (*Scanner, error) {
	if states == nil {
		return nil, errors.New("reconcile: a state repository is required")
	}
	if opts.Threshold <= 0 {
		opts.Threshold = DefaultThreshold
	}
	if opts.MaxAutoResumes < 0 {
		opts.MaxAutoResumes = 0
	}
	return &Scanner{states: states, opts: opts, now: time.Now}, nil
}

// SetClock replaces the clock; intended for tests.
func (s *Scanner) SetClock(fn func() time.Time) {
	if fn != nil {
		s.now = fn
	}
}

// Threshold reports the configured threshold, for display.
func (s *Scanner) Threshold() time.Duration { return s.opts.Threshold }

// Scan returns every run that is not progressing, most stuck first.
//
// A state that cannot be read is skipped rather than failing the scan: one
// corrupt file must not hide every other finding, which is exactly when a
// reconciliation sweep is most needed.
func (s *Scanner) Scan(ctx context.Context) ([]Finding, error) {
	ids, err := s.states.ListActive(ctx)
	if err != nil {
		return nil, err
	}

	now := s.now().UTC()
	findings := make([]Finding, 0)
	for _, id := range ids {
		if ctx.Err() != nil {
			return findings, ctx.Err()
		}
		state, err := s.states.Get(ctx, id)
		if err != nil || state == nil {
			continue
		}
		if f, ok := s.assess(state, now); ok {
			findings = append(findings, f)
		}
	}

	sort.Slice(findings, func(i, j int) bool {
		return findings[i].StuckFor > findings[j].StuckFor
	})
	if s.opts.Limit > 0 && len(findings) > s.opts.Limit {
		findings = findings[:s.opts.Limit]
	}
	return findings, nil
}

// assess decides whether one run counts as stuck.
func (s *Scanner) assess(state *workflow.State, now time.Time) (Finding, bool) {
	if state.Status.IsFinal() {
		return Finding{}, false
	}
	stuckFor := now.Sub(state.UpdatedAt.UTC())
	if stuckFor < s.opts.Threshold {
		return Finding{}, false
	}

	f := Finding{
		CorrelationID:   state.CorrelationID,
		Workflow:        state.WorkflowName,
		Status:          state.Status,
		Reason:          s.reason(state),
		Action:          currentAction(state),
		ExternalOrderID: state.Event.ExternalOrderID,
		OriginPlatform:  state.Platform,
		StuckFor:        stuckFor,
		UpdatedAt:       state.UpdatedAt.UTC(),
		ResumeCount:     state.ResumeCount,
		LastError:       state.LastError,
	}
	if state.Route != nil {
		f.IntegrationName = state.Route.IntegrationName
		f.VendorPlatform = state.Route.DestinationPlatform
		if f.OriginPlatform == "" {
			f.OriginPlatform = state.Route.SourcePlatform
		}
	}
	return f, true
}

// reason classifies the run, mirroring the worker's own auto-resume rule so
// that a run the worker will still pick up is not reported as needing an
// operator.
func (s *Scanner) reason(state *workflow.State) Reason {
	switch state.Status {
	case workflow.StatusPending:
		return ReasonNeverStarted
	case workflow.StatusRunning:
		return ReasonAbandoned
	case workflow.StatusFailed:
		if state.LastError != nil && state.LastError.Retryable && state.ResumeCount < s.opts.MaxAutoResumes {
			return ReasonAwaitingRetry
		}
		return ReasonPermanentFailure
	default:
		return ReasonPermanentFailure
	}
}

// currentAction names where the run stopped.
func currentAction(state *workflow.State) string {
	if state.NextAction != "" {
		return state.NextAction
	}
	if state.CurrentAction >= 0 && state.CurrentAction < len(state.Actions) {
		return state.Actions[state.CurrentAction].Name
	}
	if state.LastSuccessfulAction != "" {
		return "after " + state.LastSuccessfulAction
	}
	return ""
}

// Summary counts findings by reason, for a log line an operator can scan.
type Summary struct {
	Total          int
	NeedingAction  int
	ByReason       map[Reason]int
	OldestStuckFor time.Duration
}

// Summarise builds a Summary from findings.
func Summarise(findings []Finding) Summary {
	s := Summary{ByReason: make(map[Reason]int, 4)}
	for _, f := range findings {
		s.Total++
		s.ByReason[f.Reason]++
		if f.NeedsOperator() {
			s.NeedingAction++
		}
		if f.StuckFor > s.OldestStuckFor {
			s.OldestStuckFor = f.StuckFor
		}
	}
	return s
}
