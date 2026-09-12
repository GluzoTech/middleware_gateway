package inventory_test

import (
	"strings"
	"testing"

	"github.com/gluzo/integration-gateway/app/domain/inventory"
)

func TestLevelValidate(t *testing.T) {
	tests := []struct {
		name    string
		level   inventory.Level
		wantErr string
	}{
		{"valid", inventory.Level{SKU: "SKU-A", WarehouseID: "W1", Available: 10}, ""},
		{"zero stock is valid", inventory.Level{SKU: "SKU-A", Available: 0}, ""},
		{"missing sku", inventory.Level{Available: 1}, "sku is required"},
		{"negative available", inventory.Level{SKU: "S", Available: -1}, "available quantity must not be negative"},
		{"negative reserved", inventory.Level{SKU: "S", Reserved: -1}, "reserved quantity must not be negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.level.Validate()
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
