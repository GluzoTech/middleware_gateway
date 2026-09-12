package workflow

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Action is one meaningful step of a workflow: fetch an order, resolve the
// integration, push to the destination. Actions read and write State and
// must be safe to execute again after a crash, because at-least-once
// delivery means an action may run twice.
type Action interface {
	Name() string
	Execute(ctx context.Context, state *State) error
}

// ActionFunc adapts a function to Action.
type ActionFunc struct {
	name string
	fn   func(ctx context.Context, state *State) error
}

// NewAction builds an Action from a name and a function.
func NewAction(name string, fn func(ctx context.Context, state *State) error) Action {
	return ActionFunc{name: name, fn: fn}
}

// Name implements Action.
func (a ActionFunc) Name() string { return a.name }

// Execute implements Action.
func (a ActionFunc) Execute(ctx context.Context, state *State) error { return a.fn(ctx, state) }

// Policy governs retries and timeouts for one action.
type Policy struct {
	// MaxAttempts is the total number of attempts including the first.
	MaxAttempts int
	// Timeout bounds one attempt; zero means no per-attempt deadline.
	Timeout time.Duration
	// BaseDelay is the backoff before the second attempt; it doubles per
	// attempt with jitter up to MaxDelay.
	BaseDelay time.Duration
	MaxDelay  time.Duration
	// Optional marks an action whose permanent failure is recorded as
	// SKIPPED and does not fail the workflow.
	Optional bool
}

// Policy defaults.
const (
	DefaultMaxAttempts = 3
	DefaultTimeout     = 30 * time.Second
	DefaultBaseDelay   = 2 * time.Second
	DefaultMaxDelay    = 30 * time.Second
)

// DefaultPolicy is three attempts with exponential backoff.
func DefaultPolicy() Policy {
	return Policy{MaxAttempts: DefaultMaxAttempts, Timeout: DefaultTimeout, BaseDelay: DefaultBaseDelay, MaxDelay: DefaultMaxDelay}
}

// NoRetry performs exactly one attempt.
func NoRetry() Policy {
	p := DefaultPolicy()
	p.MaxAttempts = 1
	return p
}

func (p Policy) normalised() Policy {
	if p.MaxAttempts < 1 {
		p.MaxAttempts = 1
	}
	if p.BaseDelay <= 0 {
		p.BaseDelay = DefaultBaseDelay
	}
	if p.MaxDelay < p.BaseDelay {
		p.MaxDelay = p.BaseDelay
	}
	return p
}

// Step pairs an action with its policy.
type Step struct {
	Action Action
	Policy Policy
}

// Workflow is a named, ordered list of steps. Steps execute sequentially;
// the engine is designed so that conditional or parallel execution can be
// added later without changing actions.
type Workflow interface {
	Name() string
	Steps() []Step
}

// Definition is the simplest Workflow.
type Definition struct {
	name  string
	steps []Step
}

// NewDefinition builds a Workflow from steps.
func NewDefinition(name string, steps ...Step) *Definition {
	return &Definition{name: name, steps: steps}
}

// Name implements Workflow.
func (d *Definition) Name() string { return d.name }

// Steps implements Workflow.
func (d *Definition) Steps() []Step { return d.steps }

// SkipError is returned by an action to end the workflow successfully
// without running the remaining actions, for example when no route exists
// for the event. It is never retried.
type SkipError struct {
	Reason string
}

// Error implements error.
func (e *SkipError) Error() string { return "workflow skipped: " + e.Reason }

// Skip builds a SkipError.
func Skip(reason string) error { return &SkipError{Reason: reason} }

// IsSkip reports whether err (or its cause) is a SkipError.
func IsSkip(err error) (string, bool) {
	var s *SkipError
	if errors.As(err, &s) {
		return s.Reason, true
	}
	return "", false
}

// validateSteps rejects definitions the executor cannot run safely.
func validateSteps(steps []Step) error {
	if len(steps) == 0 {
		return errors.New("workflow: definition has no steps")
	}
	seen := make(map[string]bool, len(steps))
	for i, s := range steps {
		if s.Action == nil || s.Action.Name() == "" {
			return fmt.Errorf("workflow: step %d has no action", i)
		}
		if seen[s.Action.Name()] {
			return fmt.Errorf("workflow: duplicate action name %q", s.Action.Name())
		}
		seen[s.Action.Name()] = true
	}
	return nil
}
