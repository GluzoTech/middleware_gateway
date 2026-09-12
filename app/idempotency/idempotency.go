// Package idempotency prevents duplicate webhooks from starting duplicate
// workflows.
//
// Every inbound event derives a stable key from external identifiers. The
// first request to claim a key owns it; later requests with the same key are
// answered as duplicates and never queued. Claims are made atomically so two
// concurrent duplicates cannot both win.
package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// Status is the lifecycle of the event behind a key.
type Status string

// Statuses.
const (
	StatusAccepted   Status = "accepted"   // claimed and queued
	StatusProcessing Status = "processing" // a worker is executing the workflow
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
)

// ErrNotFound is returned when a key has no record.
var ErrNotFound = errors.New("idempotency: record not found")

// MaxKeyLength bounds stored keys; longer keys are hashed by KeyFor.
const MaxKeyLength = 200

// Record is the stored view of one claimed key.
type Record struct {
	Key           string
	CorrelationID string
	Platform      string
	EventType     string
	Status        Status
	CreatedAt     time.Time
	ProcessedAt   *time.Time
}

// Claim is the outcome of trying to register a key.
type Claim struct {
	// Accepted is true when the caller now owns the key.
	Accepted bool
	// Existing describes the earlier event when Accepted is false.
	Existing *Record
}

// Store persists claims.
type Store interface {
	// Claim registers rec.Key atomically. A duplicate key yields
	// Accepted=false and the existing record.
	Claim(ctx context.Context, rec Record) (Claim, error)
	// Release removes a claim so the key can be accepted again; used when
	// the event could not be queued after all.
	Release(ctx context.Context, key string) error
	// SetStatus updates the lifecycle status; completed and failed also
	// stamp processed_at.
	SetStatus(ctx context.Context, key string, status Status) error
	// Get returns the record for key or ErrNotFound.
	Get(ctx context.Context, key string) (*Record, error)
	// DeleteOlderThan removes records created before cutoff and reports how
	// many were removed.
	DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
}

// KeyFor joins parts into a stable key such as "easyecom:ORDER_CREATED:123".
// Keys that would exceed MaxKeyLength are hashed, keeping the first part as
// a readable prefix.
func KeyFor(parts ...string) string {
	trimmed := make([]string, len(parts))
	for i, p := range parts {
		trimmed[i] = strings.TrimSpace(p)
	}
	key := strings.Join(trimmed, ":")
	if len(key) <= MaxKeyLength {
		return key
	}
	sum := sha256.Sum256([]byte(key))
	prefix := ""
	if len(trimmed) > 0 {
		prefix = trimmed[0] + ":"
	}
	return prefix + "sha256:" + hex.EncodeToString(sum[:])
}

// validStatus reports whether s is a known status.
func validStatus(s Status) bool {
	switch s {
	case StatusAccepted, StatusProcessing, StatusCompleted, StatusFailed:
		return true
	}
	return false
}
