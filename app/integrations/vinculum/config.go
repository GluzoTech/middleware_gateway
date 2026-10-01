// Package vinculum is the Vinculum eRetail integration: HTTP client, endpoint
// methods and the mappers that turn its payloads into Gluzo's domain models.
// Its DTOs live in the dto subpackages and never leave this package tree.
//
// Vinculum is a fulfilment vendor, not a destination warehouse. BCPL holds
// the stock and ships it, so everything this package reads — stock positions
// and dispatch records — flows inward. Nothing here writes stock back to
// Vinculum, and nothing may.
//
// Authentication is two static headers. There is no token exchange, no
// refresh and no expiry, so the token-source machinery in the EasyEcom client
// has no counterpart here.
package vinculum

import (
	"errors"
	"strings"
	"time"
)

// PlatformName is how Vinculum is identified in routing, logs and the
// platforms table.
const PlatformName = "vinculum"

// DefaultBaseURL is Vinculum eRetail's production host.
const DefaultBaseURL = "https://erp.vineretail.com"

// LocationLength is the width Vinculum documents for orderLocation.
const LocationLength = 3

// Config holds the credentials and tuning for outbound Vinculum calls.
type Config struct {
	BaseURL string

	// APIOwner and APIKey are sent as the ApiOwner and ApiKey headers on
	// every request.
	APIOwner string
	APIKey   string

	// Location is the default three-character orderLocation. A route's
	// vendor reference overrides it; this is the fallback for calls made
	// outside a route, such as an operator-driven stock read.
	Location string

	// SellableBucket names the stock bucket whose quantity the storefront
	// may publish. Stock in any other bucket is ignored.
	//
	// TODO(VERIFY): BCPL open item 2. Which bucket value represents good,
	// sellable stock is not documented and must be confirmed before Phase 4
	// pushes anything. An empty value accepts every bucket, which is the
	// safe reading for a read-only phase but not for a push.
	SellableBucket string

	// DuplicateOrderCodes are the responseCode values Vinculum returns when
	// an order number already exists. Empty falls back to keyword matching
	// on the vendor's message.
	//
	// TODO(VERIFY): BCPL open item. The specification states that a
	// duplicate is rejected but does not give the code or the wording.
	DuplicateOrderCodes []string

	// OrderRateLimit and OrderRateWindow bound order creation. Vinculum
	// documents 80 calls per 5 minutes on that endpoint; zero uses the
	// documented figures.
	OrderRateLimit  int
	OrderRateWindow time.Duration

	Timeout time.Duration
}

// Validate reports whether the configuration is usable for outbound calls.
func (c Config) Validate() error {
	var errs []error
	if strings.TrimSpace(c.BaseURL) == "" {
		errs = append(errs, errors.New("vinculum: base URL is required"))
	}
	if strings.TrimSpace(c.APIOwner) == "" {
		errs = append(errs, errors.New("vinculum: API owner is required"))
	}
	if strings.TrimSpace(c.APIKey) == "" {
		errs = append(errs, errors.New("vinculum: API key is required"))
	}
	if loc := strings.TrimSpace(c.Location); loc != "" && len(loc) > LocationLength {
		errs = append(errs, errors.New("vinculum: location must be at most three characters"))
	}
	if c.Timeout < 0 {
		errs = append(errs, errors.New("vinculum: timeout must not be negative"))
	}
	if c.OrderRateLimit < 0 {
		errs = append(errs, errors.New("vinculum: order rate limit must not be negative"))
	}
	if c.OrderRateWindow < 0 {
		errs = append(errs, errors.New("vinculum: order rate window must not be negative"))
	}
	return errors.Join(errs...)
}
