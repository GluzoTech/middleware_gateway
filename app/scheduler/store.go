package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound reports a job with no state row.
var ErrNotFound = errors.New("scheduler: job state not found")

// State is one job's scheduling record.
type State struct {
	Name string
	// Watermark is how far the last successful run covered. Work is bounded
	// by this, not by when the process happened to wake up, so a tick missed
	// because the process was down is covered by the next one rather than
	// skipped.
	Watermark time.Time
	// NextDueAt is when the job may next be claimed.
	NextDueAt time.Time
	// LastFiredAt and LastSuccessAt are for operators. A LastFiredAt far
	// ahead of LastSuccessAt means runs are starting and not finishing.
	LastFiredAt   time.Time
	LastSuccessAt time.Time
}

// Store holds scheduling state shared by every replica.
//
// Claim is the whole design. It must be atomic: two replicas calling it for
// the same job at the same instant must see exactly one success, or the same
// sweep runs twice.
type Store interface {
	// Ensure creates the job's row if it does not exist, due at dueAt and
	// covering work from watermark onwards. An existing row is left alone,
	// so a redeploy does not reset a watermark.
	//
	// The watermark is set here rather than after the first success on
	// purpose. A job whose first run fails would otherwise have no
	// watermark, and the run after it would start from its own lookback —
	// silently skipping everything between deployment and the first
	// success, which is exactly the window most likely to fail.
	Ensure(ctx context.Context, name string, dueAt, watermark time.Time) error

	// Claim takes the job's next tick if it is due, moving NextDueAt on by
	// interval. It reports false when the job is not due or another replica
	// claimed it first.
	Claim(ctx context.Context, name string, now time.Time, interval time.Duration) (State, bool, error)

	// Complete records a successful run: the watermark advances to
	// watermark, and NextDueAt is set to nextDueAt.
	Complete(ctx context.Context, name string, watermark, nextDueAt, at time.Time) error

	// Get returns the current state, for tests and operators.
	Get(ctx context.Context, name string) (State, error)
}

// PostgresStore is the Store backed by the scheduler_state table.
//
// The claim and the watermark live in the same row and are written by the
// same statements, so there is no arrangement in which one replica believes
// it owns a tick while another has already moved the watermark.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewStore wraps pool.
func NewStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

// Ensure implements Store.
func (s *PostgresStore) Ensure(ctx context.Context, name string, dueAt, watermark time.Time) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("scheduler: job name is required")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO scheduler_state (job_name, next_due_at, watermark)
		VALUES ($1, $2, $3)
		ON CONFLICT (job_name) DO NOTHING`,
		name, dueAt.UTC(), watermark.UTC())
	if err != nil {
		return fmt.Errorf("scheduler: ensure %s: %w", name, err)
	}
	return nil
}

// Claim implements Store.
//
// The WHERE clause is the mutual exclusion: PostgreSQL serialises the two
// updates, and the loser's clause no longer matches because the winner has
// already moved next_due_at. The loser gets no row back.
func (s *PostgresStore) Claim(ctx context.Context, name string, now time.Time, interval time.Duration) (State, bool, error) {
	var st State
	var watermark, lastFired, lastSuccess *time.Time
	err := s.pool.QueryRow(ctx, `
		UPDATE scheduler_state
		SET next_due_at = $2 + make_interval(secs => $3),
		    last_fired_at = $2,
		    updated_at = now()
		WHERE job_name = $1 AND next_due_at <= $2
		RETURNING job_name, watermark, next_due_at, last_fired_at, last_success_at`,
		strings.TrimSpace(name), now.UTC(), interval.Seconds(),
	).Scan(&st.Name, &watermark, &st.NextDueAt, &lastFired, &lastSuccess)
	if errors.Is(err, pgx.ErrNoRows) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, fmt.Errorf("scheduler: claim %s: %w", name, err)
	}
	assign(&st, watermark, lastFired, lastSuccess)
	return st, true, nil
}

// Complete implements Store.
func (s *PostgresStore) Complete(ctx context.Context, name string, watermark, nextDueAt, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE scheduler_state
		SET watermark = $2, next_due_at = $3, last_success_at = $4, updated_at = now()
		WHERE job_name = $1`,
		strings.TrimSpace(name), watermark.UTC(), nextDueAt.UTC(), at.UTC())
	if err != nil {
		return fmt.Errorf("scheduler: complete %s: %w", name, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return nil
}

// Get implements Store.
func (s *PostgresStore) Get(ctx context.Context, name string) (State, error) {
	var st State
	var watermark, lastFired, lastSuccess *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT job_name, watermark, next_due_at, last_fired_at, last_success_at
		FROM scheduler_state WHERE job_name = $1`,
		strings.TrimSpace(name),
	).Scan(&st.Name, &watermark, &st.NextDueAt, &lastFired, &lastSuccess)
	if errors.Is(err, pgx.ErrNoRows) {
		return State{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err != nil {
		return State{}, fmt.Errorf("scheduler: get %s: %w", name, err)
	}
	assign(&st, watermark, lastFired, lastSuccess)
	return st, nil
}

// assign copies the nullable timestamps onto the state. A job that has never
// run has no watermark, and the zero time says so more plainly than a
// sentinel date would.
func assign(st *State, watermark, lastFired, lastSuccess *time.Time) {
	st.NextDueAt = st.NextDueAt.UTC()
	for _, f := range []struct {
		src *time.Time
		dst *time.Time
	}{
		{watermark, &st.Watermark},
		{lastFired, &st.LastFiredAt},
		{lastSuccess, &st.LastSuccessAt},
	} {
		if f.src != nil {
			*f.dst = f.src.UTC()
		}
	}
}

// MemoryStore is an in-memory Store for tests and single-process use.
//
// It holds the same invariants as the PostgreSQL one — a claim is atomic and
// exclusive — so a test that shares one MemoryStore between two Schedulers
// exercises the same contention two replicas would.
type MemoryStore struct {
	mu     sync.Mutex
	states map[string]State
}

// NewMemoryStore builds an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{states: make(map[string]State)}
}

// Ensure implements Store.
func (m *MemoryStore) Ensure(_ context.Context, name string, dueAt, watermark time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.states[name]; ok {
		return nil
	}
	m.states[name] = State{Name: name, NextDueAt: dueAt.UTC(), Watermark: watermark.UTC()}
	return nil
}

// Claim implements Store.
func (m *MemoryStore) Claim(_ context.Context, name string, now time.Time, interval time.Duration) (State, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.states[name]
	if !ok || st.NextDueAt.After(now.UTC()) {
		return State{}, false, nil
	}
	st.NextDueAt = now.UTC().Add(interval)
	st.LastFiredAt = now.UTC()
	m.states[name] = st
	return st, true, nil
}

// Complete implements Store.
func (m *MemoryStore) Complete(_ context.Context, name string, watermark, nextDueAt, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.states[name]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	st.Watermark = watermark.UTC()
	st.NextDueAt = nextDueAt.UTC()
	st.LastSuccessAt = at.UTC()
	m.states[name] = st
	return nil
}

// Get implements Store.
func (m *MemoryStore) Get(_ context.Context, name string) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.states[name]
	if !ok {
		return State{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return st, nil
}
