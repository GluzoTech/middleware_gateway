package order_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/gluzo/integration-gateway/app/integrations/easyecom/dto/order"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

func TestGetOrderDetailsResponseShapes(t *testing.T) {
	tests := []struct {
		name       string
		file       string
		wantOrders int
		wantNext   string
	}{
		{"v2 array", "order_details_v2.json", 1, ""},
		{"v1 wrapped", "order_details_v1.json", 2, "https://api.easyecom.io/orders/getAllOrders?page=2"},
		{"single object", "order_details_single.json", 1, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp order.GetOrderDetailsResponse
			if err := json.Unmarshal(fixture(t, tt.file), &resp); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if resp.Code != 200 {
				t.Fatalf("code = %d", resp.Code)
			}
			if len(resp.Data.Orders) != tt.wantOrders {
				t.Fatalf("orders = %d, want %d", len(resp.Data.Orders), tt.wantOrders)
			}
			if resp.Data.NextURL != tt.wantNext {
				t.Fatalf("nextUrl = %q, want %q", resp.Data.NextURL, tt.wantNext)
			}
		})
	}
}

func TestOrderFieldsAndFlexibleTypes(t *testing.T) {
	var resp order.GetOrderDetailsResponse
	if err := json.Unmarshal(fixture(t, "order_details_v2.json"), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	o := resp.Data.Orders[0]
	if o.OrderID != "9876543" || o.InvoiceID != "INV-1001" || o.ReferenceCode != "AMZ-403-1234567" {
		t.Fatalf("identifiers not decoded: %+v", o)
	}
	if o.WarehouseID != "12345" {
		t.Fatalf("numeric warehouse_id not decoded as string: %q", o.WarehouseID)
	}
	if float64(o.TotalAmount) != 1499.5 {
		t.Fatalf("string total_amount not decoded: %v", o.TotalAmount)
	}
	if o.PaymentMode != "COD" || o.OrderStatus != "Pending" || o.PinCode != "411001" {
		t.Fatalf("unexpected scalar fields: %+v", o)
	}
	items := o.Items()
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	if items[0].SKU != "BCP-CHY-500" || int64(items[0].Quantity) != 2 || float64(items[0].SellingPrice) != 499.75 || float64(items[0].TaxRate) != 18 {
		t.Fatalf("item not decoded: %+v", items[0])
	}
}

func TestItemsFallsBackToSuborders(t *testing.T) {
	var resp order.GetOrderDetailsResponse
	if err := json.Unmarshal(fixture(t, "order_details_v1.json"), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := resp.Data.Orders[0].Items(); len(got) != 1 || got[0].SKU != "BCP-HNY-250" {
		t.Fatalf("suborders not used as items: %+v", got)
	}
}

func TestOrdersDataRejectsScalars(t *testing.T) {
	var d order.OrdersData
	if err := json.Unmarshal([]byte(`"oops"`), &d); err == nil {
		t.Fatal("expected error for scalar data")
	}
	if err := json.Unmarshal([]byte(`null`), &d); err != nil || len(d.Orders) != 0 {
		t.Fatalf("null should decode to no orders: %v %+v", err, d)
	}
	if err := json.Unmarshal([]byte(`{"message":"nothing here"}`), &d); err != nil || len(d.Orders) != 0 {
		t.Fatalf("object without orders should decode to none: %v %+v", err, d)
	}
}

func TestGetOrderDetailsRequest(t *testing.T) {
	if err := (order.GetOrderDetailsRequest{}).Validate(); err == nil {
		t.Fatal("empty request must be invalid")
	}
	q := order.GetOrderDetailsRequest{InvoiceID: " INV-1 ", ReferenceCode: "REF"}.Query()
	if q.Get("invoice_id") != "INV-1" || q.Has("reference_code") {
		t.Fatalf("invoice id should win: %v", q)
	}
	q = order.GetOrderDetailsRequest{ReferenceCode: "REF"}.Query()
	if q.Get("reference_code") != "REF" || q.Has("invoice_id") {
		t.Fatalf("reference code query wrong: %v", q)
	}
}
