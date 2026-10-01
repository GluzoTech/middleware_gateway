package shipmentstate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gluzo/integration-gateway/app/domain/tracking"
)

// PostgresStore is the Store backed by the shipment_state table.
type PostgresStore struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// NewStore wraps pool.
func NewStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool, now: time.Now}
}

// Get implements Store.
func (s *PostgresStore) Get(ctx context.Context, key Key) (Record, bool, error) {
	k := key.Normalised()
	var rec Record
	var status string
	err := s.pool.QueryRow(ctx, `
		SELECT status, progress, tracking_number, carrier, pushed_at
		FROM shipment_state
		WHERE integration_id = $1 AND order_external_id = $2 AND package_code = $3`,
		k.IntegrationID, k.OrderExternalID, k.PackageCode,
	).Scan(&status, &rec.Progress, &rec.TrackingNumber, &rec.Carrier, &rec.PushedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("shipmentstate: get: %w", err)
	}
	rec.Status = tracking.Status(status)
	return rec, true, nil
}

// Record implements Store.
func (s *PostgresStore) Record(ctx context.Context, key Key, rec Record) error {
	k := key.Normalised()
	at := rec.PushedAt
	if at.IsZero() {
		at = s.now().UTC()
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO shipment_state
			(integration_id, order_external_id, package_code, status, progress, tracking_number, carrier, pushed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (integration_id, order_external_id, package_code)
		DO UPDATE SET status = EXCLUDED.status,
		              progress = EXCLUDED.progress,
		              tracking_number = EXCLUDED.tracking_number,
		              carrier = EXCLUDED.carrier,
		              pushed_at = EXCLUDED.pushed_at`,
		k.IntegrationID, k.OrderExternalID, k.PackageCode,
		string(rec.Status), rec.Progress, rec.TrackingNumber, rec.Carrier, at.UTC())
	if err != nil {
		return fmt.Errorf("shipmentstate: record: %w", err)
	}
	return nil
}
