package webhook

import (
	"errors"
	"fmt"
	"strings"
)

// Validate checks that a parsed event carries everything downstream stages
// depend on. It is applied after the platform parser so that every parser
// is held to the same contract.
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
	case EventOrderCreated, EventOrderConfirmed, EventOrderCancelled:
		if strings.TrimSpace(e.ExternalOrderID) == "" {
			errs = append(errs, errors.New("external order id is required for order events"))
		}
		if strings.TrimSpace(e.RoutingKey.Type) == "" || strings.TrimSpace(e.RoutingKey.Value) == "" {
			errs = append(errs, errors.New("routing key is required for order events"))
		}
	case EventInventoryUpdated, EventTrackingUpdated, "":
	default:
		errs = append(errs, fmt.Errorf("unknown event type %q", e.EventType))
	}
	return errors.Join(errs...)
}
