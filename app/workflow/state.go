// Package workflow is the execution engine: a workflow is an ordered list of
// actions with retry policies, executed against a persisted, serialisable
// state so that a failed or interrupted run resumes from the last successful
// action instead of starting over.
package workflow

import (
	"encoding/json"
	"time"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/domain/inventory"
	"github.com/gluzo/integration-gateway/app/domain/order"
	"github.com/gluzo/integration-gateway/app/domain/tracking"
	"github.com/gluzo/integration-gateway/app/event"
)

// Status is the lifecycle state of one workflow run.
type Status string

// Workflow statuses.
const (
	StatusPending   Status = "PENDING"   // created, not yet started
	StatusRunning   Status = "RUNNING"   // an action is executing or about to
	StatusCompleted Status = "COMPLETED" // every action succeeded (or was optional)
	StatusFailed    Status = "FAILED"    // an action failed permanently; resumable
	StatusSkipped   Status = "SKIPPED"   // an action decided the event needs no processing
)

// IsFinal reports whether no further execution is expected.
func (s Status) IsFinal() bool {
	return s == StatusCompleted || s == StatusSkipped
}

// ActionStatus is the lifecycle state of one action within a run.
type ActionStatus string

// Action statuses.
const (
	ActionPending   ActionStatus = "PENDING"
	ActionRunning   ActionStatus = "RUNNING"
	ActionSucceeded ActionStatus = "SUCCESS"
	ActionFailed    ActionStatus = "FAILED"
	ActionSkipped   ActionStatus = "SKIPPED"
)

// ActionRecord is the persisted progress of one action.
type ActionRecord struct {
	Name        string         `json:"name"`
	Status      ActionStatus   `json:"status"`
	Attempt     int            `json:"attempt"`
	LastError   *apperror.Info `json:"last_error,omitempty"`
	StartedAt   *time.Time     `json:"started_at,omitempty"`
	CompletedAt *time.Time     `json:"completed_at,omitempty"`
	DurationMS  int64          `json:"duration_ms,omitempty"`
}

// RouteInfo is the routing outcome kept in state so later actions and the
// logs know which destination the event is bound for.
type RouteInfo struct {
	IntegrationID        string `json:"integration_id"`
	IntegrationName      string `json:"integration_name"`
	SourcePlatform       string `json:"source_platform"`
	DestinationPlatform  string `json:"destination_platform"`
	RouteType            string `json:"route_type"`
	RouteValue           string `json:"route_value"`
	DestinationReference string `json:"destination_reference,omitempty"`
}

// Well-known result keys.
const (
	ResultDestinationOrderID = "destination_order_id"
)

// State is the complete, serialisable state of one workflow run. Domain
// data carries no external JSON names; Payloads hold opaque documents that
// a destination adapter prepared for itself.
type State struct {
	CorrelationID string `json:"correlation_id"`
	WorkflowName  string `json:"workflow"`
	EventType     string `json:"event_type"`
	Platform      string `json:"platform"`
	IntegrationID string `json:"integration_id"`
	JobID         string `json:"job_id,omitempty"`

	Status               Status         `json:"status"`
	CurrentAction        int            `json:"current_action"`
	LastSuccessfulAction string         `json:"last_successful_action,omitempty"`
	NextAction           string         `json:"next_action,omitempty"`
	Actions              []ActionRecord `json:"actions"`
	ResumeCount          int            `json:"resume_count"`
	LastError            *apperror.Info `json:"last_error,omitempty"`
	SkipReason           string         `json:"skip_reason,omitempty"`

	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`

	Event     event.Event                `json:"event"`
	Route     *RouteInfo                 `json:"route,omitempty"`
	Order     *order.Order               `json:"order,omitempty"`
	Inventory []inventory.Level          `json:"inventory,omitempty"`
	Tracking  *tracking.Shipment         `json:"tracking,omitempty"`
	Results   map[string]string          `json:"results,omitempty"`
	Payloads  map[string]json.RawMessage `json:"payloads,omitempty"`
}

// NewState creates the initial state for running workflowName on ev.
func NewState(workflowName string, ev event.Event, jobID string, now time.Time) *State {
	return &State{
		CorrelationID: ev.CorrelationID,
		WorkflowName:  workflowName,
		EventType:     ev.EventType,
		Platform:      ev.Platform,
		IntegrationID: ev.IntegrationID,
		JobID:         jobID,
		Status:        StatusPending,
		CreatedAt:     now,
		UpdatedAt:     now,
		Event:         ev,
	}
}

// SetResult stores a simple result value.
func (s *State) SetResult(key, value string) {
	if s.Results == nil {
		s.Results = make(map[string]string)
	}
	s.Results[key] = value
}

// Result returns a stored result value, or "".
func (s *State) Result(key string) string {
	return s.Results[key]
}

// SetPayload stores an opaque document under key.
func (s *State) SetPayload(key string, doc json.RawMessage) {
	if s.Payloads == nil {
		s.Payloads = make(map[string]json.RawMessage)
	}
	s.Payloads[key] = doc
}

// Payload returns a stored document, or nil.
func (s *State) Payload(key string) json.RawMessage {
	return s.Payloads[key]
}

// DestinationPlatform returns the resolved destination, or "" before routing.
func (s *State) DestinationPlatform() string {
	if s.Route == nil {
		return ""
	}
	return s.Route.DestinationPlatform
}

// ResetCurrentAttempts clears the attempt counter and error of the action
// the workflow will resume at, so a manual resume gets a fresh retry budget.
func (s *State) ResetCurrentAttempts() {
	if s.CurrentAction < 0 || s.CurrentAction >= len(s.Actions) {
		return
	}
	rec := &s.Actions[s.CurrentAction]
	if rec.Status == ActionSucceeded {
		return
	}
	rec.Attempt = 0
	rec.Status = ActionPending
	rec.LastError = nil
	rec.StartedAt, rec.CompletedAt, rec.DurationMS = nil, nil, 0
	s.LastError = nil
}
