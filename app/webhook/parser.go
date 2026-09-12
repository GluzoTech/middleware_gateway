// Package webhook receives platform events, authenticates and validates
// them, assigns correlation IDs, enforces idempotency and queues them for
// asynchronous processing. It never calls a destination platform.
package webhook

import "github.com/gluzo/integration-gateway/app/event"

// pathEventTypes maps the optional URL segment after /webhooks/<platform>/
// to an event type. A request without a segment is an order-created event.
var pathEventTypes = map[string]string{
	"":                  event.OrderCreated,
	"order-created":     event.OrderCreated,
	"order-confirmed":   event.OrderConfirmed,
	"order-cancelled":   event.OrderCancelled,
	"inventory-updated": event.InventoryUpdated,
	"tracking-updated":  event.TrackingUpdated,
}

// EventTypeFromPath resolves the URL segment naming the trigger.
func EventTypeFromPath(segment string) (string, bool) {
	t, ok := pathEventTypes[segment]
	return t, ok
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
	Parse(eventType string, body []byte) ([]event.Event, error)
}
