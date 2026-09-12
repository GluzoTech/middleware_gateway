package tracking_test

import (
	"strings"
	"testing"

	"github.com/gluzo/integration-gateway/app/domain/tracking"
)

func TestShipmentValidate(t *testing.T) {
	tests := []struct {
		name     string
		shipment tracking.Shipment
		wantErr  string
	}{
		{"valid with tracking number", tracking.Shipment{OrderExternalID: "1001", TrackingNumber: "AWB1"}, ""},
		{"valid with carrier only", tracking.Shipment{OrderExternalID: "1001", Carrier: "Delhivery"}, ""},
		{"missing order", tracking.Shipment{TrackingNumber: "AWB1"}, "order external id is required"},
		{"no carrier or number", tracking.Shipment{OrderExternalID: "1001"}, "tracking number or carrier is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.shipment.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
