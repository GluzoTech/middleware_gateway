// Command gatewayctl provides operator commands for the Gluzo Integration
// Gateway: applying migrations and provisioning platforms, integrations,
// access tokens and routes. Secrets are printed exactly once, at creation
// time, and are never stored in plaintext.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gluzo/integration-gateway/app/auth"
	"github.com/gluzo/integration-gateway/app/couriermap"
	"github.com/gluzo/integration-gateway/app/database/migrations"
	"github.com/gluzo/integration-gateway/app/database/postgres"
	"github.com/gluzo/integration-gateway/app/routing"
	"github.com/gluzo/integration-gateway/app/skumap"
)

const usage = `gatewayctl - operator commands for the Gluzo Integration Gateway

Usage:
  gatewayctl migrate
  gatewayctl platform create --name NAME --type source|destination|both
  gatewayctl platform rotate-key --name NAME
  gatewayctl platform set-status --name NAME --status active|disabled
  gatewayctl platform list
  gatewayctl integration create --name NAME --source PLATFORM --destination PLATFORM
  gatewayctl integration set-status --name NAME --status active|disabled
  gatewayctl integration list
  gatewayctl token issue --integration NAME --name LABEL [--ttl DURATION]
  gatewayctl token revoke --id TOKEN_ID
  gatewayctl token list --integration NAME
  gatewayctl route add --integration NAME --type warehouse_id --value VALUE [--destination-ref REF] [--origin-ref REF]
  gatewayctl route set-status --id ROUTE_ID --status active|disabled
  gatewayctl route remove --id ROUTE_ID
  gatewayctl route list --integration NAME
  gatewayctl skumap add --integration NAME --gluzo-sku SKU --vendor-sku SKU [--safety-buffer N]
  gatewayctl skumap set-status --id MAPPING_ID --status active|disabled
  gatewayctl skumap remove --id MAPPING_ID
  gatewayctl skumap list --integration NAME
  gatewayctl courier add --integration NAME --transporter NAME --carrier-id ID [--name NAME]
  gatewayctl courier set-status --id MAPPING_ID --status active|disabled
  gatewayctl courier remove --id MAPPING_ID
  gatewayctl courier list --integration NAME

Environment:
  DATABASE_URL  PostgreSQL connection URL (required)

Generated API keys and tokens are shown once. Store them in your secret
manager immediately; they cannot be recovered afterwards.
`

var errUsage = errors.New("usage")

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr, os.LookupEnv); err != nil {
		if errors.Is(err, errUsage) {
			fmt.Fprint(os.Stderr, usage)
		} else {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer, lookup func(string) (string, bool)) error {
	if len(args) == 0 {
		return errUsage
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	case "migrate":
		return withPool(ctx, lookup, func(pool *pgxpool.Pool) error {
			logger := slog.New(slog.NewTextHandler(stderr, nil))
			if err := migrations.Apply(ctx, pool, migrations.Files(), logger); err != nil {
				return err
			}
			fmt.Fprintln(stdout, "migrations up to date")
			return nil
		})
	case "platform":
		return platformCommand(ctx, args[1:], stdout, stderr, lookup)
	case "integration":
		return integrationCommand(ctx, args[1:], stdout, lookup)
	case "token":
		return tokenCommand(ctx, args[1:], stdout, stderr, lookup)
	case "route":
		return routeCommand(ctx, args[1:], stdout, lookup)
	case "skumap":
		return skumapCommand(ctx, args[1:], stdout, lookup)
	case "courier":
		return courierCommand(ctx, args[1:], stdout, lookup)
	default:
		return fmt.Errorf("%w: unknown command %q", errUsage, args[0])
	}
}

func withPool(ctx context.Context, lookup func(string) (string, bool), fn func(*pgxpool.Pool) error) error {
	url, _ := lookup("DATABASE_URL")
	if strings.TrimSpace(url) == "" {
		return errors.New("DATABASE_URL is required")
	}
	pool, err := postgres.Connect(ctx, postgres.Config{URL: url, MaxConns: 2, ConnectTimeout: 10 * time.Second})
	if err != nil {
		return err
	}
	defer pool.Close()
	return fn(pool)
}

func withStore(ctx context.Context, lookup func(string) (string, bool), fn func(*auth.Store) error) error {
	return withPool(ctx, lookup, func(pool *pgxpool.Pool) error {
		return fn(auth.NewStore(pool))
	})
}

func platformCommand(ctx context.Context, args []string, stdout, stderr io.Writer, lookup func(string) (string, bool)) error {
	if len(args) == 0 {
		return errUsage
	}
	fs := flag.NewFlagSet("platform "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "platform name")
	platformType := fs.String("type", auth.PlatformTypeSource, "source, destination or both")
	status := fs.String("status", "", "active or disabled")
	if err := fs.Parse(args[1:]); err != nil {
		return errUsage
	}

	switch args[0] {
	case "create":
		if *name == "" {
			return fmt.Errorf("%w: --name is required", errUsage)
		}
		return withStore(ctx, lookup, func(store *auth.Store) error {
			p, key, err := store.CreatePlatform(ctx, *name, *platformType)
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "platform %q created (id %s, type %s)\n", p.Name, p.ID, p.Type)
			if key != "" {
				printSecret(stdout, "platform API key (X-API-Key)", key)
			}
			return nil
		})
	case "rotate-key":
		if *name == "" {
			return fmt.Errorf("%w: --name is required", errUsage)
		}
		return withStore(ctx, lookup, func(store *auth.Store) error {
			key, err := store.RotatePlatformKey(ctx, *name)
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "platform %q key rotated; the previous key no longer works\n", *name)
			printSecret(stdout, "platform API key (X-API-Key)", key)
			return nil
		})
	case "set-status":
		if *name == "" || *status == "" {
			return fmt.Errorf("%w: --name and --status are required", errUsage)
		}
		return withStore(ctx, lookup, func(store *auth.Store) error {
			if err := store.SetPlatformStatus(ctx, *name, *status); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "platform %q is now %s\n", *name, *status)
			return nil
		})
	case "list":
		return withStore(ctx, lookup, func(store *auth.Store) error {
			platforms, err := store.ListPlatforms(ctx)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tTYPE\tSTATUS\tCREATED")
			for _, p := range platforms {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.ID, p.Name, p.Type, p.Status, p.CreatedAt.Format(time.RFC3339))
			}
			return tw.Flush()
		})
	default:
		return fmt.Errorf("%w: unknown platform command %q", errUsage, args[0])
	}
}

func integrationCommand(ctx context.Context, args []string, stdout io.Writer, lookup func(string) (string, bool)) error {
	if len(args) == 0 {
		return errUsage
	}
	fs := flag.NewFlagSet("integration "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "integration name")
	source := fs.String("source", "", "source platform name")
	destination := fs.String("destination", "", "destination platform name")
	status := fs.String("status", "", "active or disabled")
	if err := fs.Parse(args[1:]); err != nil {
		return errUsage
	}

	switch args[0] {
	case "create":
		if *name == "" || *source == "" || *destination == "" {
			return fmt.Errorf("%w: --name, --source and --destination are required", errUsage)
		}
		return withStore(ctx, lookup, func(store *auth.Store) error {
			i, err := store.CreateIntegration(ctx, *name, *source, *destination)
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "integration %q created (id %s, %s -> %s)\n", i.Name, i.ID, *source, *destination)
			return nil
		})
	case "set-status":
		if *name == "" || *status == "" {
			return fmt.Errorf("%w: --name and --status are required", errUsage)
		}
		return withStore(ctx, lookup, func(store *auth.Store) error {
			if err := store.SetIntegrationStatus(ctx, *name, *status); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "integration %q is now %s\n", *name, *status)
			return nil
		})
	case "list":
		return withStore(ctx, lookup, func(store *auth.Store) error {
			integrations, err := store.ListIntegrations(ctx)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tSOURCE_PLATFORM_ID\tDESTINATION_PLATFORM_ID\tSTATUS\tCREATED")
			for _, i := range integrations {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", i.ID, i.Name, i.SourcePlatformID, i.DestinationPlatformID, i.Status, i.CreatedAt.Format(time.RFC3339))
			}
			return tw.Flush()
		})
	default:
		return fmt.Errorf("%w: unknown integration command %q", errUsage, args[0])
	}
}

func tokenCommand(ctx context.Context, args []string, stdout, stderr io.Writer, lookup func(string) (string, bool)) error {
	if len(args) == 0 {
		return errUsage
	}
	fs := flag.NewFlagSet("token "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	integration := fs.String("integration", "", "integration name")
	name := fs.String("name", "", "token label")
	ttl := fs.Duration("ttl", 0, "lifetime such as 720h; 0 means no expiry")
	id := fs.String("id", "", "token id")
	if err := fs.Parse(args[1:]); err != nil {
		return errUsage
	}

	switch args[0] {
	case "issue":
		if *integration == "" || *name == "" {
			return fmt.Errorf("%w: --integration and --name are required", errUsage)
		}
		return withStore(ctx, lookup, func(store *auth.Store) error {
			t, plaintext, err := store.IssueToken(ctx, *integration, *name, *ttl)
			if err != nil {
				return err
			}
			expiry := "never"
			if t.ExpiresAt != nil {
				expiry = t.ExpiresAt.Format(time.RFC3339)
			}
			fmt.Fprintf(stdout, "token %q issued for integration %q (id %s, expires %s)\n", t.Name, *integration, t.ID, expiry)
			printSecret(stdout, "access token (Authorization: Bearer)", plaintext)
			return nil
		})
	case "revoke":
		tokenID, err := uuid.Parse(*id)
		if err != nil {
			return fmt.Errorf("%w: --id must be a token UUID", errUsage)
		}
		return withStore(ctx, lookup, func(store *auth.Store) error {
			if err := store.RevokeToken(ctx, tokenID); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "token %s revoked\n", tokenID)
			return nil
		})
	case "list":
		if *integration == "" {
			return fmt.Errorf("%w: --integration is required", errUsage)
		}
		return withStore(ctx, lookup, func(store *auth.Store) error {
			tokens, err := store.ListTokens(ctx, *integration)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tEXPIRES\tREVOKED\tLAST_USED\tCREATED")
			for _, t := range tokens {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", t.ID, t.Name, formatTime(t.ExpiresAt), formatTime(t.RevokedAt), formatTime(t.LastUsedAt), t.CreatedAt.Format(time.RFC3339))
			}
			return tw.Flush()
		})
	default:
		return fmt.Errorf("%w: unknown token command %q", errUsage, args[0])
	}
}

func routeCommand(ctx context.Context, args []string, stdout io.Writer, lookup func(string) (string, bool)) error {
	if len(args) == 0 {
		return errUsage
	}
	fs := flag.NewFlagSet("route "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	integration := fs.String("integration", "", "integration name")
	routeType := fs.String("type", routing.TypeWarehouse, "route type, e.g. warehouse_id")
	value := fs.String("value", "", "route value, e.g. the EasyEcom warehouse id")
	destRef := fs.String("destination-ref", "", "vendor-side reference, e.g. Vinculum's three-character orderLocation")
	originRef := fs.String("origin-ref", "", "origin-side reference, e.g. the EasyEcom location_key stock is written under")
	status := fs.String("status", "", "active or disabled")
	id := fs.String("id", "", "route id")
	if err := fs.Parse(args[1:]); err != nil {
		return errUsage
	}

	withRoutes := func(fn func(*routing.Store) error) error {
		return withPool(ctx, lookup, func(pool *pgxpool.Pool) error { return fn(routing.NewStore(pool)) })
	}

	switch args[0] {
	case "add":
		if *integration == "" || *routeType == "" || *value == "" {
			return fmt.Errorf("%w: --integration, --type and --value are required", errUsage)
		}
		return withRoutes(func(store *routing.Store) error {
			r, err := store.AddRoute(ctx, *integration, *routeType, *value, *destRef, *originRef)
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "route %s=%s -> integration %q added (id %s, vendor ref %q, origin ref %q)\n",
				r.Type, r.Value, *integration, r.ID, r.DestinationReference, r.OriginReference)
			return nil
		})
	case "set-status":
		routeID, err := uuid.Parse(*id)
		if err != nil || *status == "" {
			return fmt.Errorf("%w: --id (UUID) and --status are required", errUsage)
		}
		return withRoutes(func(store *routing.Store) error {
			if err := store.SetRouteStatus(ctx, routeID, *status); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "route %s is now %s\n", routeID, *status)
			return nil
		})
	case "remove":
		routeID, err := uuid.Parse(*id)
		if err != nil {
			return fmt.Errorf("%w: --id must be a route UUID", errUsage)
		}
		return withRoutes(func(store *routing.Store) error {
			if err := store.RemoveRoute(ctx, routeID); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "route %s removed\n", routeID)
			return nil
		})
	case "list":
		if *integration == "" {
			return fmt.Errorf("%w: --integration is required", errUsage)
		}
		return withRoutes(func(store *routing.Store) error {
			routes, err := store.ListRoutes(ctx, *integration)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tTYPE\tVALUE\tVENDOR_REF\tORIGIN_REF\tSTATUS\tCREATED")
			for _, r := range routes {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Type, r.Value, r.DestinationReference, r.OriginReference, r.Status, r.CreatedAt.Format(time.RFC3339))
			}
			return tw.Flush()
		})
	default:
		return fmt.Errorf("%w: unknown route command %q", errUsage, args[0])
	}
}

// skumapCommand manages the correspondence between Gluzo's SKUs and a
// vendor's item codes. Nothing about the mapping is derivable, so it is
// operator configuration rather than a rule in code.
func skumapCommand(ctx context.Context, args []string, stdout io.Writer, lookup func(string) (string, bool)) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: skumap needs a subcommand", errUsage)
	}
	fs := flag.NewFlagSet("skumap "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	integration := fs.String("integration", "", "integration name")
	gluzoSKU := fs.String("gluzo-sku", "", "the SKU the storefront and EasyEcom know")
	vendorSKU := fs.String("vendor-sku", "", "the item code the vendor knows")
	buffer := fs.Int("safety-buffer", 0, "quantity withheld from the storefront as protection against oversell")
	status := fs.String("status", "", "active or disabled")
	id := fs.String("id", "", "mapping id")
	if err := fs.Parse(args[1:]); err != nil {
		return errUsage
	}

	withMappings := func(fn func(*skumap.Store) error) error {
		return withPool(ctx, lookup, func(pool *pgxpool.Pool) error { return fn(skumap.NewStore(pool)) })
	}

	switch args[0] {
	case "add":
		if *integration == "" || *gluzoSKU == "" || *vendorSKU == "" {
			return fmt.Errorf("%w: --integration, --gluzo-sku and --vendor-sku are required", errUsage)
		}
		return withMappings(func(store *skumap.Store) error {
			m, err := store.Add(ctx, *integration, *gluzoSKU, *vendorSKU, *buffer)
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "mapping %s <-> %s added to integration %q (id %s, safety buffer %d)\n",
				m.GluzoSKU, m.VendorSKU, *integration, m.ID, m.SafetyBuffer)
			return nil
		})
	case "set-status":
		mappingID, err := uuid.Parse(*id)
		if err != nil || *status == "" {
			return fmt.Errorf("%w: --id must be a mapping UUID and --status is required", errUsage)
		}
		return withMappings(func(store *skumap.Store) error {
			if err := store.SetStatus(ctx, mappingID, *status); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "mapping %s is now %s\n", mappingID, *status)
			return nil
		})
	case "remove":
		mappingID, err := uuid.Parse(*id)
		if err != nil {
			return fmt.Errorf("%w: --id must be a mapping UUID", errUsage)
		}
		return withMappings(func(store *skumap.Store) error {
			if err := store.Remove(ctx, mappingID); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "mapping %s removed\n", mappingID)
			return nil
		})
	case "list":
		if *integration == "" {
			return fmt.Errorf("%w: --integration is required", errUsage)
		}
		return withMappings(func(store *skumap.Store) error {
			mappings, err := store.List(ctx, *integration)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tGLUZO_SKU\tVENDOR_SKU\tBUFFER\tSTATUS\tCREATED")
			for _, m := range mappings {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\n", m.ID, m.GluzoSKU, m.VendorSKU, m.SafetyBuffer, m.Status, m.CreatedAt.Format(time.RFC3339))
			}
			return tw.Flush()
		})
	default:
		return fmt.Errorf("%w: unknown skumap command %q", errUsage, args[0])
	}
}

// courierCommand manages the correspondence between a vendor's carrier names
// and the origin platform's carrier identifiers. The identifier is assigned
// by the origin when the carrier is registered on the account, so it can only
// ever be configuration.
func courierCommand(ctx context.Context, args []string, stdout io.Writer, lookup func(string) (string, bool)) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: courier needs a subcommand", errUsage)
	}
	fs := flag.NewFlagSet("courier "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	integration := fs.String("integration", "", "integration name")
	transporter := fs.String("transporter", "", "the carrier name as the vendor reports it")
	carrierID := fs.String("carrier-id", "", "the origin platform's own carrier identifier")
	name := fs.String("name", "", "the name to present to the origin platform, when it differs")
	status := fs.String("status", "", "active or disabled")
	id := fs.String("id", "", "mapping id")
	if err := fs.Parse(args[1:]); err != nil {
		return errUsage
	}

	withCouriers := func(fn func(*couriermap.Store) error) error {
		return withPool(ctx, lookup, func(pool *pgxpool.Pool) error { return fn(couriermap.NewStore(pool)) })
	}

	switch args[0] {
	case "add":
		if *integration == "" || *transporter == "" || *carrierID == "" {
			return fmt.Errorf("%w: --integration, --transporter and --carrier-id are required", errUsage)
		}
		return withCouriers(func(store *couriermap.Store) error {
			c, err := store.Add(ctx, *integration, *transporter, *carrierID, *name)
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "carrier %q -> %s added to integration %q (id %s)\n",
				c.Transporter, c.CompanyCarrierID, *integration, c.ID)
			return nil
		})
	case "set-status":
		mappingID, err := uuid.Parse(*id)
		if err != nil || *status == "" {
			return fmt.Errorf("%w: --id must be a mapping UUID and --status is required", errUsage)
		}
		return withCouriers(func(store *couriermap.Store) error {
			if err := store.SetStatus(ctx, mappingID, *status); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "carrier mapping %s is now %s\n", mappingID, *status)
			return nil
		})
	case "remove":
		mappingID, err := uuid.Parse(*id)
		if err != nil {
			return fmt.Errorf("%w: --id must be a mapping UUID", errUsage)
		}
		return withCouriers(func(store *couriermap.Store) error {
			if err := store.Remove(ctx, mappingID); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "carrier mapping %s removed\n", mappingID)
			return nil
		})
	case "list":
		if *integration == "" {
			return fmt.Errorf("%w: --integration is required", errUsage)
		}
		return withCouriers(func(store *couriermap.Store) error {
			couriers, err := store.List(ctx, *integration)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tTRANSPORTER\tCARRIER_ID\tPRESENTED_AS\tSTATUS\tCREATED")
			for _, c := range couriers {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", c.ID, c.Transporter, c.CompanyCarrierID,
					c.PresentedName(), c.Status, c.CreatedAt.Format(time.RFC3339))
			}
			return tw.Flush()
		})
	default:
		return fmt.Errorf("%w: unknown courier command %q", errUsage, args[0])
	}
}

func printSecret(w io.Writer, label, secret string) {
	fmt.Fprintf(w, "\n%s (shown once, store it securely):\n%s\n\n", label, secret)
}

func formatTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Format(time.RFC3339)
}
