package reconcile

import (
	"context"
	"log/slog"
	"time"

	"github.com/gluzo/integration-gateway/app/intlog"
)

// Alerter runs a scan and makes what it finds visible.
//
// "Visible" means two things, because they serve different readers: a log
// line an operator or an alerting rule can watch, and an execution-log entry
// per finding so a stuck order is searchable by its own correlation ID
// alongside everything else that happened to it.
//
// It deliberately does not resume anything. Reconciliation reports; acting on
// a permanent failure is a decision, and a sweep that quietly retried them
// would hide the pattern it exists to reveal.
type Alerter struct {
	scanner  *Scanner
	recorder intlog.Recorder
	logger   *slog.Logger
	now      func() time.Time
}

// NewAlerter builds an Alerter. A nil recorder logs without writing entries.
func NewAlerter(scanner *Scanner, recorder intlog.Recorder, logger *slog.Logger) *Alerter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Alerter{scanner: scanner, recorder: recorder, logger: logger, now: time.Now}
}

// SetClock replaces the clock; intended for tests.
func (a *Alerter) SetClock(fn func() time.Time) {
	if fn != nil {
		a.now = fn
	}
}

// Run performs one scan and reports it. It returns the findings so a caller
// can act on them; the reporting has already happened.
func (a *Alerter) Run(ctx context.Context) ([]Finding, error) {
	findings, err := a.scanner.Scan(ctx)
	if err != nil {
		a.logger.ErrorContext(ctx, "reconciliation scan failed", slog.String("error", err.Error()))
		return nil, err
	}

	summary := Summarise(findings)
	if summary.Total == 0 {
		// Logged at debug because finding nothing is the expected outcome
		// and an operator should not have to filter it out to see a real
		// finding.
		a.logger.DebugContext(ctx, "reconciliation found nothing stuck",
			slog.Duration("threshold", a.scanner.Threshold()))
		return nil, nil
	}

	a.logger.WarnContext(ctx, "reconciliation found runs that are not progressing",
		slog.Int("total", summary.Total),
		slog.Int("needing_action", summary.NeedingAction),
		slog.Duration("oldest_stuck_for", summary.OldestStuckFor),
		slog.Duration("threshold", a.scanner.Threshold()),
		slog.Any("by_reason", summary.ByReason))

	for _, f := range findings {
		a.record(ctx, f)
	}
	return findings, nil
}

// record writes one execution-log entry per finding, so a stuck order is
// searchable by the same correlation ID as the rest of its history.
func (a *Alerter) record(ctx context.Context, f Finding) {
	if a.recorder == nil {
		return
	}
	details := map[string]any{
		"reason":         string(f.Reason),
		"stuck_for":      f.StuckFor.String(),
		"stuck_since":    f.UpdatedAt.Format(time.RFC3339),
		"stopped_at":     f.Action,
		"needs_operator": f.NeedsOperator(),
		"resume_count":   f.ResumeCount,
	}
	if msg := f.VendorMessage(); msg != "" {
		// The vendor's own words, kept verbatim. "The vendor rejected it"
		// is not an actionable report; "SKU does not belong to this
		// location" is.
		details["vendor_error"] = msg
	}

	a.recorder.Record(ctx, intlog.Entry{
		Timestamp:       a.now().UTC(),
		CorrelationID:   f.CorrelationID,
		Workflow:        f.Workflow,
		Platform:        f.OriginPlatform,
		Integration:     f.VendorPlatform,
		ExternalOrderID: f.ExternalOrderID,
		Action:          ActionReconcileStuck,
		Status:          intlog.StatusFailed,
		Error:           f.LastError,
		Details:         details,
	})
}

// ActionReconcileStuck names the reconciliation finding in the execution log.
const ActionReconcileStuck = "RECONCILE_STUCK"
