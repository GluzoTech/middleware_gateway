package mapper_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/domain/order"
	"github.com/gluzo/integration-gateway/app/domain/tracking"
	dtoinventory "github.com/gluzo/integration-gateway/app/integrations/easyecom/dto/inventory"
	dtoorder "github.com/gluzo/integration-gateway/app/integrations/easyecom/dto/order"
	dtotracking "github.com/gluzo/integration-gateway/app/integrations/easyecom/dto/tracking"
	"github.com/gluzo/integration-gateway/app/integrations/easyecom/mapper"
)

func loadOrder(t *testing.T) dtoorder.Order {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "dto", "order", "testdata", "order_details_v2.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var resp dtoorder.GetOrderDetailsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	return resp.Data.Orders[0]
}

func TestToDomainOrder(t *testing.T) {
	got, err := mapper.ToDomainOrder(loadOrder(t))
	if err != nil {
		t.Fatalf("ToDomainOrder: %v", err)
	}

	if got.ExternalID != "9876543" || got.InvoiceNumber != "INV-1001" || got.ReferenceCode != "AMZ-403-1234567" {
		t.Errorf("identifiers: %+v", got)
	}
	if got.WarehouseID != "12345" || got.Channel != "Amazon" {
		t.Errorf("routing fields: warehouse %q channel %q", got.WarehouseID, got.Channel)
	}
	if got.Status != order.StatusPending || got.SourceStatus != "Pending" {
		t.Errorf("status: %s / %s", got.Status, got.SourceStatus)
	}
	if got.PaymentMode != order.PaymentCashOnDelivery || !got.IsCashOnDelivery() {
		t.Errorf("payment mode: %s", got.PaymentMode)
	}
	if got.TotalAmount != 1499.5 || got.Currency != "INR" {
		t.Errorf("amount: %v %s", got.TotalAmount, got.Currency)
	}
	wantTime := time.Date(2026, 9, 12, 10, 30, 1, 0, time.FixedZone("IST", 19800))
	if !got.OrderedAt.Equal(wantTime) {
		t.Errorf("ordered at = %s, want %s", got.OrderedAt, wantTime)
	}
	if got.Customer.Name != "Asha Verma" || got.Customer.Phone != "9876501234" || got.Customer.Email != "asha@example.com" {
		t.Errorf("customer: %+v", got.Customer)
	}
	if got.ShippingAddress.Line1 != "12 MG Road" || got.ShippingAddress.PostalCode != "411001" || got.ShippingAddress.State != "Maharashtra" {
		t.Errorf("address: %+v", got.ShippingAddress)
	}
	if got.BillingAddress != got.ShippingAddress {
		t.Error("billing address should mirror the single EasyEcom address")
	}
	if len(got.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(got.Items))
	}
	first := got.Items[0]
	if first.ExternalID != "555001" || first.SKU != "BCP-CHY-500" || first.Quantity != 2 || first.UnitPrice != 499.75 || first.Total != 999.5 || first.TaxRate != 18 || first.TaxType != "GST" || first.Name != "Herbal Chyawanprash 500g" {
		t.Errorf("first item: %+v", first)
	}
	if got.TotalQuantity() != 3 {
		t.Errorf("total quantity = %d", got.TotalQuantity())
	}
}

func TestToDomainOrderErrors(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*dtoorder.Order)
		want   string
	}{
		{"no identifiers", func(o *dtoorder.Order) { o.OrderID, o.InvoiceID = "", "" }, "neither order_id nor invoice_id"},
		{"bad timestamp", func(o *dtoorder.Order) { o.OrderDate = "yesterday" }, "unrecognised timestamp"},
		{"item without sku", func(o *dtoorder.Order) { o.OrderItems[1].SKU = " " }, "item 1: missing sku"},
		{"zero quantity", func(o *dtoorder.Order) { o.OrderItems[0].Quantity = 0 }, "quantity must be positive"},
		{"no items", func(o *dtoorder.Order) { o.OrderItems, o.Suborders = nil, nil }, "at least one item is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := loadOrder(t)
			tt.mutate(&src)
			_, err := mapper.ToDomainOrder(src)
			if err == nil {
				t.Fatal("expected error")
			}
			if apperror.CategoryOf(err) != apperror.Mapping || apperror.IsRetryable(err) {
				t.Fatalf("expected non-retryable mapping error, got %v", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func TestInvoiceIDFallsBackAsExternalID(t *testing.T) {
	src := loadOrder(t)
	src.OrderID = ""
	got, err := mapper.ToDomainOrder(src)
	if err != nil {
		t.Fatalf("ToDomainOrder: %v", err)
	}
	if got.ExternalID != "INV-1001" {
		t.Fatalf("external id = %q, want invoice id", got.ExternalID)
	}
}

func TestParseTimestamp(t *testing.T) {
	ist := time.FixedZone("IST", 19800)
	tests := []struct {
		in      string
		want    time.Time
		wantErr bool
	}{
		{"", time.Time{}, false},
		{"2026-09-12 10:30:01", time.Date(2026, 9, 12, 10, 30, 1, 0, ist), false},
		{"2026-09-12T10:30:01", time.Date(2026, 9, 12, 10, 30, 1, 0, ist), false},
		{"2026-09-12T10:30:01Z", time.Date(2026, 9, 12, 10, 30, 1, 0, time.UTC), false},
		{"2026-09-12", time.Date(2026, 9, 12, 0, 0, 0, 0, ist), false},
		{"12/09/2026", time.Time{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := mapper.ParseTimestamp(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !got.Equal(tt.want) {
				t.Fatalf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestNormalisers(t *testing.T) {
	payments := map[string]order.PaymentMode{
		"COD": order.PaymentCashOnDelivery, "cod": order.PaymentCashOnDelivery, "Cash On Delivery": order.PaymentCashOnDelivery,
		"Prepaid": order.PaymentPrepaid, "Online": order.PaymentPrepaid, "UPI": order.PaymentPrepaid, "Credit Card": order.PaymentPrepaid,
		"": order.PaymentUnknown, "barter": order.PaymentUnknown,
	}
	for in, want := range payments {
		if got := mapper.NormalisePaymentMode(in); got != want {
			t.Errorf("NormalisePaymentMode(%q) = %s, want %s", in, got, want)
		}
	}

	statuses := map[string]order.Status{
		"Pending": order.StatusPending, "New": order.StatusPending, "Confirmed": order.StatusConfirmed, "Ready to Dispatch": order.StatusShipped,
		"Shipped": order.StatusShipped, "Manifested": order.StatusShipped, "Delivered": order.StatusDelivered,
		"Cancelled": order.StatusCancelled, "Returned": order.StatusReturned, "RTO Initiated": order.StatusReturned,
		"": order.StatusUnknown, "weird": order.StatusUnknown,
	}
	for in, want := range statuses {
		if got := mapper.NormaliseStatus(in); got != want {
			t.Errorf("NormaliseStatus(%q) = %s, want %s", in, got, want)
		}
	}

	trackingStatuses := map[string]tracking.Status{
		"In Transit": tracking.StatusInTransit, "Out For Delivery": tracking.StatusOutForDelivery, "Delivered": tracking.StatusDelivered,
		"Shipped": tracking.StatusShipped, "RTO": tracking.StatusReturned, "Cancelled": tracking.StatusCancelled, "Booked": tracking.StatusPending, "": tracking.StatusUnknown,
	}
	for in, want := range trackingStatuses {
		if got := mapper.NormaliseTrackingStatus(in); got != want {
			t.Errorf("NormaliseTrackingStatus(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestToDomainInventory(t *testing.T) {
	src := []dtoinventory.InventoryItem{
		{SKU: "A", WarehouseID: "5", AvailableInventory: 10, ReservedInventory: 2, UpdatedAt: "2026-09-12 08:00:00"},
		{SKU: "", AvailableInventory: 1},
		{SKU: "B", AvailableInventory: 3, UpdatedAt: "not a date"},
		{SKU: "C", AvailableInventory: 0},
	}
	levels, skipped := mapper.ToDomainInventory(src)
	if skipped != 2 {
		t.Fatalf("skipped = %d, want 2", skipped)
	}
	if len(levels) != 2 || levels[0].SKU != "A" || levels[0].Available != 10 || levels[0].Reserved != 2 || levels[0].WarehouseID != "5" || levels[1].SKU != "C" {
		t.Fatalf("levels = %+v", levels)
	}
}

func TestToDomainShipment(t *testing.T) {
	got, err := mapper.ToDomainShipment(dtotracking.TrackingDetail{
		OrderID: "1001", AWBNumber: "AWB1", Carrier: "Delhivery", TrackingURL: "https://t.example/AWB1",
		Status: "In Transit", ShippedAt: "2026-09-12 09:00:00", UpdatedAt: "2026-09-12 10:00:00",
	})
	if err != nil {
		t.Fatalf("ToDomainShipment: %v", err)
	}
	if got.OrderExternalID != "1001" || got.TrackingNumber != "AWB1" || got.Carrier != "Delhivery" || got.Status != tracking.StatusInTransit || got.ShippedAt == nil {
		t.Fatalf("unexpected shipment: %+v", got)
	}

	if _, err := mapper.ToDomainShipment(dtotracking.TrackingDetail{AWBNumber: "AWB1"}); apperror.CategoryOf(err) != apperror.Mapping {
		t.Fatalf("expected mapping error for missing order id, got %v", err)
	}
	if _, err := mapper.ToDomainShipment(dtotracking.TrackingDetail{OrderID: "1"}); apperror.CategoryOf(err) != apperror.Mapping {
		t.Fatalf("expected mapping error for missing carrier and awb, got %v", err)
	}
}
