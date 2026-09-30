package tests

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/gluzo/integration-gateway/app/auth"
	"github.com/gluzo/integration-gateway/app/inventorystate"
	"github.com/gluzo/integration-gateway/app/routing"
)

func newIntegration(t *testing.T, suffix string) auth.Integration {
	t.Helper()
	ctx := context.Background()
	creds := auth.NewStore(pool)

	src, _, err := creds.CreatePlatform(ctx, "easyecom-"+suffix, auth.PlatformTypeSource)
	if err != nil {
		t.Fatalf("CreatePlatform source: %v", err)
	}
	dst, _, err := creds.CreatePlatform(ctx, "vinculum-"+suffix, auth.PlatformTypeDestination)
	if err != nil {
		t.Fatalf("CreatePlatform destination: %v", err)
	}
	integ, err := creds.CreateIntegration(ctx, "easyecom-vinculum-"+suffix, src.Name, dst.Name)
	if err != nil {
		t.Fatalf("CreateIntegration: %v", err)
	}
	return integ
}

func TestInventoryStateStore(t *testing.T) {
	ctx := context.Background()
	store := inventorystate.NewStore(pool)
	integ := newIntegration(t, uuid.NewString()[:8])

	key := inventorystate.Key{IntegrationID: integ.ID, OriginReference: "bcpl-location"}

	t.Run("an empty store reports nothing pushed", func(t *testing.T) {
		last, err := store.Load(ctx, key)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(last) != 0 {
			t.Errorf("got %+v", last)
		}
		// Every SKU is therefore changed: the gateway has never pushed
		// them, so it cannot know the platform agrees.
		changed := inventorystate.Changed(last, []inventorystate.Entry{{GluzoSKU: "GLZ-A", Quantity: 5}})
		if len(changed) != 1 {
			t.Errorf("changed = %+v, want the unseen SKU", changed)
		}
	})

	t.Run("recorded quantities come back", func(t *testing.T) {
		err := store.Record(ctx, key, []inventorystate.Entry{
			{GluzoSKU: "GLZ-A", Quantity: 100},
			{GluzoSKU: "GLZ-B", Quantity: 0},
		})
		if err != nil {
			t.Fatalf("Record: %v", err)
		}
		last, err := store.Load(ctx, key)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if last["glz-a"] != 100 {
			t.Errorf("GLZ-A = %d, want 100", last["glz-a"])
		}
		// Zero is a real recorded quantity, not an absent one. Treating it
		// as absent would re-push every out-of-stock SKU every sweep.
		q, present := last["glz-b"]
		if !present || q != 0 {
			t.Errorf("GLZ-B = %d (present=%v), want a recorded zero", q, present)
		}
	})

	t.Run("only changed quantities are reported", func(t *testing.T) {
		last, err := store.Load(ctx, key)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		changed := inventorystate.Changed(last, []inventorystate.Entry{
			{GluzoSKU: "GLZ-A", Quantity: 100}, // unchanged
			{GluzoSKU: "GLZ-B", Quantity: 7},   // moved off zero
			{GluzoSKU: "GLZ-C", Quantity: 1},   // never seen
		})
		if len(changed) != 2 {
			t.Fatalf("changed = %+v, want GLZ-B and GLZ-C", changed)
		}
	})

	t.Run("re-recording replaces rather than duplicates", func(t *testing.T) {
		if err := store.Record(ctx, key, []inventorystate.Entry{{GluzoSKU: "GLZ-A", Quantity: 55}}); err != nil {
			t.Fatalf("Record: %v", err)
		}
		last, err := store.Load(ctx, key)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if last["glz-a"] != 55 {
			t.Errorf("GLZ-A = %d, want the newer 55", last["glz-a"])
		}
	})

	t.Run("origin locations are independent", func(t *testing.T) {
		// The same SKU pushed to a second origin location has its own
		// record. Collapsing them would make a write to one location look
		// like it had already happened to the other.
		other := inventorystate.Key{IntegrationID: integ.ID, OriginReference: "other-location"}
		last, err := store.Load(ctx, other)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(last) != 0 {
			t.Errorf("the second location inherited %+v", last)
		}
		if err := store.Record(ctx, other, []inventorystate.Entry{{GluzoSKU: "GLZ-A", Quantity: 9}}); err != nil {
			t.Fatalf("Record: %v", err)
		}
		first, err := store.Load(ctx, key)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if first["glz-a"] != 55 {
			t.Errorf("writing the second location changed the first to %d", first["glz-a"])
		}
	})

	t.Run("integrations are independent", func(t *testing.T) {
		otherInteg := newIntegration(t, uuid.NewString()[:8])
		last, err := store.Load(ctx, inventorystate.Key{IntegrationID: otherInteg.ID, OriginReference: "bcpl-location"})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(last) != 0 {
			t.Errorf("another integration saw %+v", last)
		}
	})

	t.Run("forget makes the next sweep push everything", func(t *testing.T) {
		if err := store.Forget(ctx, key); err != nil {
			t.Fatalf("Forget: %v", err)
		}
		last, err := store.Load(ctx, key)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(last) != 0 {
			t.Errorf("after Forget the store still holds %+v", last)
		}
	})

	t.Run("recording nothing is not an error", func(t *testing.T) {
		if err := store.Record(ctx, key, nil); err != nil {
			t.Errorf("Record with no entries: %v", err)
		}
	})
}

// A sweep works per distinct vendor location. Several warehouse routes
// commonly share one, and sweeping per route would read the same stock and
// push it once per route.
func TestRoutingActiveLocationsAreDistinct(t *testing.T) {
	ctx := context.Background()
	routes := routing.NewStore(pool)
	integ := newIntegration(t, uuid.NewString()[:8])

	// Two warehouses fulfilled from the same vendor location.
	for _, warehouse := range []string{"111", "222"} {
		if _, err := routes.AddRoute(ctx, integ.Name, routing.TypeWarehouse, warehouse, "DEL", "bcpl-key"); err != nil {
			t.Fatalf("AddRoute %s: %v", warehouse, err)
		}
	}
	// A third warehouse at a different vendor location.
	if _, err := routes.AddRoute(ctx, integ.Name, routing.TypeWarehouse, "333", "BLR", "bcpl-key"); err != nil {
		t.Fatalf("AddRoute: %v", err)
	}
	// A route with no vendor reference cannot be swept: "the stock at ''"
	// is not a question.
	if _, err := routes.AddRoute(ctx, integ.Name, routing.TypeWarehouse, "444", "", ""); err != nil {
		t.Fatalf("AddRoute: %v", err)
	}

	locations, err := routes.ActiveLocations(ctx)
	if err != nil {
		t.Fatalf("ActiveLocations: %v", err)
	}

	mine := map[string]routing.Location{}
	for _, loc := range locations {
		if loc.IntegrationID == integ.ID {
			mine[loc.VendorReference] = loc
		}
	}
	if len(mine) != 2 {
		t.Fatalf("got %d locations for this integration, want DEL and BLR once each: %+v", len(mine), mine)
	}
	if _, ok := mine[""]; ok {
		t.Error("a route with no vendor reference was listed as sweepable")
	}
	if got := mine["DEL"]; got.OriginReference != "bcpl-key" || got.VendorPlatform == "" || got.OriginPlatform == "" {
		t.Errorf("DEL = %+v; a sweep needs both platforms and the origin reference", got)
	}
}
