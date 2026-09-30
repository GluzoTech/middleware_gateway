// Package routing decides which integration processes an event.
//
// An event carries a routing key such as warehouse_id=12345. Routes are
// database rows mapping (type, value) to an integration, optionally with a
// destination-side reference such as the Uniware facility code that should
// receive the order. Resolution is scoped to the integration that
// authenticated the event, so a token can only ever steer events into its
// own pipeline.
package routing

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Route types the gateway understands. The set is open: operators may
// configure any type their platform parser emits.
const (
	TypeWarehouse   = "warehouse_id"
	TypeMarketplace = "marketplace_id"
	TypeChannel     = "channel"
	TypeCompany     = "company"
)

// Statuses.
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// Sentinel errors.
var (
	ErrNoRoute         = errors.New("routing: no active route for key")
	ErrNotFound        = errors.New("routing: not found")
	ErrAlreadyExists   = errors.New("routing: route already exists")
	ErrInvalidArgument = errors.New("routing: invalid argument")
)

// Key is the attribute an event is routed on.
type Key struct {
	Type  string
	Value string
}

// String renders the key as type=value.
func (k Key) String() string { return k.Type + "=" + k.Value }

// Route is one configured mapping.
type Route struct {
	ID            uuid.UUID
	IntegrationID uuid.UUID
	Type          string
	Value         string
	// DestinationReference is the vendor-side location for this route, e.g.
	// Vinculum's three-character orderLocation.
	DestinationReference string
	// OriginReference is the origin-side location: for EasyEcom the
	// location_key whose JWT is scoped to that location. Empty means the
	// process default.
	//
	// The two are separate because they are different systems' names for
	// different ends of the same pipeline, and conflating them would make a
	// stock push authenticate for whichever one happened to be set.
	OriginReference string
	Status          string
	CreatedAt       time.Time
}

// Resolution is the outcome of routing an event.
type Resolution struct {
	Route               Route
	IntegrationID       uuid.UUID
	IntegrationName     string
	SourcePlatform      string
	DestinationPlatform string
}

// Resolver finds the integration for an event.
type Resolver interface {
	// Resolve returns the active route for key within integrationID, or
	// ErrNoRoute when the key is not configured for that integration.
	Resolve(ctx context.Context, integrationID uuid.UUID, key Key) (*Resolution, error)
}
