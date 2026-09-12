package easyecom_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/integrations/easyecom"
	"github.com/gluzo/integration-gateway/app/webhook"
)

func TestWebhookParserOrderEvents(t *testing.T) {
	parser := easyecom.WebhookParser{}
	if parser.Platform() != "easyecom" {
		t.Fatalf("platform = %q", parser.Platform())
	}

	tests := []struct {
		name      string
		eventType string
		body      string
		wantIDs   []string
		wantKeys  []string
	}{
		{
			name:      "v2 array with two orders",
			eventType: webhook.EventOrderCreated,
			body:      `[{"order_id":101,"invoice_id":"INV-1","reference_code":"R1","warehouse_id":5,"extra":"kept"},{"order_id":"102","warehouse_id":"6"}]`,
			wantIDs:   []string{"101", "102"},
			wantKeys:  []string{"easyecom:ORDER_CREATED:101", "easyecom:ORDER_CREATED:102"},
		},
		{
			name:      "v1 wrapped",
			eventType: webhook.EventOrderConfirmed,
			body:      `{"orders":[{"order_id":7,"warehouse_id":1}],"nextUrl":""}`,
			wantIDs:   []string{"7"},
			wantKeys:  []string{"easyecom:ORDER_CONFIRMED:7"},
		},
		{
			name:      "single object falls back to invoice id",
			eventType: webhook.EventOrderCancelled,
			body:      `{"order_id":"","invoice_id":"INV-9","warehouse_id":3}`,
			wantIDs:   []string{"INV-9"},
			wantKeys:  []string{"easyecom:ORDER_CANCELLED:INV-9"},
		},
		{
			name:      "empty array",
			eventType: webhook.EventOrderCreated,
			body:      `[]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events, err := parser.Parse(tt.eventType, []byte(tt.body))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(events) != len(tt.wantIDs) {
				t.Fatalf("events = %d, want %d", len(events), len(tt.wantIDs))
			}
			for i, ev := range events {
				if ev.ExternalOrderID != tt.wantIDs[i] || ev.IdempotencyKey != tt.wantKeys[i] {
					t.Errorf("event %d = %+v", i, ev)
				}
				if ev.Platform != "easyecom" || ev.EventType != tt.eventType || ev.RoutingKey.Type != easyecom.RoutingKeyWarehouse || ev.RoutingKey.Value == "" {
					t.Errorf("event %d metadata = %+v", i, ev)
				}
				if !json.Valid(ev.Payload) {
					t.Errorf("event %d payload is not JSON", i)
				}
			}
		})
	}
}

func TestWebhookParserPreservesUnknownFieldsInPayload(t *testing.T) {
	events, err := easyecom.WebhookParser{}.Parse(webhook.EventOrderCreated, []byte(`[{"order_id":1,"warehouse_id":2,"undocumented_field":"kept"}]`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !strings.Contains(string(events[0].Payload), `"undocumented_field":"kept"`) {
		t.Fatalf("payload lost fields: %s", events[0].Payload)
	}
	if events[0].ReferenceCode != "" || events[0].InvoiceNumber != "" {
		t.Fatalf("unexpected identifiers: %+v", events[0])
	}
}

func TestWebhookParserRejections(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		body      string
		want      string
	}{
		{"unknown event", "SOMETHING", `[]`, "unknown event type"},
		{"unsupported event", webhook.EventInventoryUpdated, `{}`, "not supported"},
		{"empty body", webhook.EventOrderCreated, ``, "empty body"},
		{"scalar body", webhook.EventOrderCreated, `42`, "must be a JSON array or object"},
		{"array of scalars", webhook.EventOrderCreated, `[1]`, "not an object"},
		{"object without orders", webhook.EventOrderCreated, `{"hello":"world"}`, "neither orders nor order_id"},
		{"malformed json", webhook.EventOrderCreated, `[{"order_id":1,`, "not an EasyEcom order webhook"},
		{"missing identifiers", webhook.EventOrderCreated, `[{"warehouse_id":1}]`, "neither order_id nor invoice_id"},
		{"missing warehouse", webhook.EventOrderCreated, `[{"order_id":1}]`, "no warehouse_id"},
		{"bad field type", webhook.EventOrderCreated, `[{"order_id":{"x":1},"warehouse_id":1}]`, "order 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := easyecom.WebhookParser{}.Parse(tt.eventType, []byte(tt.body))
			if err == nil {
				t.Fatal("expected error")
			}
			if apperror.CategoryOf(err) != apperror.Validation || apperror.IsRetryable(err) {
				t.Fatalf("expected non-retryable validation error, got %v", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}
