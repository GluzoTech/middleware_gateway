package mapper_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gluzo/integration-gateway/app/domain/tracking"
	dtoinventory "github.com/gluzo/integration-gateway/app/integrations/vinculum/dto/inventory"
	dtoshipment "github.com/gluzo/integration-gateway/app/integrations/vinculum/dto/shipment"
	"github.com/gluzo/integration-gateway/app/integrations/vinculum/mapper"
)

// observedAt is the fixed clock every mapping in this file is stamped with.
// The mappers take it as an argument precisely so a test needs no clock.
var observedAt = time.Date(2026, 9, 29, 6, 0, 0, 0, time.UTC)

func load(t *testing.T, name string, out any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
}

func TestSellableQuantity(t *testing.T) {
	// A1: sellable is what is held minus what is already promised.
	tests := []struct {
		name           string
		qty, committed int
		want           int
	}{
		{"nothing committed", 80, 0, 80},
		{"part committed", 120, 20, 100},
		{"all committed", 15, 15, 0},
		{"over-committed clamps at zero", 5, 8, 0},
		{"empty", 0, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mapper.SellableQuantity(tc.qty, tc.committed); got != tc.want {
				t.Errorf("SellableQuantity(%d, %d) = %d, want %d", tc.qty, tc.committed, got, tc.want)
			}
		})
	}
}

func TestToDomainStock(t *testing.T) {
	var payload dtoinventory.GetWhInventoryResponse
	load(t, "get_wh_inventory.json", &payload)

	if payload.Envelope.Failed() {
		t.Fatalf("recorded payload reports failure: %+v", payload.Envelope)
	}
	if len(payload.Response) != 6 {
		t.Fatalf("payload has %d rows, want 6", len(payload.Response))
	}

	t.Run("sellable bucket only", func(t *testing.T) {
		levels, sum := mapper.ToDomainStock(payload.Response, mapper.StockOptions{
			SellableBucket: "Good",
			ObservedAt:     observedAt,
		})
		if sum.Filtered != 1 {
			t.Errorf("filtered = %d, want 1 (the Damaged row)", sum.Filtered)
		}
		if sum.Skipped != 1 {
			t.Errorf("skipped = %d, want 1 (the row with no SKU)", sum.Skipped)
		}

		want := []struct {
			sku                 string
			available, reserved int
		}{
			{"BCPL-CHY-500", 100, 20},
			{"BCPL-HNY-250", 80, 0},
			{"BCPL-AML-200", 0, 15},
			{"BCPL-OVR-010", 0, 8},
		}
		if len(levels) != len(want) {
			t.Fatalf("mapped %d levels, want %d: %+v", len(levels), len(want), levels)
		}
		for i, w := range want {
			got := levels[i]
			if got.SKU != w.sku || got.Available != w.available || got.Reserved != w.reserved {
				t.Errorf("level %d = {%s %d %d}, want {%s %d %d}", i,
					got.SKU, got.Available, got.Reserved, w.sku, w.available, w.reserved)
			}
			if got.WarehouseID != "DEL" {
				t.Errorf("level %d warehouse = %q, want DEL", i, got.WarehouseID)
			}
			if !got.UpdatedAt.Equal(observedAt) {
				t.Errorf("level %d stamped %s, want the observation time", i, got.UpdatedAt)
			}
			if err := got.Validate(); err != nil {
				t.Errorf("level %d does not validate: %v", i, err)
			}
		}
	})

	t.Run("no bucket configured accepts every bucket", func(t *testing.T) {
		levels, sum := mapper.ToDomainStock(payload.Response, mapper.StockOptions{ObservedAt: observedAt})
		if sum.Filtered != 0 {
			t.Errorf("filtered = %d, want 0", sum.Filtered)
		}
		if len(levels) != 5 {
			t.Fatalf("mapped %d levels, want 5 (the damaged row included)", len(levels))
		}
	})

	t.Run("bucket match ignores case", func(t *testing.T) {
		levels, _ := mapper.ToDomainStock(payload.Response, mapper.StockOptions{
			SellableBucket: "GOOD",
			ObservedAt:     observedAt,
		})
		if len(levels) != 4 {
			t.Fatalf("mapped %d levels, want 4", len(levels))
		}
	})

	t.Run("empty input", func(t *testing.T) {
		levels, sum := mapper.ToDomainStock(nil, mapper.StockOptions{ObservedAt: observedAt})
		if len(levels) != 0 || sum.Filtered != 0 || sum.Skipped != 0 {
			t.Errorf("empty input produced %d levels and %+v", len(levels), sum)
		}
	})
}

func TestToDomainShipments(t *testing.T) {
	var payload dtoshipment.ShipmentDetailResponse
	load(t, "shipment_detail.json", &payload)

	if payload.Envelope.Failed() {
		t.Fatalf("recorded payload reports failure: %+v", payload.Envelope)
	}
	if len(payload.Response) != 5 {
		t.Fatalf("payload has %d records, want 5", len(payload.Response))
	}

	shipments, sum := mapper.ToDomainShipments(payload.Response, observedAt)

	if sum.NotShipped != 1 {
		t.Errorf("not shipped = %d, want 1 (the accepted order)", sum.NotShipped)
	}
	if sum.Skipped != 1 {
		t.Errorf("skipped = %d, want 1 (the record with no identifier)", sum.Skipped)
	}
	if len(shipments) != 3 {
		t.Fatalf("mapped %d shipments, want 3: %+v", len(shipments), shipments)
	}

	tests := []struct {
		orderID        string
		carrier        string
		trackingNumber string
		status         tracking.Status
		sourceStatus   string
		invoice        string
		gstin          string
		shippedAt      time.Time
	}{
		{
			orderID: "9876543", carrier: "Delhivery", trackingNumber: "AWB10000001",
			status: tracking.StatusShipped, sourceStatus: "Shipped",
			invoice: "BCPL/2026/00891", gstin: "07AABCB1234C1ZQ",
			shippedAt: time.Date(2026, 9, 28, 14, 5, 0, 0, time.UTC),
		},
		{
			// The dispatch block's own status wins over the order-level one.
			orderID: "9876544", carrier: "Blue Dart", trackingNumber: "AWB10000002",
			status: tracking.StatusDelivered, sourceStatus: "DELIVERED",
			invoice: "BCPL/2026/00892", gstin: "07AABCB1234C1ZQ",
			shippedAt: time.Date(2026, 9, 26, 9, 12, 30, 0, time.UTC),
		},
		{
			// No extOrderNo, so Vinculum's own order_no identifies it, and
			// the empty dispatch status falls back to the order-level one.
			orderID: "VIN-000124", carrier: "Ecom Express", trackingNumber: "AWB10000004",
			status: tracking.StatusInTransit, sourceStatus: "In Transit",
			invoice: "BCPL/2026/00894", gstin: "07AABCB1234C1ZQ",
			shippedAt: time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC),
		},
	}

	for i, want := range tests {
		got := shipments[i]
		switch {
		case got.OrderExternalID != want.orderID:
			t.Errorf("shipment %d order = %q, want %q", i, got.OrderExternalID, want.orderID)
		case got.Carrier != want.carrier:
			t.Errorf("shipment %d carrier = %q, want %q", i, got.Carrier, want.carrier)
		case got.TrackingNumber != want.trackingNumber:
			t.Errorf("shipment %d awb = %q, want %q", i, got.TrackingNumber, want.trackingNumber)
		case got.Status != want.status:
			t.Errorf("shipment %d status = %q, want %q", i, got.Status, want.status)
		case got.SourceStatus != want.sourceStatus:
			t.Errorf("shipment %d source status = %q, want %q", i, got.SourceStatus, want.sourceStatus)
		case got.InvoiceNumber != want.invoice:
			t.Errorf("shipment %d invoice = %q, want %q", i, got.InvoiceNumber, want.invoice)
		case got.SellerGSTIN != want.gstin:
			t.Errorf("shipment %d seller gstin = %q, want %q", i, got.SellerGSTIN, want.gstin)
		case got.ShippedAt == nil || !got.ShippedAt.Equal(want.shippedAt):
			t.Errorf("shipment %d shipped at = %v, want %s", i, got.ShippedAt, want.shippedAt)
		case !got.UpdatedAt.Equal(observedAt):
			t.Errorf("shipment %d stamped %s, want the observation time", i, got.UpdatedAt)
		}
	}
}

func TestToDomainShipmentNotShippedIsNotAnError(t *testing.T) {
	rec := dtoshipment.OrderShipment{ExtOrderNo: "9876545", Status: "Accepted"}
	s, err := mapper.ToDomainShipment(rec, observedAt)
	if err != nil {
		t.Fatalf("an unshipped order is an ordinary state, got error: %v", err)
	}
	if s != nil {
		t.Fatalf("expected no shipment, got %+v", s)
	}
}

func TestToDomainShipmentRejectsUnidentifiableRecord(t *testing.T) {
	_, err := mapper.ToDomainShipment(dtoshipment.OrderShipment{}, observedAt)
	if err == nil {
		t.Fatal("a record with no order identifier must not map")
	}
}

func TestToDomainShipmentRejectsUnparsableShipDate(t *testing.T) {
	rec := dtoshipment.OrderShipment{
		ExtOrderNo: "9876543",
		ShipDetail: &dtoshipment.ShipDetail{TrackingNumber: "AWB1", ShipDate: "last Tuesday"},
	}
	if _, err := mapper.ToDomainShipment(rec, observedAt); err == nil {
		t.Fatal("an unparsable dispatch date must not map silently")
	}
}

func TestNormaliseShipmentStatus(t *testing.T) {
	tests := []struct {
		in   string
		want tracking.Status
	}{
		{"", tracking.StatusUnknown},
		{"Shipped", tracking.StatusShipped},
		{"DISPATCHED", tracking.StatusShipped},
		{"Handover to courier", tracking.StatusShipped},
		{"In Transit", tracking.StatusInTransit},
		{"Out for delivery", tracking.StatusOutForDelivery},
		{"OFD", tracking.StatusOutForDelivery},
		{"Delivered", tracking.StatusDelivered},
		{"RTO Initiated", tracking.StatusReturned},
		{"Return to origin", tracking.StatusReturned},
		{"Cancelled", tracking.StatusCancelled},
		{"Packing", tracking.StatusPending},
		{"New", tracking.StatusPending},
		{"Schrodinger", tracking.StatusUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := mapper.NormaliseShipmentStatus(tc.in); got != tc.want {
				t.Errorf("NormaliseShipmentStatus(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A cancelled order must not be read as delivered because "cancelled"
// happens to contain no delivery keyword and "Delivery cancelled" contains
// both. Order of the keyword checks is the only thing keeping these apart.
func TestNormaliseShipmentStatusPrefersTheMoreSpecificKeyword(t *testing.T) {
	if got := mapper.NormaliseShipmentStatus("Delivery cancelled"); got != tracking.StatusCancelled {
		t.Errorf("got %q, want %q", got, tracking.StatusCancelled)
	}
	if got := mapper.NormaliseShipmentStatus("Out for delivery"); got != tracking.StatusOutForDelivery {
		t.Errorf("got %q, want %q", got, tracking.StatusOutForDelivery)
	}
}
