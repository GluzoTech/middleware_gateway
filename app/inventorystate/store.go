package inventorystate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore is the Store backed by the inventory_state table.
type PostgresStore struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// NewStore wraps pool.
func NewStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool, now: time.Now}
}

// Load implements Store.
func (s *PostgresStore) Load(ctx context.Context, key Key) (map[string]int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT gluzo_sku, quantity
		FROM inventory_state
		WHERE integration_id = $1 AND origin_reference = $2`,
		key.IntegrationID, strings.TrimSpace(key.OriginReference))
	if err != nil {
		return nil, fmt.Errorf("inventorystate: load: %w", err)
	}
	defer rows.Close()

	out := make(map[string]int)
	for rows.Next() {
		var sku string
		var qty int
		if err := rows.Scan(&sku, &qty); err != nil {
			return nil, fmt.Errorf("inventorystate: scan: %w", err)
		}
		out[normalise(sku)] = qty
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inventorystate: load: %w", err)
	}
	return out, nil
}

// Record implements Store.
//
// Written in one batch inside a transaction: a sweep that recorded half its
// SKUs and then failed would leave the other half looking unchanged next run,
// which is the one way this table can cause a missed update rather than a
// redundant one.
func (s *PostgresStore) Record(ctx context.Context, key Key, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	origin := strings.TrimSpace(key.OriginReference)
	now := s.now().UTC()

	batch := &pgx.Batch{}
	for _, e := range entries {
		sku := strings.TrimSpace(e.GluzoSKU)
		if sku == "" {
			continue
		}
		batch.Queue(`
			INSERT INTO inventory_state (integration_id, origin_reference, gluzo_sku, quantity, pushed_at)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (integration_id, origin_reference, gluzo_sku)
			DO UPDATE SET quantity = EXCLUDED.quantity, pushed_at = EXCLUDED.pushed_at`,
			key.IntegrationID, origin, sku, e.Quantity, now)
	}
	if batch.Len() == 0 {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("inventorystate: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	results := tx.SendBatch(ctx, batch)
	for i := 0; i < batch.Len(); i++ {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return fmt.Errorf("inventorystate: record: %w", err)
		}
	}
	if err := results.Close(); err != nil {
		return fmt.Errorf("inventorystate: record: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("inventorystate: commit: %w", err)
	}
	return nil
}

// Forget implements Store.
func (s *PostgresStore) Forget(ctx context.Context, key Key) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM inventory_state WHERE integration_id = $1 AND origin_reference = $2`,
		key.IntegrationID, strings.TrimSpace(key.OriginReference))
	if err != nil {
		return fmt.Errorf("inventorystate: forget: %w", err)
	}
	return nil
}
