// Package event defines the platform-neutral description of an inbound
// business event. It is produced by webhook parsers, travels on the queue,
// and is the input to workflows. It deliberately depends on nothing but the
// standard library so that intake, queue and workflow packages can all share
// it without importing each other.
package event

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Event types the gateway understands. Platform parsers translate a
// platform's own trigger names into these.
const (
	OrderCreated     = "ORDER_CREATED"
	OrderConfirmed   = "ORDER_CONFIRMED"
	OrderCancelled   = "ORDER_CANCELLED"
	InventoryUpdated = "INVENTORY_UPDATED"
	TrackingUpdated  = "TRACKING_UPDATED"
)

// IsOrderEvent reports whether t concerns a single order.
func IsOrderEvent(t string) bool {
	switch t {
	case OrderCreated, OrderConfirmed, OrderCancelled:
		return true
	}
	return false
}

// RoutingKey is the attribute the router uses to pick an integration.
type RoutingKey struct {
	Type  string `json:"type"`  // e.g. "warehouse_id"
	Value string `json:"value"` // e.g. "12345"
}

// String renders the key as type=value for logs.
func (k RoutingKey) String() string { return k.Type + "=" + k.Value }

// Event is one inbound business event. The platform payload travels with it
// so the worker can decode it with the platform's own DTOs.
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

// Validate checks that an event carries everything downstream stages depend
// on. Every parser is held to the same contract.
func Validate(e Event) error {
	var errs []error
	if strings.TrimSpace(e.Platform) == "" {
		errs = append(errs, errors.New("platform is required"))
	}
	if strings.TrimSpace(e.EventType) == "" {
		errs = append(errs, errors.New("event type is required"))
	}
	if strings.TrimSpace(e.IdempotencyKey) == "" {
		errs = append(errs, errors.New("idempotency key is required"))
	}
	if len(e.Payload) == 0 {
		errs = append(errs, errors.New("payload is required"))
	}
	switch e.EventType {
	case OrderCreated, OrderConfirmed, OrderCancelled:
		if strings.TrimSpace(e.ExternalOrderID) == "" {
			errs = append(errs, errors.New("external order id is required for order events"))
		}
		if strings.TrimSpace(e.RoutingKey.Type) == "" || strings.TrimSpace(e.RoutingKey.Value) == "" {
			errs = append(errs, errors.New("routing key is required for order events"))
		}
	case InventoryUpdated, TrackingUpdated, "":
	default:
		errs = append(errs, fmt.Errorf("unknown event type %q", e.EventType))
	}
	return errors.Join(errs...)
}
