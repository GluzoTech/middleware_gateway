package vinculum_test

import (
	"context"
	"testing"
	"time"

	"github.com/gluzo/integration-gateway/app/integrations/vinculum"
	"github.com/gluzo/integration-gateway/app/vendor"
)

func vendorRoute() vendor.Route {
	return vendor.Route{
		IntegrationID:   "int-1",
		IntegrationName: "easyecom-vinculum",
		OriginPlatform:  "easyecom",
		VendorPlatform:  vinculum.PlatformName,
		VendorReference: "DEL",
		OriginReference: "bcpl-location",
	}
}

// Publishing every bucket would put damaged, quarantined and in-transit
// stock on the storefront as sellable, and the distinction is invisible in
// the data — every bucket is just a quantity. Refusing to build is the only
// place it can be caught before the damage is done.
func TestNewVendorRequiresASellableBucket(t *testing.T) {
	f := newFakeVinculum(t)

	c, err := vinculum.NewClient(vinculum.Config{
		BaseURL: f.srv.URL, APIOwner: testOwner, APIKey: testKey,
		Location: "DEL", Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	// The client is happy without a bucket: reading every bucket is fine.
	if _, err := vinculum.NewVendor(c, nil); err == nil {
		t.Fatal("a vendor that would publish every bucket must not be built")
	}

	withBucket, err := vinculum.NewClient(vinculum.Config{
		BaseURL: f.srv.URL, APIOwner: testOwner, APIKey: testKey,
		Location: "DEL", SellableBucket: "Good", Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := vinculum.NewVendor(withBucket, nil); err != nil {
		t.Fatalf("NewVendor with a bucket: %v", err)
	}
}

func TestFetchStockReturnsLevelsInGluzoTerms(t *testing.T) {
	ctx := context.Background()
	f := newFakeVinculum(t)
	f.stockPages = [][]map[string]any{{
		{"skuCode": "BCPL-CHY-500", "location": "DEL", "qty": 120, "committedQty": 20, "bucket": "Good"},
		{"skuCode": "BCPL-DMG-001", "location": "DEL", "qty": 9, "committedQty": 0, "bucket": "Damaged"},
	}}

	v := f.vendor(t)
	page, err := v.FetchStock(ctx, vendorRoute(), vendor.StockCursor{})
	if err != nil {
		t.Fatalf("FetchStock: %v", err)
	}

	// The role contract says the page arrives in Gluzo's terms: the bucket
	// filtered and the committed-quantity rule applied. A workflow that had
	// to understand buckets would need rewriting for the next vendor.
	if len(page.Levels) != 1 {
		t.Fatalf("got %d levels, want only the sellable bucket: %+v", len(page.Levels), page.Levels)
	}
	if page.Levels[0].SKU != "BCPL-CHY-500" || page.Levels[0].Available != 100 {
		t.Errorf("level = %+v, want BCPL-CHY-500 at 120 less the 20 committed", page.Levels[0])
	}
	if page.HasMore {
		t.Error("a single page reported more")
	}

	// The request must carry the configured bucket, or the filter would be
	// done only on our side and the vendor would send the whole catalogue.
	body, _ := f.lastStockBody.Load().(map[string]any)
	if got := body["buckets"]; got != "Good" {
		t.Errorf("buckets = %v, want the configured sellable bucket", got)
	}
	if got := body["locCode"]; got != "DEL" {
		t.Errorf("locCode = %v, want the route's vendor reference", got)
	}
}

// vendor.StockCursor pages from zero and Vinculum's pageNumber from one.
// Getting this wrong reads page one twice and never reads the last page.
func TestFetchStockConvertsPageNumbering(t *testing.T) {
	ctx := context.Background()
	f := newFakeVinculum(t)
	f.stockPages = [][]map[string]any{
		{{"skuCode": "BCPL-A", "location": "DEL", "qty": 1, "committedQty": 0, "bucket": "Good"}},
		{{"skuCode": "BCPL-B", "location": "DEL", "qty": 2, "committedQty": 0, "bucket": "Good"}},
	}
	v := f.vendor(t)

	first, err := v.FetchStock(ctx, vendorRoute(), vendor.StockCursor{})
	if err != nil {
		t.Fatalf("FetchStock: %v", err)
	}
	body, _ := f.lastStockBody.Load().(map[string]any)
	if got, ok := body["pageNumber"].(float64); !ok || int(got) != 1 {
		t.Errorf("first request asked for page %v, want 1", body["pageNumber"])
	}
	if !first.HasMore {
		t.Fatal("first page should report more")
	}
	if first.Levels[0].SKU != "BCPL-A" {
		t.Errorf("first page = %+v", first.Levels)
	}

	second, err := v.FetchStock(ctx, vendorRoute(), first.Next)
	if err != nil {
		t.Fatalf("FetchStock page 2: %v", err)
	}
	body, _ = f.lastStockBody.Load().(map[string]any)
	if got, ok := body["pageNumber"].(float64); !ok || int(got) != 2 {
		t.Errorf("second request asked for page %v, want 2", body["pageNumber"])
	}
	if len(second.Levels) != 1 || second.Levels[0].SKU != "BCPL-B" {
		t.Errorf("second page = %+v", second.Levels)
	}
	if second.HasMore {
		t.Error("the last page reported more")
	}
}

// An empty page ends the sweep whatever hasMore claims: following a hasMore
// that never goes false would loop until the action's timeout, every attempt.
func TestFetchStockStopsOnAnEmptyPageDespiteHasMore(t *testing.T) {
	ctx := context.Background()
	f := newFakeVinculum(t)
	f.alwaysMore = true
	f.stockPages = [][]map[string]any{{}}
	v := f.vendor(t)

	page, err := v.FetchStock(ctx, vendorRoute(), vendor.StockCursor{})
	if err != nil {
		t.Fatalf("FetchStock: %v", err)
	}
	if page.HasMore {
		t.Error("an empty page reported more")
	}
}

func TestFetchStockRejectsAnIncompleteRoute(t *testing.T) {
	ctx := context.Background()
	f := newFakeVinculum(t)
	v := f.vendor(t)

	// No vendor reference: there is no location to ask about.
	if _, err := v.FetchStock(ctx, vendor.Route{VendorPlatform: vinculum.PlatformName}, vendor.StockCursor{}); err == nil {
		t.Fatal("an incomplete route must not be swept")
	}
	if f.stockCalls.Load() != 0 {
		t.Error("an incomplete route reached the vendor")
	}
}

func TestVendorRegistersAsAStockProviderOnly(t *testing.T) {
	f := newFakeVinculum(t)
	v := f.vendor(t)

	reg := vendor.NewRegistry()
	if err := reg.Register(v); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := reg.StockProvider(vinculum.PlatformName); err != nil {
		t.Errorf("StockProvider: %v", err)
	}
	// Asking for a role this vendor does not implement must be a different
	// error from asking for an unknown vendor: they are different operator
	// mistakes. OrderReceiver arrives in Phase 5.
	if _, err := reg.OrderReceiver(vinculum.PlatformName); err == nil {
		t.Error("this vendor cannot yet receive orders")
	} else if _, unknown := reg.OrderReceiver("no-such-vendor"); unknown == nil {
		t.Error("an unknown vendor must also error")
	} else if err.Error() == unknown.Error() {
		t.Errorf("a missing role and an unknown vendor report the same error: %v", err)
	}
}
