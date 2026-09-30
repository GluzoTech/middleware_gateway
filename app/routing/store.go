package routing

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

// Store is the PostgreSQL-backed Resolver and route administration.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Resolve implements Resolver. Disabled routes and disabled integrations
// resolve to nothing, so an operator can pause a warehouse or a whole
// pipeline without deleting configuration.
func (s *Store) Resolve(ctx context.Context, integrationID uuid.UUID, key Key) (*Resolution, error) {
	key.Type, key.Value = strings.TrimSpace(key.Type), strings.TrimSpace(key.Value)
	if key.Type == "" || key.Value == "" {
		return nil, fmt.Errorf("%w: routing key type and value are required", ErrInvalidArgument)
	}

	var res Resolution
	var destRef, originRef *string
	err := s.pool.QueryRow(ctx, `
		SELECT r.id, r.integration_id, r.route_type, r.route_value, r.destination_reference, r.origin_reference, r.status, r.created_at,
		       i.name, src.name, dst.name
		FROM integration_routes r
		JOIN integrations i ON i.id = r.integration_id
		JOIN platforms src ON src.id = i.source_platform_id
		JOIN platforms dst ON dst.id = i.destination_platform_id
		WHERE r.integration_id = $1 AND r.route_type = $2 AND r.route_value = $3
		  AND r.status = 'active' AND i.status = 'active'`,
		integrationID, key.Type, key.Value,
	).Scan(&res.Route.ID, &res.Route.IntegrationID, &res.Route.Type, &res.Route.Value, &destRef, &originRef, &res.Route.Status, &res.Route.CreatedAt,
		&res.IntegrationName, &res.SourcePlatform, &res.DestinationPlatform)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoRoute
	}
	if err != nil {
		return nil, fmt.Errorf("routing: resolve: %w", err)
	}
	if destRef != nil {
		res.Route.DestinationReference = *destRef
	}
	if originRef != nil {
		res.Route.OriginReference = *originRef
	}
	res.IntegrationID = res.Route.IntegrationID
	return &res, nil
}

// AddRoute creates a route for the named integration.
func (s *Store) AddRoute(ctx context.Context, integrationName, routeType, routeValue, destinationReference, originReference string) (Route, error) {
	routeType, routeValue = strings.TrimSpace(routeType), strings.TrimSpace(routeValue)
	if routeType == "" || routeValue == "" {
		return Route{}, fmt.Errorf("%w: route type and value are required", ErrInvalidArgument)
	}
	destRef := optional(destinationReference)
	originRef := optional(originReference)

	r := Route{
		ID:                   uuid.New(),
		Type:                 routeType,
		Value:                routeValue,
		DestinationReference: strings.TrimSpace(destinationReference),
		OriginReference:      strings.TrimSpace(originReference),
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO integration_routes (id, integration_id, route_type, route_value, destination_reference, origin_reference)
		SELECT $1, i.id, $2, $3, $4, $5 FROM integrations i WHERE i.name = $6
		RETURNING integration_id, status, created_at`,
		r.ID, r.Type, r.Value, destRef, originRef, integrationName,
	).Scan(&r.IntegrationID, &r.Status, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Route{}, fmt.Errorf("%w: integration %q", ErrNotFound, integrationName)
	}
	if isUniqueViolation(err) {
		return Route{}, fmt.Errorf("%w: %s=%s for integration %q", ErrAlreadyExists, routeType, routeValue, integrationName)
	}
	if err != nil {
		return Route{}, fmt.Errorf("routing: add route: %w", err)
	}
	return r, nil
}

// SetRouteStatus enables or disables a route.
func (s *Store) SetRouteStatus(ctx context.Context, id uuid.UUID, status string) error {
	if status != StatusActive && status != StatusDisabled {
		return fmt.Errorf("%w: status must be active or disabled", ErrInvalidArgument)
	}
	tag, err := s.pool.Exec(ctx, `UPDATE integration_routes SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	if err != nil {
		return fmt.Errorf("routing: set route status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: route %s", ErrNotFound, id)
	}
	return nil
}

// RemoveRoute deletes a route.
func (s *Store) RemoveRoute(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM integration_routes WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("routing: remove route: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: route %s", ErrNotFound, id)
	}
	return nil
}

// ListRoutes returns the routes of the named integration.
func (s *Store) ListRoutes(ctx context.Context, integrationName string) ([]Route, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.id, r.integration_id, r.route_type, r.route_value, r.destination_reference, r.origin_reference, r.status, r.created_at
		FROM integration_routes r
		JOIN integrations i ON i.id = r.integration_id
		WHERE i.name = $1
		ORDER BY r.route_type, r.route_value`, integrationName)
	if err != nil {
		return nil, fmt.Errorf("routing: list routes: %w", err)
	}
	defer rows.Close()

	var out []Route
	for rows.Next() {
		var r Route
		var destRef, originRef *string
		if err := rows.Scan(&r.ID, &r.IntegrationID, &r.Type, &r.Value, &destRef, &originRef, &r.Status, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("routing: scan route: %w", err)
		}
		if destRef != nil {
			r.DestinationReference = *destRef
		}
		if originRef != nil {
			r.OriginReference = *originRef
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("routing: list routes: %w", err)
	}
	return out, nil
}

// optional renders a blank reference as NULL rather than an empty string, so
// "not configured" and "configured as empty" stay distinguishable in the row.
func optional(v string) *string {
	if v = strings.TrimSpace(v); v == "" {
		return nil
	}
	return &v
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
