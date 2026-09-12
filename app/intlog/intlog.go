// Package intlog defines the integration execution log: one append-only
// entry per meaningful step of processing an event (webhook received, each
// action attempt, workflow outcome).
//
// This package holds the entry schema and the Recorder contract so that the
// webhook handler and the workflow engine can emit entries without depending
// on how they are stored. The JSONL writer, sanitiser and retention live in
// the same package tree and are wired in at startup.
package intlog

import (
	"context"
	"sync"
	"time"

	"github.com/gluzo/integration-gateway/app/apperror"
)

// Well-known action names that are not workflow actions.
const (
	ActionWebhookReceived   = "WEBHOOK_RECEIVED"
	ActionWorkflowStarted   = "WORKFLOW_STARTED"
	ActionWorkflowResumed   = "WORKFLOW_RESUMED"
	ActionWorkflowCompleted = "WORKFLOW_COMPLETED"
	ActionWorkflowFailed    = "WORKFLOW_FAILED"
	ActionWorkflowSkipped   = "WORKFLOW_SKIPPED"
)

// Entry statuses.
const (
	StatusSuccess   = "SUCCESS"
	StatusFailed    = "FAILED"
	StatusSkipped   = "SKIPPED"
	StatusDuplicate = "DUPLICATE"
	StatusStarted   = "STARTED"
	StatusInfo      = "INFO"
)

// Entry is one line of the integration log. Field names are the JSON keys
// written to the JSONL files and searched by the log viewer.
type Entry struct {
	Timestamp     time.Time `json:"timestamp"`
	CorrelationID string    `json:"correlation_id"`
	Workflow      string    `json:"workflow,omitempty"`
	// Platform is the source platform (e.g. easyecom).
	Platform string `json:"platform,omitempty"`
	// Integration is the destination platform or integration name (e.g. dabur).
	Integration   string `json:"integration,omitempty"`
	IntegrationID string `json:"integration_id,omitempty"`
	// ExternalOrderID is the source platform's order identifier; OrderID is
	// the destination platform's identifier once known.
	ExternalOrderID string `json:"external_order_id,omitempty"`
	OrderID         string `json:"order_id,omitempty"`

	Action     string         `json:"action"`
	Status     string         `json:"status"`
	Attempt    int            `json:"attempt,omitempty"`
	DurationMS int64          `json:"duration_ms,omitempty"`
	Error      *apperror.Info `json:"error,omitempty"`
	// Request and Response hold sanitised metadata about an external call
	// (method, URL path, status, bounded body excerpt). Never credentials.
	Request  map[string]any `json:"request,omitempty"`
	Response map[string]any `json:"response,omitempty"`
	Details  map[string]any `json:"details,omitempty"`
}

// Recorder receives entries. Implementations must be safe for concurrent
// use and must never block processing on log I/O failures.
type Recorder interface {
	Record(ctx context.Context, e Entry)
}

// Nop discards entries.
type Nop struct{}

// Record implements Recorder.
func (Nop) Record(context.Context, Entry) {}

// Memory collects entries for tests.
type Memory struct {
	mu      sync.Mutex
	entries []Entry
}

// Record implements Recorder.
func (m *Memory) Record(_ context.Context, e Entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, e)
}

// Entries returns a copy of everything recorded so far.
func (m *Memory) Entries() []Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Entry, len(m.entries))
	copy(out, m.entries)
	return out
}

// Reset discards recorded entries.
func (m *Memory) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = nil
}
