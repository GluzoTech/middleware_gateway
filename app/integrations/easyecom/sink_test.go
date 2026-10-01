package easyecom_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gluzo/integration-gateway/app/domain/inventory"
	"github.com/gluzo/integration-gateway/app/integrations/easyecom"
	dtoinventory "github.com/gluzo/integration-gateway/app/integrations/easyecom/dto/inventory"
	"github.com/gluzo/integration-gateway/app/vendor"
)

// fakeLocationScopedEasyEcom models the property the whole design rests on:
// EasyEcom issues a JWT per location, and a write is applied to the location
// the token was issued for — not the one the caller believed it was using.
//
// Each SKU belongs to exactly one location, as it would in a real account
// with a separate location for BCPL. A write carrying a token for location A
// may only touch location A's SKUs.
type fakeLocationScopedEasyEcom struct {
	t   *testing.T
	srv *httptest.Server

	mu sync.Mutex
	// tokens maps an issued JWT to the location it is scoped to.
	tokens map[string]string
	// skuLocation says which location owns each SKU.
	skuLocation map[string]string
	// stock is the stored quantity per location and SKU.
	stock map[string]map[string]int
	// writes records every (location, sku, quantity) applied.
	writes []appliedWrite
	// logins counts logins per location key.
	logins map[string]int
}

type appliedWrite struct {
	location string
	sku      string
	quantity int
}

func newFakeLocationScopedEasyEcom(t *testing.T, skuLocation map[string]string) *fakeLocationScopedEasyEcom {
	f := &fakeLocationScopedEasyEcom{
		t:           t,
		tokens:      map[string]string{},
		skuLocation: skuLocation,
		stock:       map[string]map[string]int{},
		logins:      map[string]int{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/access/token", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		location := body["location_key"]
		if location == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.logins[location]++
		token := "jwt-for-" + location
		f.tokens[token] = location
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"token":{"jwt_token":"` + token + `"}}}`))
	})
	mux.HandleFunc("/bulkInventoryUpdate", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if len(auth) <= len(prefix) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		location, ok := f.tokens[auth[len(prefix):]]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		var req dtoinventory.BulkInventoryUpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		results := make([]map[string]any, 0, len(req.Items))
		f.mu.Lock()
		for _, item := range req.Items {
			owner := f.skuLocation[item.SKU]
			if owner != location {
				// The token's scope, not the request body, decides. This is
				// the platform refusing a cross-location write.
				results = append(results, map[string]any{
					"sku": item.SKU, "status": "failed",
					"message": "sku does not belong to this location",
				})
				continue
			}
			if f.stock[location] == nil {
				f.stock[location] = map[string]int{}
			}
			f.stock[location][item.SKU] = item.Quantity
			f.writes = append(f.writes, appliedWrite{location: location, sku: item.SKU, quantity: item.Quantity})
			results = append(results, map[string]any{"sku": item.SKU, "status": "success"})
		}
		f.mu.Unlock()

		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "message": "ok", "data": results})
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLocationScopedEasyEcom) quantity(location, sku string) (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q, ok := f.stock[location][sku]
	return q, ok
}

func (f *fakeLocationScopedEasyEcom) appliedWrites() []appliedWrite {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]appliedWrite, len(f.writes))
	copy(out, f.writes)
	return out
}

func (f *fakeLocationScopedEasyEcom) loginCount(location string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logins[location]
}

func (f *fakeLocationScopedEasyEcom) sink(t *testing.T, defaultLocation string) *easyecom.Sink {
	t.Helper()
	c, err := easyecom.NewClient(easyecom.Config{
		BaseURL:     f.srv.URL,
		APIKey:      "key-123",
		Email:       "ops@gluzo.com",
		Password:    "pw",
		LocationKey: defaultLocation,
		Timeout:     2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return easyecom.NewSink(c, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func route(originRef string) vendor.Route {
	return vendor.Route{
		IntegrationID:   "int-1",
		IntegrationName: "easyecom-vinculum",
		OriginPlatform:  "easyecom",
		VendorPlatform:  "vinculum",
		VendorReference: "DEL",
		OriginReference: originRef,
	}
}

// The Phase 3 exit criterion. A push authenticated for BCPL's location must
// not be able to write Gluzo's own quantities, and the guarantee must come
// from the platform rather than from the gateway being careful.
func TestAPushForOneLocationCannotWriteAnother(t *testing.T) {
	ctx := context.Background()
	f := newFakeLocationScopedEasyEcom(t, map[string]string{
		"BCPL-CHY-500":  "bcpl-location",
		"GLUZO-TEA-100": "gluzo-location",
	})
	sink := f.sink(t, "gluzo-location") // the process default is Gluzo's own

	// A level for a SKU that belongs to Gluzo's location, pushed on a route
	// whose origin reference is BCPL's. A defect upstream could produce
	// exactly this.
	res, err := sink.PushStock(ctx, route("bcpl-location"), []inventory.Level{
		{SKU: "BCPL-CHY-500", Available: 100},
		{SKU: "GLUZO-TEA-100", Available: 7},
	})
	if err != nil {
		t.Fatalf("PushStock: %v", err)
	}

	if res.Updated != 1 || res.Failed != 1 {
		t.Errorf("result = %+v, want one updated and one rejected", res)
	}
	if q, ok := f.quantity("bcpl-location", "BCPL-CHY-500"); !ok || q != 100 {
		t.Errorf("BCPL's own SKU should have been written: got %d (present=%v)", q, ok)
	}
	if _, ok := f.quantity("gluzo-location", "GLUZO-TEA-100"); ok {
		t.Error("a push on BCPL's route wrote a quantity into Gluzo's location")
	}
	for _, w := range f.appliedWrites() {
		if w.location != "bcpl-location" {
			t.Errorf("a write landed in %q; every write on this route must be scoped to bcpl-location", w.location)
		}
	}
}

// The mechanism behind the guarantee: two routes must produce two different
// tokens. A single cached token for the process would hand BCPL's credential
// to a write meant for Gluzo's warehouse.
func TestEachLocationGetsItsOwnToken(t *testing.T) {
	ctx := context.Background()
	f := newFakeLocationScopedEasyEcom(t, map[string]string{
		"BCPL-CHY-500":  "bcpl-location",
		"GLUZO-TEA-100": "gluzo-location",
	})
	sink := f.sink(t, "gluzo-location")

	if _, err := sink.PushStock(ctx, route("bcpl-location"), []inventory.Level{{SKU: "BCPL-CHY-500", Available: 10}}); err != nil {
		t.Fatalf("PushStock bcpl: %v", err)
	}
	if _, err := sink.PushStock(ctx, route("gluzo-location"), []inventory.Level{{SKU: "GLUZO-TEA-100", Available: 20}}); err != nil {
		t.Fatalf("PushStock gluzo: %v", err)
	}

	if got := f.loginCount("bcpl-location"); got != 1 {
		t.Errorf("logged in %d times for bcpl-location, want 1", got)
	}
	if got := f.loginCount("gluzo-location"); got != 1 {
		t.Errorf("logged in %d times for gluzo-location, want 1", got)
	}

	// Both writes landed in their own location.
	for _, c := range []struct {
		location, sku string
		want          int
	}{
		{"bcpl-location", "BCPL-CHY-500", 10},
		{"gluzo-location", "GLUZO-TEA-100", 20},
	} {
		if q, ok := f.quantity(c.location, c.sku); !ok || q != c.want {
			t.Errorf("%s/%s = %d (present=%v), want %d", c.location, c.sku, q, ok, c.want)
		}
	}

	// A repeat for a location already logged in must reuse its token rather
	// than log in again.
	if _, err := sink.PushStock(ctx, route("bcpl-location"), []inventory.Level{{SKU: "BCPL-CHY-500", Available: 11}}); err != nil {
		t.Fatalf("PushStock bcpl again: %v", err)
	}
	if got := f.loginCount("bcpl-location"); got != 1 {
		t.Errorf("logged in %d times for bcpl-location after a second push, want the cached token reused", got)
	}
}

func TestPushStockClampsAtTheEasyEcomMaximum(t *testing.T) {
	ctx := context.Background()
	f := newFakeLocationScopedEasyEcom(t, map[string]string{"BCPL-BULK": "bcpl-location"})
	sink := f.sink(t, "bcpl-location")

	if _, err := sink.PushStock(ctx, route("bcpl-location"), []inventory.Level{
		{SKU: "BCPL-BULK", Available: 25_000},
	}); err != nil {
		t.Fatalf("PushStock: %v", err)
	}

	// EasyEcom clamps silently; clamping here means the number sent is the
	// number stored, so the response can be read at face value.
	if q, _ := f.quantity("bcpl-location", "BCPL-BULK"); q != dtoinventory.MaxQuantity {
		t.Errorf("stored %d, want the clamped maximum %d", q, dtoinventory.MaxQuantity)
	}
}

func TestPushStockSplitsLargeSetsIntoBatches(t *testing.T) {
	ctx := context.Background()
	owners := map[string]string{}
	levels := make([]inventory.Level, 0, dtoinventory.MaxBulkItems+50)
	for i := 0; i < dtoinventory.MaxBulkItems+50; i++ {
		sku := "BCPL-" + string(rune('A'+i%26)) + "-" + itoa(i)
		owners[sku] = "bcpl-location"
		levels = append(levels, inventory.Level{SKU: sku, Available: i})
	}
	f := newFakeLocationScopedEasyEcom(t, owners)
	sink := f.sink(t, "bcpl-location")

	res, err := sink.PushStock(ctx, route("bcpl-location"), levels)
	if err != nil {
		t.Fatalf("PushStock: %v", err)
	}
	if res.Updated != len(levels) {
		t.Errorf("updated %d, want %d", res.Updated, len(levels))
	}
	if got := len(f.appliedWrites()); got != len(levels) {
		t.Errorf("%d writes applied, want %d: a batch was dropped", got, len(levels))
	}
}

func TestPushStockCountsUnsendableLevelsAsFailures(t *testing.T) {
	ctx := context.Background()
	f := newFakeLocationScopedEasyEcom(t, map[string]string{"BCPL-OK": "bcpl-location"})
	sink := f.sink(t, "bcpl-location")

	// A level with no SKU cannot be sent. Counting it rather than dropping
	// it matters: a SKU whose stock never reaches the storefront looks
	// exactly like one whose stock did not change.
	res, err := sink.PushStock(ctx, route("bcpl-location"), []inventory.Level{
		{SKU: "BCPL-OK", Available: 5},
		{SKU: "   ", Available: 9},
	})
	if err != nil {
		t.Fatalf("PushStock: %v", err)
	}
	if res.Updated != 1 || res.Failed != 1 {
		t.Errorf("result = %+v, want one updated and one failed", res)
	}
}

func TestPushStockWithNothingToPushIsANoOp(t *testing.T) {
	ctx := context.Background()
	f := newFakeLocationScopedEasyEcom(t, map[string]string{})
	sink := f.sink(t, "bcpl-location")

	res, err := sink.PushStock(ctx, route("bcpl-location"), nil)
	if err != nil {
		t.Fatalf("PushStock: %v", err)
	}
	if res.Updated != 0 || res.Failed != 0 {
		t.Errorf("result = %+v, want a no-op", res)
	}
	if len(f.appliedWrites()) != 0 {
		t.Error("an empty push reached the platform")
	}
}

func TestPushStockRejectsAnIncompleteRoute(t *testing.T) {
	ctx := context.Background()
	f := newFakeLocationScopedEasyEcom(t, map[string]string{"BCPL-OK": "bcpl-location"})
	sink := f.sink(t, "bcpl-location")

	// No vendor reference: the route does not identify a pipeline.
	_, err := sink.PushStock(ctx, vendor.Route{VendorPlatform: "vinculum"}, []inventory.Level{{SKU: "BCPL-OK", Available: 1}})
	if err == nil {
		t.Fatal("an incomplete route must not push")
	}
	if len(f.appliedWrites()) != 0 {
		t.Error("an incomplete route reached the platform")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
