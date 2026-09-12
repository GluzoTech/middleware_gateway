package easyecom_test

import (
	"context"
	"testing"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/domain/order"
	"github.com/gluzo/integration-gateway/app/event"
	"github.com/gluzo/integration-gateway/app/integrations/easyecom"
)

func newSource(t *testing.T) (*fakeEasyEcom, *easyecom.Source) {
	t.Helper()
	f := newFakeEasyEcom(t)
	c, err := easyecom.NewClient(f.config(), noBackoff())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return f, easyecom.NewSource(c, nil)
}

func TestSourceFetchOrderFromAPI(t *testing.T) {
	_, src := newSource(t)
	if src.Platform() != "easyecom" {
		t.Fatalf("platform = %q", src.Platform())
	}
	o, err := src.FetchOrder(context.Background(), event.Event{ExternalOrderID: "1", ReferenceCode: "REF-1", Payload: []byte(`{"order_id":1,"warehouse_id":5,"order_items":[{"sku":"PAYLOAD","suborder_quantity":1}]}`)})
	if err != nil {
		t.Fatalf("FetchOrder: %v", err)
	}
	if o.ExternalID != "1" || len(o.Items) != 1 || o.Items[0].SKU != "A" {
		t.Fatalf("order should come from the API, got %+v", o)
	}
}

func TestSourceFallsBackToPayload(t *testing.T) {
	payload := []byte(`{"order_id":42,"warehouse_id":5,"payment_mode":"COD","order_items":[{"sku":"PAYLOAD","suborder_quantity":2,"selling_price":5}]}`)
	tests := []struct {
		name string
		ev   event.Event
	}{
		{"no identifiers", event.Event{ExternalOrderID: "42", Payload: payload}},
		{"api returns nothing", event.Event{ExternalOrderID: "42", ReferenceCode: "REF-UNKNOWN", Payload: payload}},
		{"api reports not found", event.Event{ExternalOrderID: "42", ReferenceCode: "REF-APP-ERR", Payload: payload}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, src := newSource(t)
			o, err := src.FetchOrder(context.Background(), tt.ev)
			if err != nil {
				t.Fatalf("FetchOrder: %v", err)
			}
			if o.ExternalID != "42" || o.Items[0].SKU != "PAYLOAD" || o.PaymentMode != order.PaymentCashOnDelivery {
				t.Fatalf("order should come from the payload, got %+v", o)
			}
		})
	}
}

func TestSourcePropagatesTransientErrors(t *testing.T) {
	_, src := newSource(t)
	_, err := src.FetchOrder(context.Background(), event.Event{ExternalOrderID: "1", ReferenceCode: "REF-SERVER-ERR", Payload: []byte(`{"order_id":1,"warehouse_id":5,"order_items":[{"sku":"A","suborder_quantity":1}]}`)})
	if err == nil || !apperror.IsRetryable(err) {
		t.Fatalf("expected retryable error, got %v", err)
	}
}

func TestSourceRejectsUnusablePayload(t *testing.T) {
	_, src := newSource(t)
	if _, err := src.FetchOrder(context.Background(), event.Event{ExternalOrderID: "1"}); apperror.CategoryOf(err) != apperror.Validation {
		t.Fatalf("empty payload: %v", err)
	}
	if _, err := src.FetchOrder(context.Background(), event.Event{ExternalOrderID: "1", Payload: []byte(`{"order_id":1}`)}); apperror.CategoryOf(err) != apperror.Mapping {
		t.Fatalf("payload without items should be a mapping error: %v", err)
	}
}

func TestSourceInventoryAndTracking(t *testing.T) {
	_, src := newSource(t)
	o := order.Order{ExternalID: "1", InvoiceNumber: "INV-1", WarehouseID: "5", Items: []order.Item{{SKU: "DAB-1", Quantity: 1}, {SKU: "DAB-1", Quantity: 2}, {SKU: "DAB-2", Quantity: 1}}}

	levels, err := src.FetchInventory(context.Background(), o)
	if err != nil {
		t.Fatalf("FetchInventory: %v", err)
	}
	if len(levels) != 2 || levels[0].SKU != "DAB-1" || levels[0].Available != 42 || levels[1].SKU != "DAB-2" {
		t.Fatalf("levels = %+v (duplicate SKUs must be queried once)", levels)
	}

	shipment, err := src.FetchTracking(context.Background(), o)
	if err != nil {
		t.Fatalf("FetchTracking: %v", err)
	}
	if shipment == nil || shipment.TrackingNumber != "AWB1" || shipment.Carrier != "Delhivery" {
		t.Fatalf("shipment = %+v", shipment)
	}

	if s, err := src.FetchTracking(context.Background(), order.Order{ExternalID: "1"}); err != nil || s != nil {
		t.Fatalf("order without identifiers should yield no shipment: %+v %v", s, err)
	}
}
