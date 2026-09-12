package routing_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/gluzo/integration-gateway/app/routing"
)

func TestMemoryResolver(t *testing.T) {
	ctx := context.Background()
	r := routing.NewMemoryResolver()
	integ := uuid.New()
	other := uuid.New()

	r.Add(routing.Resolution{
		Route:               routing.Route{ID: uuid.New(), IntegrationID: integ, Type: routing.TypeWarehouse, Value: "12345", DestinationReference: "DABUR-DEL"},
		IntegrationID:       integ,
		IntegrationName:     "easyecom-dabur",
		SourcePlatform:      "easyecom",
		DestinationPlatform: "dabur",
	})
	r.Add(routing.Resolution{
		Route:         routing.Route{ID: uuid.New(), IntegrationID: integ, Type: routing.TypeWarehouse, Value: "99999", Status: routing.StatusDisabled},
		IntegrationID: integ,
	})

	res, err := r.Resolve(ctx, integ, routing.Key{Type: routing.TypeWarehouse, Value: "12345"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.DestinationPlatform != "dabur" || res.Route.DestinationReference != "DABUR-DEL" || res.IntegrationName != "easyecom-dabur" {
		t.Fatalf("unexpected resolution: %+v", res)
	}

	tests := []struct {
		name  string
		integ uuid.UUID
		key   routing.Key
	}{
		{"unknown warehouse", integ, routing.Key{Type: routing.TypeWarehouse, Value: "1"}},
		{"other integration", other, routing.Key{Type: routing.TypeWarehouse, Value: "12345"}},
		{"wrong type", integ, routing.Key{Type: routing.TypeChannel, Value: "12345"}},
		{"disabled route", integ, routing.Key{Type: routing.TypeWarehouse, Value: "99999"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := r.Resolve(ctx, tt.integ, tt.key); !errors.Is(err, routing.ErrNoRoute) {
				t.Fatalf("err = %v, want ErrNoRoute", err)
			}
		})
	}
}

func TestKeyString(t *testing.T) {
	if got := (routing.Key{Type: "warehouse_id", Value: "5"}).String(); got != "warehouse_id=5" {
		t.Fatalf("String = %q", got)
	}
}
