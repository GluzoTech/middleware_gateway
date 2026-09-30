package skumap_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/gluzo/integration-gateway/app/skumap"
)

func TestPublishableAppliesTheSafetyBuffer(t *testing.T) {
	tests := []struct {
		name     string
		buffer   int
		quantity int
		want     int
	}{
		{"no buffer", 0, 120, 120},
		{"buffer withheld", 5, 120, 115},
		{"buffer exactly consumes the stock", 5, 5, 0},
		{"buffer exceeds the stock", 10, 3, 0},
		// The rule that matters: an item the vendor has none of is out of
		// stock now. Subtracting a buffer from nothing to reach nothing
		// would be the same answer reached more slowly, and any other
		// reading would delay a zero.
		{"zero propagates immediately", 50, 0, 0},
		{"a negative reading is clamped", 5, -3, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := skumap.Mapping{SafetyBuffer: tc.buffer}
			if got := m.Publishable(tc.quantity); got != tc.want {
				t.Errorf("Publishable(%d) with buffer %d = %d, want %d", tc.quantity, tc.buffer, got, tc.want)
			}
		})
	}
}

func TestIndexTranslatesBothDirections(t *testing.T) {
	idx := skumap.NewIndex([]skumap.Mapping{
		{GluzoSKU: "GLZ-CHY-500", VendorSKU: "BCPL-CHY-500", SafetyBuffer: 5, Status: skumap.StatusActive},
		{GluzoSKU: "GLZ-HNY-250", VendorSKU: "BCPL-HNY-250", Status: skumap.StatusActive},
	})

	if idx.Len() != 2 {
		t.Fatalf("index holds %d mappings, want 2", idx.Len())
	}

	m, err := idx.ToGluzo("BCPL-CHY-500")
	if err != nil {
		t.Fatalf("ToGluzo: %v", err)
	}
	if m.GluzoSKU != "GLZ-CHY-500" || m.SafetyBuffer != 5 {
		t.Errorf("got %+v", m)
	}

	m, err = idx.ToVendor("GLZ-HNY-250")
	if err != nil {
		t.Fatalf("ToVendor: %v", err)
	}
	if m.VendorSKU != "BCPL-HNY-250" {
		t.Errorf("got %+v", m)
	}
}

// Vendors are inconsistent about the case of item codes. A mapping that
// failed because a code arrived lower-cased would report a configuration
// error that is not one.
func TestIndexLookupIgnoresCaseAndSurroundingSpace(t *testing.T) {
	idx := skumap.NewIndex([]skumap.Mapping{
		{GluzoSKU: "GLZ-CHY-500", VendorSKU: "BCPL-CHY-500", Status: skumap.StatusActive},
	})
	for _, probe := range []string{"bcpl-chy-500", "BCPL-CHY-500", "  BCPL-Chy-500  "} {
		if _, err := idx.ToGluzo(probe); err != nil {
			t.Errorf("ToGluzo(%q): %v", probe, err)
		}
	}
}

// An unmapped SKU must be an error, never a silent skip. A stock sweep that
// drops one publishes nothing for it, and a storefront showing no update
// looks exactly like a storefront that is up to date.
func TestUnmappedSKUIsAnErrorNamingTheSKU(t *testing.T) {
	idx := skumap.NewIndex(nil)

	_, err := idx.ToGluzo("BCPL-UNKNOWN")
	if !errors.Is(err, skumap.ErrUnmapped) {
		t.Fatalf("got %v, want ErrUnmapped", err)
	}
	// The operator's next action is to add exactly this mapping, so the
	// message has to name it.
	if got := err.Error(); !contains(got, "BCPL-UNKNOWN") {
		t.Errorf("error %q does not name the sku", got)
	}

	if _, err := idx.ToVendor("GLZ-UNKNOWN"); !errors.Is(err, skumap.ErrUnmapped) {
		t.Errorf("ToVendor: got %v, want ErrUnmapped", err)
	}
}

// Disabling a mapping stops that SKU syncing. It must not make the SKU
// unmapped-and-therefore-an-error every sweep; it must simply not be in the
// index, which is the same outcome the operator asked for.
func TestDisabledMappingsAreExcluded(t *testing.T) {
	idx := skumap.NewIndex([]skumap.Mapping{
		{GluzoSKU: "GLZ-A", VendorSKU: "BCPL-A", Status: skumap.StatusActive},
		{GluzoSKU: "GLZ-B", VendorSKU: "BCPL-B", Status: skumap.StatusDisabled},
	})
	if idx.Len() != 1 {
		t.Errorf("index holds %d mappings, want 1", idx.Len())
	}
	if _, err := idx.ToGluzo("BCPL-B"); !errors.Is(err, skumap.ErrUnmapped) {
		t.Errorf("a disabled mapping must not translate: %v", err)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		m    skumap.Mapping
		ok   bool
	}{
		{"complete", skumap.Mapping{GluzoSKU: "A", VendorSKU: "B"}, true},
		{"no gluzo sku", skumap.Mapping{VendorSKU: "B"}, false},
		{"no vendor sku", skumap.Mapping{GluzoSKU: "A"}, false},
		{"negative buffer", skumap.Mapping{GluzoSKU: "A", VendorSKU: "B", SafetyBuffer: -1}, false},
		{"unknown status", skumap.Mapping{GluzoSKU: "A", VendorSKU: "B", Status: "paused"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.m.Validate()
			if tc.ok && err != nil {
				t.Errorf("expected valid, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestMemoryReaderExcludesDisabled(t *testing.T) {
	ctx := context.Background()
	id := uuid.New()
	r := skumap.NewMemoryReader()
	r.Set(id, []skumap.Mapping{
		{GluzoSKU: "GLZ-A", VendorSKU: "BCPL-A", Status: skumap.StatusActive},
		{GluzoSKU: "GLZ-B", VendorSKU: "BCPL-B", Status: skumap.StatusDisabled},
	})

	got, err := r.Active(ctx, id)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if len(got) != 1 || got[0].GluzoSKU != "GLZ-A" {
		t.Errorf("got %+v", got)
	}

	// A different integration must see nothing: mappings are scoped, so one
	// vendor's codes can never be used by another's pipeline.
	other, err := r.Active(ctx, uuid.New())
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("another integration saw %d mappings", len(other))
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
