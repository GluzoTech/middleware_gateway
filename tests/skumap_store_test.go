package tests

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/gluzo/integration-gateway/app/auth"
	"github.com/gluzo/integration-gateway/app/skumap"
)

func TestSKUMapStore(t *testing.T) {
	ctx := context.Background()
	creds := auth.NewStore(pool)
	mappings := skumap.NewStore(pool)
	suffix := uuid.NewString()[:8]

	src, _, err := creds.CreatePlatform(ctx, "easyecom-"+suffix, auth.PlatformTypeSource)
	if err != nil {
		t.Fatalf("CreatePlatform: %v", err)
	}
	dst, _, err := creds.CreatePlatform(ctx, "vinculum-"+suffix, auth.PlatformTypeDestination)
	if err != nil {
		t.Fatalf("CreatePlatform: %v", err)
	}
	integ, err := creds.CreateIntegration(ctx, "easyecom-vinculum-"+suffix, src.Name, dst.Name)
	if err != nil {
		t.Fatalf("CreateIntegration: %v", err)
	}
	otherInteg, err := creds.CreateIntegration(ctx, "other-"+suffix, src.Name, dst.Name)
	if err != nil {
		t.Fatalf("CreateIntegration other: %v", err)
	}

	m, err := mappings.Add(ctx, integ.Name, "GLZ-CHY-500", "BCPL-CHY-500", 5)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if m.SafetyBuffer != 5 || m.Status != skumap.StatusActive {
		t.Errorf("added mapping = %+v", m)
	}

	if _, err := mappings.Add(ctx, integ.Name, "GLZ-HNY-250", "BCPL-HNY-250", 0); err != nil {
		t.Fatalf("Add second: %v", err)
	}

	t.Run("both directions are unique within an integration", func(t *testing.T) {
		// Both are used: stock arrives under the vendor's code, orders go
		// out under it. A second row for either side would make one
		// direction ambiguous.
		if _, err := mappings.Add(ctx, integ.Name, "GLZ-CHY-500", "BCPL-OTHER", 0); !errors.Is(err, skumap.ErrAlreadyExists) {
			t.Errorf("duplicate gluzo sku: got %v, want ErrAlreadyExists", err)
		}
		if _, err := mappings.Add(ctx, integ.Name, "GLZ-OTHER", "BCPL-CHY-500", 0); !errors.Is(err, skumap.ErrAlreadyExists) {
			t.Errorf("duplicate vendor sku: got %v, want ErrAlreadyExists", err)
		}
	})

	t.Run("the same codes may be used by another integration", func(t *testing.T) {
		// Two vendors may both call an item BCPL-CHY-500. Scoping is what
		// keeps that from colliding.
		if _, err := mappings.Add(ctx, otherInteg.Name, "GLZ-CHY-500", "BCPL-CHY-500", 0); err != nil {
			t.Errorf("Add to another integration: %v", err)
		}
	})

	t.Run("active excludes other integrations", func(t *testing.T) {
		active, err := mappings.Active(ctx, integ.ID)
		if err != nil {
			t.Fatalf("Active: %v", err)
		}
		if len(active) != 2 {
			t.Fatalf("got %d active mappings, want 2: %+v", len(active), active)
		}
		for _, mapping := range active {
			if mapping.IntegrationID != integ.ID {
				t.Errorf("mapping %s belongs to integration %s", mapping.ID, mapping.IntegrationID)
			}
		}
	})

	t.Run("disabling removes a mapping from active without deleting it", func(t *testing.T) {
		if err := mappings.SetStatus(ctx, m.ID, skumap.StatusDisabled); err != nil {
			t.Fatalf("SetStatus: %v", err)
		}
		active, err := mappings.Active(ctx, integ.ID)
		if err != nil {
			t.Fatalf("Active: %v", err)
		}
		if len(active) != 1 {
			t.Errorf("got %d active mappings, want 1", len(active))
		}
		// Still listed, so an operator can see what they turned off.
		all, err := mappings.List(ctx, integ.Name)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(all) != 2 {
			t.Errorf("got %d mappings in the full list, want 2", len(all))
		}

		if err := mappings.SetStatus(ctx, m.ID, skumap.StatusActive); err != nil {
			t.Fatalf("SetStatus back: %v", err)
		}
	})

	t.Run("invalid input is rejected", func(t *testing.T) {
		if _, err := mappings.Add(ctx, integ.Name, "", "BCPL-X", 0); !errors.Is(err, skumap.ErrInvalidArgument) {
			t.Errorf("empty gluzo sku: got %v", err)
		}
		if _, err := mappings.Add(ctx, integ.Name, "GLZ-X", "BCPL-X", -1); !errors.Is(err, skumap.ErrInvalidArgument) {
			t.Errorf("negative buffer: got %v", err)
		}
		if err := mappings.SetStatus(ctx, m.ID, "paused"); !errors.Is(err, skumap.ErrInvalidArgument) {
			t.Errorf("unknown status: got %v", err)
		}
	})

	t.Run("unknown integrations and mappings are reported", func(t *testing.T) {
		if _, err := mappings.Add(ctx, "missing-"+suffix, "A", "B", 0); !errors.Is(err, skumap.ErrNotFound) {
			t.Errorf("unknown integration: got %v", err)
		}
		if err := mappings.SetStatus(ctx, uuid.New(), skumap.StatusActive); !errors.Is(err, skumap.ErrNotFound) {
			t.Errorf("unknown mapping: got %v", err)
		}
		if err := mappings.Remove(ctx, uuid.New()); !errors.Is(err, skumap.ErrNotFound) {
			t.Errorf("remove unknown: got %v", err)
		}
	})

	t.Run("remove deletes the row", func(t *testing.T) {
		if err := mappings.Remove(ctx, m.ID); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		active, err := mappings.Active(ctx, integ.ID)
		if err != nil {
			t.Fatalf("Active: %v", err)
		}
		for _, mapping := range active {
			if mapping.ID == m.ID {
				t.Error("the removed mapping is still active")
			}
		}
	})
}

// An index built from the store must translate what the store holds. This is
// the join between the two halves of the package, and the place a schema
// change would break quietly.
func TestSKUMapIndexFromStore(t *testing.T) {
	ctx := context.Background()
	creds := auth.NewStore(pool)
	mappings := skumap.NewStore(pool)
	suffix := uuid.NewString()[:8]

	src, _, err := creds.CreatePlatform(ctx, "easyecom-"+suffix, auth.PlatformTypeSource)
	if err != nil {
		t.Fatalf("CreatePlatform: %v", err)
	}
	dst, _, err := creds.CreatePlatform(ctx, "vinculum-"+suffix, auth.PlatformTypeDestination)
	if err != nil {
		t.Fatalf("CreatePlatform: %v", err)
	}
	integ, err := creds.CreateIntegration(ctx, "easyecom-vinculum-"+suffix, src.Name, dst.Name)
	if err != nil {
		t.Fatalf("CreateIntegration: %v", err)
	}

	if _, err := mappings.Add(ctx, integ.Name, "GLZ-CHY-500", "BCPL-CHY-500", 5); err != nil {
		t.Fatalf("Add: %v", err)
	}

	active, err := mappings.Active(ctx, integ.ID)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	idx := skumap.NewIndex(active)

	mapping, err := idx.ToGluzo("BCPL-CHY-500")
	if err != nil {
		t.Fatalf("ToGluzo: %v", err)
	}
	if mapping.GluzoSKU != "GLZ-CHY-500" {
		t.Errorf("translated to %q", mapping.GluzoSKU)
	}
	// 120 held at the vendor, 5 withheld as the buffer.
	if got := mapping.Publishable(120); got != 115 {
		t.Errorf("Publishable(120) = %d, want 115", got)
	}

	if _, err := idx.ToGluzo("BCPL-NOT-MAPPED"); !errors.Is(err, skumap.ErrUnmapped) {
		t.Errorf("unmapped sku: got %v, want ErrUnmapped", err)
	}
}
