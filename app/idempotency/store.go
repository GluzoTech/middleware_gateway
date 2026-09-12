package idempotency

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore is the production Store.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore wraps pool.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

// Claim implements Store. The insert races on the primary key, so of two
// concurrent duplicates exactly one receives Accepted=true.
func (s *PostgresStore) Claim(ctx context.Context, rec Record) (Claim, error) {
	if rec.Key == "" {
		return Claim{}, fmt.Errorf("idempotency: key is required")
	}
	if rec.Status == "" {
		rec.Status = StatusAccepted
	}

	var createdAt time.Time
	err := s.pool.QueryRow(ctx, `
		INSERT INTO idempotency_records (idempotency_key, correlation_id, platform, event_type, status)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING created_at`,
		rec.Key, rec.CorrelationID, rec.Platform, rec.EventType, string(rec.Status),
	).Scan(&createdAt)
	if err == nil {
		return Claim{Accepted: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Claim{}, fmt.Errorf("idempotency: claim: %w", err)
	}

	existing, err := s.Get(ctx, rec.Key)
	if err != nil {
		// The winner released its claim between our insert and read; the
		// caller should treat this as transient and let the sender retry.
		return Claim{}, fmt.Errorf("idempotency: claim raced with a release: %w", err)
	}
	return Claim{Accepted: false, Existing: existing}, nil
}

// Release implements Store.
func (s *PostgresStore) Release(ctx context.Context, key string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM idempotency_records WHERE idempotency_key = $1`, key); err != nil {
		return fmt.Errorf("idempotency: release: %w", err)
	}
	return nil
}

// SetStatus implements Store.
func (s *PostgresStore) SetStatus(ctx context.Context, key string, status Status) error {
	if !validStatus(status) {
		return fmt.Errorf("idempotency: invalid status %q", status)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE idempotency_records
		SET status = $2,
		    processed_at = CASE WHEN $2 IN ('completed', 'failed') THEN now() ELSE processed_at END
		WHERE idempotency_key = $1`, key, string(status))
	if err != nil {
		return fmt.Errorf("idempotency: set status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Get implements Store.
func (s *PostgresStore) Get(ctx context.Context, key string) (*Record, error) {
	var rec Record
	var status string
	err := s.pool.QueryRow(ctx, `
		SELECT idempotency_key, correlation_id, platform, event_type, status, created_at, processed_at
		FROM idempotency_records WHERE idempotency_key = $1`, key,
	).Scan(&rec.Key, &rec.CorrelationID, &rec.Platform, &rec.EventType, &status, &rec.CreatedAt, &rec.ProcessedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("idempotency: get: %w", err)
	}
	rec.Status = Status(status)
	return &rec, nil
}

// DeleteOlderThan implements Store.
func (s *PostgresStore) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM idempotency_records WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("idempotency: delete older than: %w", err)
	}
	return tag.RowsAffected(), nil
}
