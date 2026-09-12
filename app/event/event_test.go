package event_test

import (
	"strings"
	"testing"

	"github.com/gluzo/integration-gateway/app/event"
)

func TestValidate(t *testing.T) {
	good := event.Event{
		Platform:        "easyecom",
		EventType:       event.OrderCreated,
		ExternalOrderID: "1",
		RoutingKey:      event.RoutingKey{Type: "warehouse_id", Value: "5"},
		IdempotencyKey:  "k",
		Payload:         []byte(`{}`),
	}
	if err := event.Validate(good); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*event.Event)
		want   string
	}{
		{"no platform", func(e *event.Event) { e.Platform = "" }, "platform is required"},
		{"no event type", func(e *event.Event) { e.EventType = "" }, "event type is required"},
		{"no key", func(e *event.Event) { e.IdempotencyKey = "" }, "idempotency key is required"},
		{"no payload", func(e *event.Event) { e.Payload = nil }, "payload is required"},
		{"no order id", func(e *event.Event) { e.ExternalOrderID = "" }, "external order id is required"},
		{"no routing key", func(e *event.Event) { e.RoutingKey = event.RoutingKey{} }, "routing key is required"},
		{"unknown type", func(e *event.Event) { e.EventType = "NOPE" }, "unknown event type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := good
			tt.mutate(&ev)
			err := event.Validate(ev)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestHelpers(t *testing.T) {
	if !event.IsOrderEvent(event.OrderConfirmed) || event.IsOrderEvent(event.InventoryUpdated) {
		t.Fatal("IsOrderEvent is wrong")
	}
	if got := (event.RoutingKey{Type: "warehouse_id", Value: "5"}).String(); got != "warehouse_id=5" {
		t.Fatalf("String = %q", got)
	}
}
