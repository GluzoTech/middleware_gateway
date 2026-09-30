package reconcile_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/event"
	"github.com/gluzo/integration-gateway/app/intlog"
	"github.com/gluzo/integration-gateway/app/reconcile"
	"github.com/gluzo/integration-gateway/app/workflow"
	"github.com/gluzo/integration-gateway/app/workflowstate"
)

var now = time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// run builds a state as the engine would have left it.
func run(id string, status workflow.Status, age time.Duration, opts ...func(*workflow.State)) *workflow.State {
	st := &workflow.State{
		CorrelationID: id,
		WorkflowName:  "ORDER_SYNC",
		Status:        status,
		Platform:      "easyecom",
		UpdatedAt:     now.Add(-age),
		CreatedAt:     now.Add(-age),
		Event:         event.Event{ExternalOrderID: "9876543"},
		Route: &workflow.RouteInfo{
			IntegrationName:     "easyecom-vinculum",
			SourcePlatform:      "easyecom",
			DestinationPlatform: "vinculum",
		},
	}
	for _, opt := range opts {
		opt(st)
	}
	return st
}

func withError(info *apperror.Info) func(*workflow.State) {
	return func(st *workflow.State) { st.LastError = info }
}

func withNextAction(name string) func(*workflow.State) {
	return func(st *workflow.State) { st.NextAction = name }
}

func withResumes(n int) func(*workflow.State) {
	return func(st *workflow.State) { st.ResumeCount = n }
}

func newScanner(t *testing.T, opts reconcile.Options, states ...*workflow.State) *reconcile.Scanner {
	t.Helper()
	repo := workflowstate.NewMemoryRepository()
	for _, st := range states {
		if err := repo.Save(context.Background(), st); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	s, err := reconcile.NewScanner(repo, opts)
	if err != nil {
		t.Fatalf("NewScanner: %v", err)
	}
	s.SetClock(func() time.Time { return now })
	return s
}

// The Phase 7 exit criterion: an order stuck at the vendor appears within the
// threshold, carrying the vendor's own words.
func TestAnOrderStuckAtTheVendorIsReportedWithTheVendorsOwnError(t *testing.T) {
	vendorRejection := &apperror.Info{
		Category:        apperror.ExternalAPI,
		Message:         "vinculum rejected the request",
		Retryable:       false,
		Integration:     "vinculum",
		Operation:       "CreateOrder",
		ExternalCode:    "120",
		ExternalMessage: "location is closed for dispatch",
	}
	s := newScanner(t, reconcile.Options{Threshold: 30 * time.Minute, MaxAutoResumes: 3},
		run("INT-STUCK", workflow.StatusFailed, time.Hour,
			withError(vendorRejection), withNextAction("SUBMIT_VENDOR_ORDER")))

	findings, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1", len(findings))
	}

	f := findings[0]
	if f.CorrelationID != "INT-STUCK" {
		t.Errorf("correlation = %q", f.CorrelationID)
	}
	if f.Reason != reconcile.ReasonPermanentFailure {
		t.Errorf("reason = %q, want a permanent failure", f.Reason)
	}
	if !f.NeedsOperator() {
		t.Error("a permanent failure needs an operator")
	}
	if f.Action != "SUBMIT_VENDOR_ORDER" {
		t.Errorf("stopped at %q", f.Action)
	}
	// "The order did not go through" is not actionable; "location is closed
	// for dispatch" is. The vendor's own words must survive.
	if got := f.VendorMessage(); got != "120: location is closed for dispatch" {
		t.Errorf("vendor message = %q", got)
	}
	if f.VendorPlatform != "vinculum" || f.ExternalOrderID != "9876543" {
		t.Errorf("finding = %+v", f)
	}
}

func TestRunsYoungerThanTheThresholdAreNotReported(t *testing.T) {
	s := newScanner(t, reconcile.Options{Threshold: 30 * time.Minute},
		run("INT-FRESH", workflow.StatusFailed, 5*time.Minute, withError(&apperror.Info{Retryable: true})))

	findings, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("reported %d findings inside the threshold: %+v", len(findings), findings)
	}
}

func TestCompletedAndSkippedRunsAreNeverReported(t *testing.T) {
	s := newScanner(t, reconcile.Options{Threshold: time.Minute},
		run("INT-DONE", workflow.StatusCompleted, 10*time.Hour),
		run("INT-SKIP", workflow.StatusSkipped, 10*time.Hour))

	findings, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("reported finished runs: %+v", findings)
	}
}

// A run the worker will still resume by itself is not something an operator
// should be woken for.
func TestARunAwaitingItsOwnRetryIsNotFlaggedForAnOperator(t *testing.T) {
	transient := &apperror.Info{Category: apperror.ExternalAPI, Message: "vendor unavailable", Retryable: true}
	s := newScanner(t, reconcile.Options{Threshold: 30 * time.Minute, MaxAutoResumes: 3},
		run("INT-RETRY", workflow.StatusFailed, time.Hour, withError(transient), withResumes(1)))

	findings, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1: it is still reported, just not as urgent", len(findings))
	}
	if findings[0].Reason != reconcile.ReasonAwaitingRetry {
		t.Errorf("reason = %q, want awaiting retry", findings[0].Reason)
	}
	if findings[0].NeedsOperator() {
		t.Error("a run inside its auto-resume budget does not need an operator")
	}
}

// Once the budget is spent, the same error does need one.
func TestAnExhaustedRetryBudgetBecomesAPermanentFailure(t *testing.T) {
	transient := &apperror.Info{Category: apperror.ExternalAPI, Message: "vendor unavailable", Retryable: true}
	s := newScanner(t, reconcile.Options{Threshold: 30 * time.Minute, MaxAutoResumes: 3},
		run("INT-SPENT", workflow.StatusFailed, time.Hour, withError(transient), withResumes(3)))

	findings, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(findings) != 1 || findings[0].Reason != reconcile.ReasonPermanentFailure {
		t.Fatalf("findings = %+v, want a permanent failure", findings)
	}
	if !findings[0].NeedsOperator() {
		t.Error("an exhausted budget needs an operator")
	}
}

func TestAnAbandonedRunIsReported(t *testing.T) {
	s := newScanner(t, reconcile.Options{Threshold: 30 * time.Minute},
		// Still RUNNING and untouched: the worker holding it died and
		// recovery has not reclaimed it.
		run("INT-ABANDONED", workflow.StatusRunning, 2*time.Hour))

	findings, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(findings) != 1 || findings[0].Reason != reconcile.ReasonAbandoned {
		t.Fatalf("findings = %+v, want abandoned", findings)
	}
	if !findings[0].NeedsOperator() {
		t.Error("an abandoned run needs an operator")
	}
}

func TestFindingsAreOrderedMostStuckFirst(t *testing.T) {
	s := newScanner(t, reconcile.Options{Threshold: time.Minute},
		run("INT-1H", workflow.StatusFailed, time.Hour),
		run("INT-3H", workflow.StatusFailed, 3*time.Hour),
		run("INT-2H", workflow.StatusFailed, 2*time.Hour))

	findings, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := []string{"INT-3H", "INT-2H", "INT-1H"}
	if len(findings) != len(want) {
		t.Fatalf("got %d findings", len(findings))
	}
	for i, id := range want {
		if findings[i].CorrelationID != id {
			t.Errorf("position %d = %q, want %q", i, findings[i].CorrelationID, id)
		}
	}
}

func TestLimitCapsTheReport(t *testing.T) {
	s := newScanner(t, reconcile.Options{Threshold: time.Minute, Limit: 2},
		run("INT-1", workflow.StatusFailed, time.Hour),
		run("INT-2", workflow.StatusFailed, 2*time.Hour),
		run("INT-3", workflow.StatusFailed, 3*time.Hour))

	findings, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("got %d findings, want the limit of 2", len(findings))
	}
	// The cap keeps the most stuck, not an arbitrary two.
	if findings[0].CorrelationID != "INT-3" || findings[1].CorrelationID != "INT-2" {
		t.Errorf("kept %q and %q", findings[0].CorrelationID, findings[1].CorrelationID)
	}
}

func TestSummarise(t *testing.T) {
	findings := []reconcile.Finding{
		{Reason: reconcile.ReasonPermanentFailure, StuckFor: 3 * time.Hour},
		{Reason: reconcile.ReasonAwaitingRetry, StuckFor: time.Hour},
		{Reason: reconcile.ReasonAbandoned, StuckFor: 2 * time.Hour},
	}
	s := reconcile.Summarise(findings)
	if s.Total != 3 {
		t.Errorf("total = %d", s.Total)
	}
	// Awaiting retry is not counted: it resolves itself.
	if s.NeedingAction != 2 {
		t.Errorf("needing action = %d, want 2", s.NeedingAction)
	}
	if s.OldestStuckFor != 3*time.Hour {
		t.Errorf("oldest = %s", s.OldestStuckFor)
	}
}

func TestNewScannerRequiresARepository(t *testing.T) {
	if _, err := reconcile.NewScanner(nil, reconcile.Options{}); err == nil {
		t.Error("expected an error")
	}
}

// failingRepo lists ids it cannot then read.
type failingRepo struct {
	workflow.Repository
	ids []string
}

func (f failingRepo) ListActive(context.Context) ([]string, error) { return f.ids, nil }
func (f failingRepo) Get(_ context.Context, id string) (*workflow.State, error) {
	if id == "INT-CORRUPT" {
		return nil, errors.New("state file is unreadable")
	}
	return run(id, workflow.StatusFailed, time.Hour), nil
}

// One unreadable state must not hide every other finding — which is exactly
// when a reconciliation sweep matters most.
func TestAnUnreadableStateDoesNotHideTheRest(t *testing.T) {
	s, err := reconcile.NewScanner(failingRepo{ids: []string{"INT-CORRUPT", "INT-OK"}}, reconcile.Options{Threshold: time.Minute})
	if err != nil {
		t.Fatalf("NewScanner: %v", err)
	}
	s.SetClock(func() time.Time { return now })

	findings, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(findings) != 1 || findings[0].CorrelationID != "INT-OK" {
		t.Fatalf("findings = %+v, want the readable one", findings)
	}
}

func TestAlerterRecordsOneEntryPerFinding(t *testing.T) {
	recorder := &intlog.Memory{}
	s := newScanner(t, reconcile.Options{Threshold: 30 * time.Minute, MaxAutoResumes: 3},
		run("INT-STUCK", workflow.StatusFailed, time.Hour,
			withError(&apperror.Info{ExternalCode: "120", ExternalMessage: "location is closed"}),
			withNextAction("SUBMIT_VENDOR_ORDER")))

	alerter := reconcile.NewAlerter(s, recorder, quiet())
	alerter.SetClock(func() time.Time { return now })

	findings, err := alerter.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("got %d findings", len(findings))
	}

	entries := recorder.Entries()
	if len(entries) != 1 {
		t.Fatalf("recorded %d entries, want one per finding", len(entries))
	}
	e := entries[0]
	// Recorded under the run's own correlation ID so a stuck order is
	// searchable alongside everything else that happened to it.
	if e.CorrelationID != "INT-STUCK" {
		t.Errorf("correlation = %q", e.CorrelationID)
	}
	if e.Action != reconcile.ActionReconcileStuck || e.Status != intlog.StatusFailed {
		t.Errorf("entry = %s / %s", e.Action, e.Status)
	}
	if got := e.Details["vendor_error"]; got != "120: location is closed" {
		t.Errorf("vendor_error = %v", got)
	}
	if got := e.Details["needs_operator"]; got != true {
		t.Errorf("needs_operator = %v", got)
	}
}

func TestAlerterRecordsNothingWhenNothingIsStuck(t *testing.T) {
	recorder := &intlog.Memory{}
	s := newScanner(t, reconcile.Options{Threshold: time.Hour},
		run("INT-FRESH", workflow.StatusFailed, time.Minute))

	alerter := reconcile.NewAlerter(s, recorder, quiet())
	findings, err := alerter.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("findings = %+v", findings)
	}
	// Finding nothing is the expected outcome and must not fill the log.
	if got := len(recorder.Entries()); got != 0 {
		t.Errorf("recorded %d entries when nothing was stuck", got)
	}
}
