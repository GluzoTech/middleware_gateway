package mapper_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/domain/inventory"
	"github.com/gluzo/integration-gateway/app/domain/order"
	dtoorder "github.com/gluzo/integration-gateway/app/integrations/dabur/dto/order"
	"github.com/gluzo/integration-gateway/app/integrations/dabur/mapper"
)

func sampleOrder() order.Order {
	addr := order.Address{Name: "Asha Verma", Line1: "12 MG Road", Line2: "Near Station", City: "Pune", State: "Maharashtra", Country: "India", PostalCode: "411001", Phone: "9876501234", Email: "asha@example.com"}
	return order.Order{
		ExternalID:      "9876543",
		InvoiceNumber:   "INV-1001",
		ReferenceCode:   "AMZ-403-1234567",
		Channel:         "Amazon",
		WarehouseID:     "12345",
		OrderedAt:       time.Date(2026, 9, 12, 10, 30, 1, 0, time.UTC),
		Customer:        order.Customer{Name: "Asha Verma", Email: "asha@example.com", Phone: "9876501234"},
		ShippingAddress: addr,
		BillingAddress:  addr,
		PaymentMode:     order.PaymentCashOnDelivery,
		Currency:        "INR",
		ShippingCharges: 40,
		Discount:        10,
		TotalAmount:     1499.5,
		Items: []order.Item{
			{ExternalID: "555001", SKU: "DAB-CHY-500", Quantity: 2, UnitPrice: 499.75, Discount: 10, TaxRate: 18, Total: 999.5},
			{ExternalID: "555002", SKU: "DAB-HNY-250", Quantity: 1, UnitPrice: 500, Total: 500},
		},
	}
}

func TestToCreateSaleOrderRequest(t *testing.T) {
	req, err := mapper.ToCreateSaleOrderRequest(sampleOrder(), mapper.OrderOptions{FacilityCode: "DABUR-DEL", Channel: "GLUZO", VerificationRequired: false})
	if err != nil {
		t.Fatalf("ToCreateSaleOrderRequest: %v", err)
	}
	so := req.SaleOrder
	if so.Code != "9876543" || so.DisplayOrderCode != "AMZ-403-1234567" || so.Channel != "GLUZO" || so.CustomerName != "Asha Verma" {
		t.Errorf("header fields: %+v", so)
	}
	if !so.CashOnDelivery || so.PaymentInstrument != dtoorder.PaymentInstrumentCash || so.TotalPrepaidAmount != 0 {
		t.Errorf("COD fields: cod=%v instrument=%q prepaid=%v", so.CashOnDelivery, so.PaymentInstrument, so.TotalPrepaidAmount)
	}
	if so.TotalShippingCharges != 40 || so.TotalDiscount != 10 || so.CurrencyCode != "INR" || so.TaxExempted || so.VerificationRequired || !so.UseVerifiedListings {
		t.Errorf("totals/flags: %+v", so)
	}
	if so.DisplayOrderDateTime == nil || *so.DisplayOrderDateTime != time.Date(2026, 9, 12, 10, 30, 1, 0, time.UTC).UnixMilli() {
		t.Errorf("display order date = %v", so.DisplayOrderDateTime)
	}
	if len(so.Addresses) != 1 || so.Addresses[0].ID != "1" || so.Addresses[0].City != "Pune" || so.Addresses[0].Pincode != "411001" || so.BillingAddress.ReferenceID != "1" || so.ShippingAddress.ReferenceID != "1" {
		t.Errorf("addresses: %+v", so.Addresses)
	}
	if len(so.SaleOrderItems) != 3 {
		t.Fatalf("items = %d, want 3 (quantity expanded to one item per unit)", len(so.SaleOrderItems))
	}
	first, second, third := so.SaleOrderItems[0], so.SaleOrderItems[1], so.SaleOrderItems[2]
	if first.Code != "555001-1" || second.Code != "555001-2" || third.Code != "555002-1" {
		t.Errorf("item codes: %s %s %s", first.Code, second.Code, third.Code)
	}
	if first.ItemSku != "DAB-CHY-500" || first.SellingPrice != 499.75 || first.TotalPrice != 499.75 || first.Discount != 5 || first.PrepaidAmount != 0 || first.FacilityCode != "DABUR-DEL" || first.ShippingMethodCode != "STD" {
		t.Errorf("first item: %+v", first)
	}
	if !strings.Contains(so.AdditionalInfo, "INV-1001") || !strings.Contains(so.AdditionalInfo, "Amazon") {
		t.Errorf("additional info = %q", so.AdditionalInfo)
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"cashOnDelivery":true`, `"giftWrap":false`, `"taxExempted":false`, `"verificationRequired":false`, `"saleOrder":{`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("json lacks %s: %s", want, data)
		}
	}
}

func TestPrepaidOrderFields(t *testing.T) {
	o := sampleOrder()
	o.PaymentMode = order.PaymentPrepaid
	req, err := mapper.ToCreateSaleOrderRequest(o, mapper.OrderOptions{FacilityCode: "F"})
	if err != nil {
		t.Fatalf("ToCreateSaleOrderRequest: %v", err)
	}
	so := req.SaleOrder
	if so.CashOnDelivery || so.PaymentInstrument != "" || so.TotalPrepaidAmount != 1499.5 {
		t.Fatalf("prepaid fields: %+v", so)
	}
	if so.SaleOrderItems[0].PrepaidAmount != 499.75 {
		t.Fatalf("item prepaid amount = %v", so.SaleOrderItems[0].PrepaidAmount)
	}
}

func TestMappingErrorsNameTheField(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*order.Order)
		want   string
	}{
		{"no external id", func(o *order.Order) { o.ExternalID = "" }, "external id is required"},
		{"code too long", func(o *order.Order) { o.ExternalID = strings.Repeat("9", 46) }, "exceeds 45"},
		{"no state", func(o *order.Order) { o.ShippingAddress.State = "" }, "state is required"},
		{"no phone anywhere", func(o *order.Order) { o.ShippingAddress.Phone = ""; o.Customer.Phone = "" }, "phone is required"},
		{"no items", func(o *order.Order) { o.Items = nil }, "order has no items"},
		{"item without sku", func(o *order.Order) { o.Items[0].SKU = "" }, "item 0: sku is required"},
		{"zero quantity", func(o *order.Order) { o.Items[1].Quantity = 0 }, "quantity must be positive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := sampleOrder()
			tt.mutate(&o)
			_, err := mapper.ToCreateSaleOrderRequest(o, mapper.OrderOptions{})
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

func TestAddressFallsBackToCustomerDetails(t *testing.T) {
	o := sampleOrder()
	o.ShippingAddress.Name, o.ShippingAddress.Phone, o.ShippingAddress.Email = "", "", ""
	req, err := mapper.ToCreateSaleOrderRequest(o, mapper.OrderOptions{})
	if err != nil {
		t.Fatalf("ToCreateSaleOrderRequest: %v", err)
	}
	a := req.SaleOrder.Addresses[0]
	if a.Name != "Asha Verma" || a.Phone != "9876501234" || a.Email != "asha@example.com" {
		t.Fatalf("address did not fall back to customer: %+v", a)
	}
}

func TestToInventoryAdjustments(t *testing.T) {
	req := mapper.ToInventoryAdjustments([]inventory.Level{
		{SKU: "A", Available: 10},
		{SKU: " ", Available: 1},
		{SKU: "B", Available: 0},
	}, mapper.InventoryOptions{FacilityCode: "F1", ShelfCode: "DEFAULT", Remarks: "gluzo sync"})
	if len(req.InventoryAdjustments) != 2 || req.ForceAllocate {
		t.Fatalf("adjustments = %+v", req)
	}
	a := req.InventoryAdjustments[0]
	if a.ItemSKU != "A" || a.Quantity != 10 || a.ShelfCode != "DEFAULT" || a.FacilityCode != "F1" || a.AdjustmentType != "REPLACE" || a.InventoryType != "GOOD_INVENTORY" || a.Remarks != "gluzo sync" {
		t.Fatalf("adjustment = %+v", a)
	}
}
