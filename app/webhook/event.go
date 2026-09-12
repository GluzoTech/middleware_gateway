// Package webhook receives platform events, authenticates and validates
// them, assigns correlation IDs, enforces idempotency and queues them for
// asynchronous processing. It never calls a destination platform.
package webhook

import (
	"encoding/json"
	"time"
)

// Event types the gateway understands. Platform parsers translate a
// platform's own trigger names into these.
const (
	EventOrderCreated     = "ORDER_CREATED"
	EventOrderConfirmed   = "ORDER_CONFIRMED"
	EventOrderCancelled   = "ORDER_CANCELLED"
	EventInventoryUpdated = "INVENTORY_UPDATED"
	EventTrackingUpdated  = "TRACKING_UPDATED"
)

// pathEventTypes maps the optional URL segment after /webhooks/<platform>/
// to an event type. A request without a segment is an order-created event.
var pathEventTypes = map[string]string{
	"":                  EventOrderCreated,
	"order-created":     EventOrderCreated,
	"order-confirmed":   EventOrderConfirmed,
	"order-cancelled":   EventOrderCancelled,
	"inventory-updated": EventInventoryUpdated,
	"tracking-updated":  EventTrackingUpdated,
}

// EventTypeFromPath resolves the URL segment naming the trigger.
func EventTypeFromPath(segment string) (string, bool) {
	t, ok := pathEventTypes[segment]
	return t, ok
}

// RoutingKey is the attribute the router uses to pick an integration.
type RoutingKey struct {
	Type  string `json:"type"`  // e.g. "warehouse_id"
	Value string `json:"value"` // e.g. "12345"
}

// Event is the platform-neutral description of one inbound business event.
// It is what gets queued; the platform payload travels with it so the worker
// can decode it with the platform's own DTOs.
type Event struct {
	Platform        string          `json:"platform"`
	EventType       string          `json:"event_type"`
	CorrelationID   string          `json:"correlation_id"`
	IntegrationID   string          `json:"integration_id"`
	ExternalOrderID string          `json:"external_order_id,omitempty"`
	ReferenceCode   string          `json:"reference_code,omitempty"`
	InvoiceNumber   string          `json:"invoice_number,omitempty"`
	RoutingKey      RoutingKey      `json:"routing_key"`
	IdempotencyKey  string          `json:"idempotency_key"`
	ReceivedAt      time.Time       `json:"received_at"`
	Payload         json.RawMessage `json:"payload"`
}

// Parser turns a platform's raw webhook body into events. Implementations
// live with the platform's DTOs and must not leak them; validation failures
// are returned as apperror.Validation errors.
type Parser interface {
	// Platform names the platform whose payloads this parser understands.
	Platform() string
	// Parse validates body for eventType and returns one Event per business
	// object it contains. Platform, EventType, identifiers, RoutingKey,
	// IdempotencyKey and Payload are set; the handler fills in the rest.
	Parse(eventType string, body []byte) ([]Event, error)
}
