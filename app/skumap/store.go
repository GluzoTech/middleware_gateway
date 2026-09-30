package skumap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the PostgreSQL-backed Reader and mapping administration.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Active implements Reader.
func (s *Store) Active(ctx context.Context, integrationID uuid.UUID) ([]Mapping, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, integration_id, gluzo_sku, vendor_sku, safety_buffer, status, created_at
		FROM sku_map
		WHERE integration_id = $1 AND status = 'active'
		ORDER BY gluzo_sku`, integrationID)
	if err != nil {
		return nil, fmt.Errorf("skumap: list active: %w", err)
	}
	defer rows.Close()

	var out []Mapping
	for rows.Next() {
		var m Mapping
		if err := rows.Scan(&m.ID, &m.IntegrationID, &m.GluzoSKU, &m.VendorSKU, &m.SafetyBuffer, &m.Status, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("skumap: scan: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("skumap: list active: %w", err)
	}
	return out, nil
}

// List returns every mapping for an integration, including disabled ones, so
// an operator can see what they turned off.
func (s *Store) List(ctx context.Context, integrationName string) ([]Mapping, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.id, m.integration_id, m.gluzo_sku, m.vendor_sku, m.safety_buffer, m.status, m.created_at
		FROM sku_map m
		JOIN integrations i ON i.id = m.integration_id
		WHERE i.name = $1
		ORDER BY m.gluzo_sku`, strings.TrimSpace(integrationName))
	if err != nil {
		return nil, fmt.Errorf("skumap: list: %w", err)
	}
	defer rows.Close()

	var out []Mapping
	for rows.Next() {
		var m Mapping
		if err := rows.Scan(&m.ID, &m.IntegrationID, &m.GluzoSKU, &m.VendorSKU, &m.SafetyBuffer, &m.Status, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("skumap: scan: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("skumap: list: %w", err)
	}
	return out, nil
}

// Add creates a mapping for the named integration.
func (s *Store) Add(ctx context.Context, integrationName, gluzoSKU, vendorSKU string, safetyBuffer int) (Mapping, error) {
	m := Mapping{
		ID:           uuid.New(),
		GluzoSKU:     strings.TrimSpace(gluzoSKU),
		VendorSKU:    strings.TrimSpace(vendorSKU),
		SafetyBuffer: safetyBuffer,
		Status:       StatusActive,
	}
	if err := m.Validate(); err != nil {
		return Mapping{}, fmt.Errorf("%w: %s", ErrInvalidArgument, err)
	}

	err := s.pool.QueryRow(ctx, `
		INSERT INTO sku_map (id, integration_id, gluzo_sku, vendor_sku, safety_buffer)
		SELECT $1, i.id, $2, $3, $4 FROM integrations i WHERE i.name = $5
		RETURNING integration_id, status, created_at`,
		m.ID, m.GluzoSKU, m.VendorSKU, m.SafetyBuffer, strings.TrimSpace(integrationName),
	).Scan(&m.IntegrationID, &m.Status, &m.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Mapping{}, fmt.Errorf("%w: integration %q", ErrNotFound, integrationName)
	}
	if isUniqueViolation(err) {
		return Mapping{}, fmt.Errorf("%w: %s <-> %s for integration %q", ErrAlreadyExists, m.GluzoSKU, m.VendorSKU, integrationName)
	}
	if err != nil {
		return Mapping{}, fmt.Errorf("skumap: add: %w", err)
	}
	return m, nil
}

// SetStatus enables or disables a mapping.
func (s *Store) SetStatus(ctx context.Context, id uuid.UUID, status string) error {
	switch status {
	case StatusActive, StatusDisabled:
	default:
		return fmt.Errorf("%w: status must be %s or %s", ErrInvalidArgument, StatusActive, StatusDisabled)
	}
	tag, err := s.pool.Exec(ctx, `UPDATE sku_map SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	if err != nil {
		return fmt.Errorf("skumap: set status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: mapping %s", ErrNotFound, id)
	}
	return nil
}

// Remove deletes a mapping.
func (s *Store) Remove(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sku_map WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("skumap: remove: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: mapping %s", ErrNotFound, id)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// MemoryReader is an in-memory Reader for tests and for wiring a workflow
// before a database is available.
type MemoryReader struct {
	mu       sync.RWMutex
	mappings map[uuid.UUID][]Mapping
}

// NewMemoryReader builds an empty reader.
func NewMemoryReader() *MemoryReader {
	return &MemoryReader{mappings: make(map[uuid.UUID][]Mapping)}
}

// Set replaces the mappings for an integration.
func (m *MemoryReader) Set(integrationID uuid.UUID, mappings []Mapping) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mappings[integrationID] = append([]Mapping(nil), mappings...)
}

// Active implements Reader.
func (m *MemoryReader) Active(_ context.Context, integrationID uuid.UUID) ([]Mapping, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Mapping
	for _, mapping := range m.mappings[integrationID] {
		if mapping.Status == StatusDisabled {
			continue
		}
		out = append(out, mapping)
	}
	return out, nil
}
