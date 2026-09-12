// Package queue decouples webhook intake from workflow execution.
//
// A Job is the unit handed from the webhook handler to a worker. Publishers
// enqueue jobs; consumers receive Deliveries, which pair a job with the
// acknowledgement handles the implementation needs for at-least-once
// delivery. Queue implementations (in-memory, Redis Streams) never leak into
// the workflow engine: it only sees Job.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Job is a queued request to run a workflow for one event.
type Job struct {
	ID            string `json:"id"`
	CorrelationID string `json:"correlation_id"`
	// Workflow names the workflow to run. Empty means "resolve from EventType".
	Workflow       string          `json:"workflow,omitempty"`
	EventType      string          `json:"event_type"`
	Platform       string          `json:"platform"`
	IntegrationID  string          `json:"integration_id"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	EnqueuedAt     time.Time       `json:"enqueued_at"`
	// Attempt is the 1-based delivery attempt, set by the consumer. A job
	// that was requeued or reclaimed from a dead consumer carries a higher
	// number so workers can detect poison messages.
	Attempt int `json:"attempt,omitempty"`
}

// Validate reports whether the job carries the fields every consumer needs.
func (j Job) Validate() error {
	switch {
	case j.ID == "":
		return errors.New("queue: job id is required")
	case j.CorrelationID == "":
		return errors.New("queue: correlation id is required")
	case j.EventType == "" && j.Workflow == "":
		return errors.New("queue: event type or workflow is required")
	}
	return nil
}

// Delivery is a job received from the queue.
type Delivery struct {
	Job Job
	// Ack marks the job as processed; it will not be delivered again.
	Ack func(ctx context.Context) error
	// Requeue puts the job back for another delivery with Attempt advanced.
	Requeue func(ctx context.Context) error
}

// Publisher enqueues jobs.
type Publisher interface {
	Publish(ctx context.Context, job Job) error
}

// Consumer receives jobs. The channel closes when ctx is cancelled or the
// queue is closed.
type Consumer interface {
	Consume(ctx context.Context) (<-chan Delivery, error)
}

// Queue is a Publisher and Consumer with a lifecycle.
type Queue interface {
	Publisher
	Consumer
	Close() error
}
