package tests

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gluzo/integration-gateway/app/couriermap"
	"github.com/gluzo/integration-gateway/app/domain/tracking"
	"github.com/gluzo/integration-gateway/app/shipmentstate"
)

func TestCourierMapStore(t *testing.T) {
	ctx := context.Background()
	store := couriermap.NewStore(pool)
	integ := newIntegration(t, uuid.NewString()[:8])
	other := newIntegration(t, uuid.NewString()[:8])

	c, err := store.Add(ctx, integ.Name, "Blue Dart", "42", "BlueDart Express")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if c.CompanyCarrierID != "42" || c.Status != couriermap.StatusActive {
		t.Errorf("added mapping = %+v", c)
	}
	if _, err := store.Add(ctx, integ.Name, "Delhivery", "77", ""); err != nil {
		t.Fatalf("Add second: %v", err)
	}

	t.Run("a carrier maps once per integration", func(t *testing.T) {
		if _, err := store.Add(ctx, integ.Name, "Blue Dart", "99", ""); !errors.Is(err, couriermap.ErrAlreadyExists) {
			t.Errorf("duplicate carrier: got %v, want ErrAlreadyExists", err)
		}
		// The same carrier may map differently for another integration:
		// two vendors' accounts carry different carrier ids.
		if _, err := store.Add(ctx, other.Name, "Blue Dart", "500", ""); err != nil {
			t.Errorf("Add to another integration: %v", err)
		}
	})

	t.Run("an index built from the store translates", func(t *testing.T) {
		active, err := store.Active(ctx, integ.ID)
		if err != nil {
			t.Fatalf("Active: %v", err)
		}
		idx := couriermap.NewIndex(active)
		if idx.Len() != 2 {
			t.Fatalf("index holds %d, want 2", idx.Len())
		}

		// Carriers arrive spelled inconsistently; a mapping that failed on
		// case or spacing would report a configuration error that is not one.
		for _, probe := range []string{"Blue Dart", "bluedart", "  BLUEDART  "} {
			got, err := idx.Lookup(probe)
			if err != nil {
				t.Errorf("Lookup(%q): %v", probe, err)
				continue
			}
			if got.CompanyCarrierID != "42" {
				t.Errorf("Lookup(%q) = %q", probe, got.CompanyCarrierID)
			}
			if got.PresentedName() != "BlueDart Express" {
				t.Errorf("presented name = %q", got.PresentedName())
			}
		}
		// With no override the vendor's own spelling is sent onward.
		if got, err := idx.Lookup("Delhivery"); err != nil || got.PresentedName() != "Delhivery" {
			t.Errorf("Delhivery = %+v, %v", got, err)
		}
		if _, err := idx.Lookup("Xpressbees"); !errors.Is(err, couriermap.ErrUnmapped) {
			t.Errorf("unmapped carrier: got %v, want ErrUnmapped", err)
		}
	})

	t.Run("disabling removes it from active but not from the listing", func(t *testing.T) {
		if err := store.SetStatus(ctx, c.ID, couriermap.StatusDisabled); err != nil {
			t.Fatalf("SetStatus: %v", err)
		}
		active, err := store.Active(ctx, integ.ID)
		if err != nil {
			t.Fatalf("Active: %v", err)
		}
		if len(active) != 1 {
			t.Errorf("active = %d, want 1", len(active))
		}
		all, err := store.List(ctx, integ.Name)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(all) != 2 {
			t.Errorf("listing = %d, want 2 so an operator can see what they turned off", len(all))
		}
	})

	t.Run("invalid input and unknown rows are reported", func(t *testing.T) {
		if _, err := store.Add(ctx, integ.Name, "", "1", ""); !errors.Is(err, couriermap.ErrInvalidArgument) {
			t.Errorf("empty transporter: got %v", err)
		}
		if _, err := store.Add(ctx, integ.Name, "X", "", ""); !errors.Is(err, couriermap.ErrInvalidArgument) {
			t.Errorf("empty carrier id: got %v", err)
		}
		if _, err := store.Add(ctx, "missing-integration", "X", "1", ""); !errors.Is(err, couriermap.ErrNotFound) {
			t.Errorf("unknown integration: got %v", err)
		}
		if err := store.Remove(ctx, uuid.New()); !errors.Is(err, couriermap.ErrNotFound) {
			t.Errorf("remove unknown: got %v", err)
		}
	})

	t.Run("remove deletes the row", func(t *testing.T) {
		if err := store.Remove(ctx, c.ID); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		all, err := store.List(ctx, integ.Name)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(all) != 1 {
			t.Errorf("listing = %d after removal, want 1", len(all))
		}
	})
}

func TestShipmentStateStore(t *testing.T) {
	ctx := context.Background()
	store := shipmentstate.NewStore(pool)
	integ := newIntegration(t, uuid.NewString()[:8])
	at := time.Date(2026, 9, 30, 13, 0, 0, 0, time.UTC)

	key := shipmentstate.Key{IntegrationID: integ.ID, OrderExternalID: "9876543"}

	t.Run("nothing pushed means every notice is an advance", func(t *testing.T) {
		rec, found, err := store.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if found {
			t.Fatalf("an empty store reported %+v", rec)
		}
		d := shipmentstate.Decide(rec, found, tracking.Shipment{Status: tracking.StatusShipped})
		if !d.Push || !d.AdvanceStatus {
			t.Errorf("decision = %+v, want a full push", d)
		}
	})

	t.Run("a recorded state comes back", func(t *testing.T) {
		err := store.Record(ctx, key, shipmentstate.Record{
			Status: tracking.StatusDelivered, Progress: tracking.StatusDelivered.Progress(),
			TrackingNumber: "AWB1", Carrier: "Delhivery", PushedAt: at,
		})
		if err != nil {
			t.Fatalf("Record: %v", err)
		}
		rec, found, err := store.Get(ctx, key)
		if err != nil || !found {
			t.Fatalf("Get: %v (found=%v)", err, found)
		}
		if rec.Status != tracking.StatusDelivered || rec.TrackingNumber != "AWB1" {
			t.Errorf("record = %+v", rec)
		}
	})

	t.Run("a later notice does not regress a delivered order", func(t *testing.T) {
		rec, found, err := store.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		// The "shipped" notice arriving after "delivered" is ordinary: the
		// gateway polls, so records arrive out of order.
		late := tracking.Shipment{Status: tracking.StatusShipped, TrackingNumber: "AWB1", Carrier: "Delhivery"}
		if d := shipmentstate.Decide(rec, found, late); d.Push {
			t.Errorf("decision = %+v, want nothing pushed", d)
		}

		// A corrected waybill is worth sending; the status still is not.
		corrected := tracking.Shipment{Status: tracking.StatusShipped, TrackingNumber: "AWB2", Carrier: "Delhivery"}
		d := shipmentstate.Decide(rec, found, corrected)
		if !d.Push || d.AdvanceStatus {
			t.Errorf("decision = %+v, want a details-only push", d)
		}

		next := shipmentstate.FromShipment(rec, corrected, d.AdvanceStatus, at)
		if next.Status != tracking.StatusDelivered {
			t.Errorf("status = %q, want it left at delivered", next.Status)
		}
		if next.TrackingNumber != "AWB2" {
			t.Errorf("tracking number = %q, want the correction", next.TrackingNumber)
		}
	})

	t.Run("packages of one order are independent", func(t *testing.T) {
		second := key
		second.PackageCode = "PKG-2"
		_, found, err := store.Get(ctx, second)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if found {
			t.Error("a second package inherited the first's state")
		}
	})

	t.Run("integrations are independent", func(t *testing.T) {
		other := newIntegration(t, uuid.NewString()[:8])
		_, found, err := store.Get(ctx, shipmentstate.Key{IntegrationID: other.ID, OrderExternalID: "9876543"})
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if found {
			t.Error("another integration saw this order's dispatch state")
		}
	})
}
