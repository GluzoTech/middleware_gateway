package workflow

import (
	"context"
	"errors"
)

// ErrStateNotFound is returned when no state exists for a correlation ID.
var ErrStateNotFound = errors.New("workflow: state not found")

// Repository persists workflow state. Implementations must make Save
// atomic: a reader never observes a partially written state.
type Repository interface {
	// Get loads the state for correlationID or returns ErrStateNotFound.
	Get(ctx context.Context, correlationID string) (*State, error)
	// Save persists state, replacing any previous version.
	Save(ctx context.Context, state *State) error
	// ListActive returns the correlation IDs of runs that are not in a
	// final state, so a restarted worker can resume them.
	ListActive(ctx context.Context) ([]string, error)
}
