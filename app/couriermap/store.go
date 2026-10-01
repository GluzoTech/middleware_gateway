package couriermap

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the PostgreSQL-backed Reader and carrier administration.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const columns = `id, integration_id, transporter, company_carrier_id, courier_name, status, created_at`

func scanAll(rows pgx.Rows) ([]Courier, error) {
	defer rows.Close()
	var out []Courier
	for rows.Next() {
		var c Courier
		if err := rows.Scan(&c.ID, &c.IntegrationID, &c.Transporter, &c.CompanyCarrierID, &c.Name, &c.Status, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("couriermap: scan: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("couriermap: read rows: %w", err)
	}
	return out, nil
}

// Active implements Reader.
func (s *Store) Active(ctx context.Context, integrationID uuid.UUID) ([]Courier, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+columns+` FROM courier_map
		WHERE integration_id = $1 AND status = 'active'
		ORDER BY transporter`, integrationID)
	if err != nil {
		return nil, fmt.Errorf("couriermap: list active: %w", err)
	}
	return scanAll(rows)
}

// List returns every mapping for an integration, disabled ones included, so
// an operator can see what they turned off.
func (s *Store) List(ctx context.Context, integrationName string) ([]Courier, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+prefixed(columns, "m")+`
		FROM courier_map m
		JOIN integrations i ON i.id = m.integration_id
		WHERE i.name = $1
		ORDER BY m.transporter`, strings.TrimSpace(integrationName))
	if err != nil {
		return nil, fmt.Errorf("couriermap: list: %w", err)
	}
	return scanAll(rows)
}

// Add creates a mapping for the named integration.
func (s *Store) Add(ctx context.Context, integrationName, transporter, companyCarrierID, name string) (Courier, error) {
	c := Courier{
		ID:               uuid.New(),
		Transporter:      strings.TrimSpace(transporter),
		CompanyCarrierID: strings.TrimSpace(companyCarrierID),
		Name:             strings.TrimSpace(name),
		Status:           StatusActive,
	}
	if err := c.Validate(); err != nil {
		return Courier{}, fmt.Errorf("%w: %s", ErrInvalidArgument, err)
	}

	err := s.pool.QueryRow(ctx, `
		INSERT INTO courier_map (id, integration_id, transporter, company_carrier_id, courier_name)
		SELECT $1, i.id, $2, $3, $4 FROM integrations i WHERE i.name = $5
		RETURNING integration_id, status, created_at`,
		c.ID, c.Transporter, c.CompanyCarrierID, c.Name, strings.TrimSpace(integrationName),
	).Scan(&c.IntegrationID, &c.Status, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Courier{}, fmt.Errorf("%w: integration %q", ErrNotFound, integrationName)
	}
	if isUniqueViolation(err) {
		return Courier{}, fmt.Errorf("%w: carrier %q for integration %q", ErrAlreadyExists, c.Transporter, integrationName)
	}
	if err != nil {
		return Courier{}, fmt.Errorf("couriermap: add: %w", err)
	}
	return c, nil
}

// SetStatus enables or disables a mapping.
func (s *Store) SetStatus(ctx context.Context, id uuid.UUID, status string) error {
	switch status {
	case StatusActive, StatusDisabled:
	default:
		return fmt.Errorf("%w: status must be %s or %s", ErrInvalidArgument, StatusActive, StatusDisabled)
	}
	tag, err := s.pool.Exec(ctx, `UPDATE courier_map SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	if err != nil {
		return fmt.Errorf("couriermap: set status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: mapping %s", ErrNotFound, id)
	}
	return nil
}

// Remove deletes a mapping.
func (s *Store) Remove(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM courier_map WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("couriermap: remove: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: mapping %s", ErrNotFound, id)
	}
	return nil
}

func prefixed(cols, alias string) string {
	parts := strings.Split(cols, ", ")
	for i, p := range parts {
		parts[i] = alias + "." + p
	}
	return strings.Join(parts, ", ")
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
