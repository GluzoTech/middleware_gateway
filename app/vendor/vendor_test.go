package vendor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gluzo/integration-gateway/app/domain/order"
)

// orderOnly implements Vendor and OrderReceiver only.
type orderOnly struct{ name string }

func (v orderOnly) Platform() string { return v.name }
func (v orderOnly) PrepareOrder(context.Context, order.Order, Route) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (v orderOnly) SubmitOrder(context.Context, json.RawMessage, order.Order, Route) (OrderAck, error) {
	return OrderAck{VendorOrderID: "x", Created: true}, nil
}

// fullVendor implements every role, as a Vinculum-shaped adapter will.
type fullVendor struct{ orderOnly }

func (v fullVendor) FetchStock(context.Context, Route, StockCursor) (StockPage, error) {
	return StockPage{}, nil
}
func (v fullVendor) FetchShipments(context.Context, Route, Window) (ShipmentPage, error) {
	return ShipmentPage{}, nil
}

// roleless implements Vendor and nothing else.
type roleless struct{ name string }

func (v roleless) Platform() string { return v.name }

func TestRegisterDiscoversRoles(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(fullVendor{orderOnly{name: "vinculum"}}); err != nil {
		t.Fatalf("register: %v", err)
	}

	got := r.Roles("vinculum")
	want := []Role{RoleFulfilmentProvider, RoleOrderReceiver, RoleStockProvider}
	if len(got) != len(want) {
		t.Fatalf("roles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("roles = %v, want %v", got, want)
		}
	}

	if _, err := r.OrderReceiver("vinculum"); err != nil {
		t.Errorf("order receiver: %v", err)
	}
	if _, err := r.StockProvider("vinculum"); err != nil {
		t.Errorf("stock provider: %v", err)
	}
	if _, err := r.FulfilmentProvider("vinculum"); err != nil {
		t.Errorf("fulfilment provider: %v", err)
	}
}

// A partner that takes orders by API but publishes stock by feed implements
// one role. Asking for the role it does not implement must say so, rather
// than reporting the vendor as unknown.
func TestPartialVendorReportsMissingRole(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(orderOnly{name: "feedpartner"}); err != nil {
		t.Fatalf("register: %v", err)
	}

	if _, err := r.OrderReceiver("feedpartner"); err != nil {
		t.Fatalf("order receiver: %v", err)
	}

	_, err := r.StockProvider("feedpartner")
	if err == nil {
		t.Fatal("expected an error for an unimplemented role")
	}
	if !strings.Contains(err.Error(), "does not implement") {
		t.Errorf("error should distinguish an unimplemented role from an unknown vendor, got: %v", err)
	}
}

func TestRegisterRejectsRolelessVendor(t *testing.T) {
	r := NewRegistry()
	err := r.Register(roleless{name: "empty"})
	if err == nil {
		t.Fatal("expected a roleless adapter to be rejected at registration")
	}
	if r.Has("empty") {
		t.Error("a rejected adapter must not be registered")
	}
}

func TestRegisterRejectsDuplicateAndUnnamed(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(orderOnly{name: "vinculum"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := r.Register(orderOnly{name: "vinculum"}); err == nil {
		t.Error("expected a duplicate platform name to be rejected")
	}
	if err := r.Register(orderOnly{name: "  "}); err == nil {
		t.Error("expected an empty platform name to be rejected")
	}
}

func TestUnknownVendorErrorListsRegistered(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(orderOnly{name: "vinculum"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	_, err := r.OrderReceiver("uniware")
	if err == nil {
		t.Fatal("expected an error for an unknown vendor")
	}
	if !strings.Contains(err.Error(), "vinculum") {
		t.Errorf("error should list what is registered, got: %v", err)
	}
}

func TestRouteValidate(t *testing.T) {
	tests := []struct {
		name  string
		route Route
		ok    bool
	}{
		{"complete", Route{VendorPlatform: "vinculum", VendorReference: "BLR"}, true},
		{"no platform", Route{VendorReference: "BLR"}, false},
		{"no reference", Route{VendorPlatform: "vinculum"}, false},
		{"blank reference", Route{VendorPlatform: "vinculum", VendorReference: "   "}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.route.Validate()
			if tt.ok && err != nil {
				t.Errorf("expected valid, got %v", err)
			}
			if !tt.ok && err == nil {
				t.Error("expected invalid")
			}
		})
	}
}
