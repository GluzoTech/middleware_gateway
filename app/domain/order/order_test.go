package order_test

import (
	"strings"
	"testing"

	"github.com/gluzo/integration-gateway/app/domain/order"
)

func validOrder() order.Order {
	return order.Order{
		ExternalID:  "1001",
		PaymentMode: order.PaymentCashOnDelivery,
		TotalAmount: 250,
		Items: []order.Item{
			{ExternalID: "1", SKU: "SKU-A", Quantity: 2, UnitPrice: 100},
			{ExternalID: "2", SKU: "SKU-B", Quantity: 1, UnitPrice: 50},
		},
	}
}

func TestOrderValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*order.Order)
		wantErr []string
	}{
		{"valid", func(*order.Order) {}, nil},
		{"missing external id", func(o *order.Order) { o.ExternalID = "  " }, []string{"external id is required"}},
		{"no items", func(o *order.Order) { o.Items = nil }, []string{"at least one item is required"}},
		{"negative total", func(o *order.Order) { o.TotalAmount = -1 }, []string{"total amount must not be negative"}},
		{"bad item", func(o *order.Order) { o.Items[1].SKU = ""; o.Items[1].Quantity = 0 }, []string{"item 1: sku is required", "quantity must be positive"}},
		{"several problems", func(o *order.Order) { o.ExternalID = ""; o.Items = nil }, []string{"external id is required", "at least one item is required"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := validOrder()
			tt.mutate(&o)
			err := o.Validate()
			if len(tt.wantErr) == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error")
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err, want)
				}
			}
		})
	}
}

func TestOrderHelpers(t *testing.T) {
	o := validOrder()
	if o.TotalQuantity() != 3 {
		t.Fatalf("TotalQuantity = %d, want 3", o.TotalQuantity())
	}
	if !o.IsCashOnDelivery() {
		t.Fatal("expected COD")
	}
	o.PaymentMode = order.PaymentPrepaid
	if o.IsCashOnDelivery() {
		t.Fatal("prepaid reported as COD")
	}
	if !(order.Address{}).IsEmpty() || (order.Address{City: "Pune"}).IsEmpty() {
		t.Fatal("Address.IsEmpty is wrong")
	}
}
