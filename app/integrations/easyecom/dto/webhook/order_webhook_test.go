package webhook_test

import (
	"encoding/json"
	"testing"

	"github.com/gluzo/integration-gateway/app/integrations/easyecom/dto/webhook"
)

func TestOrderWebhookShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{"v2 array", `[{"order_id":1,"warehouse_id":5,"order_items":[]},{"order_id":2,"warehouse_id":5}]`, 2},
		{"v1 wrapped", `{"orders":[{"order_id":"1","warehouse_id":"5"}],"nextUrl":""}`, 1},
		{"single object", `{"order_id":1,"warehouse_id":5}`, 1},
		{"empty array", `[]`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var w webhook.OrderWebhook
			if err := json.Unmarshal([]byte(tt.body), &w); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(w.Orders) != tt.want {
				t.Fatalf("orders = %d, want %d", len(w.Orders), tt.want)
			}
		})
	}
}

func TestOrderWebhookRejectsMalformed(t *testing.T) {
	var w webhook.OrderWebhook
	for _, body := range []string{`"text"`, `42`, `[{"order_id":{}}]`, `{"orders":"x"}`} {
		if err := json.Unmarshal([]byte(body), &w); err == nil {
			t.Errorf("body %s should not decode", body)
		}
	}
}
