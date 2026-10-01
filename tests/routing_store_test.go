package tests

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/gluzo/integration-gateway/app/auth"
	"github.com/gluzo/integration-gateway/app/routing"
)

func TestRoutingStore(t *testing.T) {
	ctx := context.Background()
	creds := auth.NewStore(pool)
	routes := routing.NewStore(pool)
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

	route, err := routes.AddRoute(ctx, integ.Name, routing.TypeWarehouse, "12345", "DEL", "bcpl-loc-key")
	if err != nil {
		t.Fatalf("AddRoute: %v", err)
	}
	if route.IntegrationID != integ.ID || route.Status != routing.StatusActive || route.DestinationReference != "DEL" {
		t.Fatalf("unexpected route: %+v", route)
	}
	// A route names both ends. The vendor-side reference selects the
	// vendor's location; the origin-side one selects the EasyEcom location a
	// stock push authenticates for, which is what keeps vendor stock out of
	// Gluzo's own warehouse.
	if route.OriginReference != "bcpl-loc-key" {
		t.Fatalf("origin reference = %q, want bcpl-loc-key", route.OriginReference)
	}
	if _, err := routes.AddRoute(ctx, integ.Name, routing.TypeWarehouse, "12345", "", ""); !errors.Is(err, routing.ErrAlreadyExists) {
		t.Fatalf("duplicate route: err = %v", err)
	}
	if _, err := routes.AddRoute(ctx, "missing-"+suffix, routing.TypeWarehouse, "1", "", ""); !errors.Is(err, routing.ErrNotFound) {
		t.Fatalf("missing integration: err = %v", err)
	}
	if _, err := routes.AddRoute(ctx, integ.Name, "", "1", "", ""); !errors.Is(err, routing.ErrInvalidArgument) {
		t.Fatalf("empty type: err = %v", err)
	}

	key := routing.Key{Type: routing.TypeWarehouse, Value: "12345"}
	res, err := routes.Resolve(ctx, integ.ID, key)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.IntegrationID != integ.ID || res.IntegrationName != integ.Name || res.SourcePlatform != src.Name || res.DestinationPlatform != dst.Name || res.Route.DestinationReference != "DEL" {
		t.Fatalf("unexpected resolution: %+v", res)
	}
	if res.Route.OriginReference != "bcpl-loc-key" {
		t.Fatalf("resolution dropped the origin reference: %+v", res.Route)
	}

	// A route added without either reference resolves with both empty
	// rather than with one standing in for the other.
	if _, err := routes.AddRoute(ctx, integ.Name, routing.TypeWarehouse, "99999", "", ""); err != nil {
		t.Fatalf("AddRoute without references: %v", err)
	}
	bare, err := routes.Resolve(ctx, integ.ID, routing.Key{Type: routing.TypeWarehouse, Value: "99999"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if bare.Route.DestinationReference != "" || bare.Route.OriginReference != "" {
		t.Errorf("unset references came back as %+v", bare.Route)
	}

	if _, err := routes.Resolve(ctx, integ.ID, routing.Key{Type: routing.TypeWarehouse, Value: "unknown"}); !errors.Is(err, routing.ErrNoRoute) {
		t.Fatalf("unknown warehouse: err = %v", err)
	}
	if _, err := routes.Resolve(ctx, otherInteg.ID, key); !errors.Is(err, routing.ErrNoRoute) {
		t.Fatalf("route must be scoped to its integration: err = %v", err)
	}
	if _, err := routes.Resolve(ctx, integ.ID, routing.Key{}); !errors.Is(err, routing.ErrInvalidArgument) {
		t.Fatalf("empty key: err = %v", err)
	}

	if err := routes.SetRouteStatus(ctx, route.ID, routing.StatusDisabled); err != nil {
		t.Fatalf("SetRouteStatus: %v", err)
	}
	if _, err := routes.Resolve(ctx, integ.ID, key); !errors.Is(err, routing.ErrNoRoute) {
		t.Fatalf("disabled route resolved: err = %v", err)
	}
	if err := routes.SetRouteStatus(ctx, route.ID, routing.StatusActive); err != nil {
		t.Fatalf("SetRouteStatus active: %v", err)
	}
	if err := routes.SetRouteStatus(ctx, uuid.New(), routing.StatusActive); !errors.Is(err, routing.ErrNotFound) {
		t.Fatalf("set status unknown: err = %v", err)
	}

	if err := creds.SetIntegrationStatus(ctx, integ.Name, auth.StatusDisabled); err != nil {
		t.Fatalf("SetIntegrationStatus: %v", err)
	}
	if _, err := routes.Resolve(ctx, integ.ID, key); !errors.Is(err, routing.ErrNoRoute) {
		t.Fatalf("route of disabled integration resolved: err = %v", err)
	}
	if err := creds.SetIntegrationStatus(ctx, integ.Name, auth.StatusActive); err != nil {
		t.Fatalf("SetIntegrationStatus active: %v", err)
	}

	list, err := routes.ListRoutes(ctx, integ.Name)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListRoutes = %+v, %v", list, err)
	}
	// The listing carries both references, so an operator can see which
	// location each end of a route points at without reading the database.
	var listed *routing.Route
	for i := range list {
		if list[i].ID == route.ID {
			listed = &list[i]
		}
	}
	if listed == nil {
		t.Fatalf("ListRoutes did not return the added route: %+v", list)
	}
	if listed.DestinationReference != "DEL" || listed.OriginReference != "bcpl-loc-key" {
		t.Errorf("listed route lost a reference: %+v", *listed)
	}
	if err := routes.RemoveRoute(ctx, route.ID); err != nil {
		t.Fatalf("RemoveRoute: %v", err)
	}
	if err := routes.RemoveRoute(ctx, route.ID); !errors.Is(err, routing.ErrNotFound) {
		t.Fatalf("second remove: err = %v", err)
	}
	if _, err := routes.Resolve(ctx, integ.ID, key); !errors.Is(err, routing.ErrNoRoute) {
		t.Fatalf("removed route resolved: err = %v", err)
	}
}
