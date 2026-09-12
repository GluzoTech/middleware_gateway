package easyecom

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/idempotency"
	dtoorder "github.com/gluzo/integration-gateway/app/integrations/easyecom/dto/order"
	"github.com/gluzo/integration-gateway/app/webhook"
)

// RoutingKeyWarehouse is the routing attribute EasyEcom order events carry.
const RoutingKeyWarehouse = "warehouse_id"

// WebhookParser turns EasyEcom order webhooks into gateway events.
//
// Each order object in the payload becomes one event. The original object's
// bytes travel as the event payload so the worker can decode it with the
// same DTO without losing fields the DTO does not model.
type WebhookParser struct{}

// Platform implements webhook.Parser.
func (WebhookParser) Platform() string { return PlatformName }

// Parse implements webhook.Parser.
func (WebhookParser) Parse(eventType string, body []byte) ([]webhook.Event, error) {
	switch eventType {
	case webhook.EventOrderCreated, webhook.EventOrderConfirmed, webhook.EventOrderCancelled:
	case webhook.EventInventoryUpdated, webhook.EventTrackingUpdated:
		// TODO(VERIFY): the Update Inventory and Tracking trigger payloads
		// are not publicly documented. Do not configure these triggers in
		// EasyEcom until their contracts are confirmed and workflows exist.
		return nil, validationError(fmt.Sprintf("event %s is not supported for easyecom yet", eventType))
	default:
		return nil, validationError(fmt.Sprintf("unknown event type %q", eventType))
	}

	objects, err := splitOrders(body)
	if err != nil {
		return nil, validationError("payload is not an EasyEcom order webhook: " + err.Error())
	}

	events := make([]webhook.Event, 0, len(objects))
	for i, raw := range objects {
		var o dtoorder.Order
		if err := json.Unmarshal(raw, &o); err != nil {
			return nil, validationError(fmt.Sprintf("order %d: %v", i, err))
		}
		orderID := strings.TrimSpace(o.OrderID.String())
		if orderID == "" {
			orderID = strings.TrimSpace(o.InvoiceID.String())
		}
		if orderID == "" {
			return nil, validationError(fmt.Sprintf("order %d has neither order_id nor invoice_id", i))
		}
		warehouse := strings.TrimSpace(o.WarehouseID.String())
		if warehouse == "" {
			return nil, validationError(fmt.Sprintf("order %s has no warehouse_id", orderID))
		}

		events = append(events, webhook.Event{
			Platform:        PlatformName,
			EventType:       eventType,
			ExternalOrderID: orderID,
			ReferenceCode:   strings.TrimSpace(o.ReferenceCode.String()),
			InvoiceNumber:   strings.TrimSpace(o.InvoiceID.String()),
			RoutingKey:      webhook.RoutingKey{Type: RoutingKeyWarehouse, Value: warehouse},
			IdempotencyKey:  idempotency.KeyFor(PlatformName, eventType, orderID),
			Payload:         raw,
		})
	}
	return events, nil
}

// splitOrders returns the raw JSON of each order object in a webhook body,
// accepting the bare array (V2), the {"orders": [...]} wrapper (V1) and a
// single order object.
func splitOrders(body []byte) ([]json.RawMessage, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, fmt.Errorf("empty body")
	}
	switch body[0] {
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, err
		}
		for i, item := range items {
			if len(bytes.TrimSpace(item)) == 0 || bytes.TrimSpace(item)[0] != '{' {
				return nil, fmt.Errorf("element %d is not an object", i)
			}
		}
		return items, nil
	case '{':
		var wrapped struct {
			Orders  []json.RawMessage `json:"orders"`
			OrderID json.RawMessage   `json:"order_id"`
		}
		if err := json.Unmarshal(body, &wrapped); err != nil {
			return nil, err
		}
		if wrapped.Orders != nil {
			return wrapped.Orders, nil
		}
		if wrapped.OrderID != nil {
			return []json.RawMessage{body}, nil
		}
		return nil, fmt.Errorf("object carries neither orders nor order_id")
	default:
		return nil, fmt.Errorf("body must be a JSON array or object")
	}
}

func validationError(msg string) error {
	e := apperror.New(apperror.Validation, msg)
	e.Integration = PlatformName
	return e
}
