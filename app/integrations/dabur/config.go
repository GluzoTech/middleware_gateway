// Package dabur is the Dabur integration, implemented against Unicommerce
// Uniware: OAuth client, endpoint methods, DTOs and mappers, plus the
// destination adapter used by the order workflow.
//
// Contracts follow Unicommerce's public Uniware documentation
// (documentation.unicommerce.com). Dabur's tenant host, facility codes,
// channel code and any customisation of the standard payloads must be
// confirmed with Dabur; every such assumption is marked VERIFY.
package dabur

import (
	"errors"
	"strings"
	"time"
)

// PlatformName is how Dabur is identified in routing, logs and the
// platforms table.
const PlatformName = "dabur"

// Defaults.
const (
	DefaultClientID  = "my-trusted-client" // the client id Uniware documents for API users
	DefaultShelfCode = "DEFAULT"           // Uniware's default shelf in every facility
)

// Config holds the credentials and tuning for Uniware calls.
type Config struct {
	// BaseURL is the tenant host, e.g. https://dabur.unicommerce.com.
	BaseURL  string
	Username string
	Password string
	ClientID string

	// DefaultFacility is the Uniware facility code used when a route carries
	// no destination reference.
	DefaultFacility string
	// Channel is the Uniware channel code stamped on created orders.
	// VERIFY with Dabur: optional in the API, but tenants usually require it.
	Channel string
	// ShelfCode receives inventory adjustments.
	ShelfCode string
	// VerificationRequired asks Uniware to hold created orders for manual
	// verification. VERIFY with Dabur; false lets orders flow automatically.
	VerificationRequired bool

	Timeout time.Duration
}

// Validate reports whether the configuration is usable for outbound calls.
func (c Config) Validate() error {
	var errs []error
	if strings.TrimSpace(c.BaseURL) == "" {
		errs = append(errs, errors.New("dabur: base URL is required"))
	}
	if strings.TrimSpace(c.Username) == "" || strings.TrimSpace(c.Password) == "" {
		errs = append(errs, errors.New("dabur: username and password are required"))
	}
	if c.Timeout < 0 {
		errs = append(errs, errors.New("dabur: timeout must not be negative"))
	}
	return errors.Join(errs...)
}

func (c Config) withDefaults() Config {
	if strings.TrimSpace(c.ClientID) == "" {
		c.ClientID = DefaultClientID
	}
	if strings.TrimSpace(c.ShelfCode) == "" {
		c.ShelfCode = DefaultShelfCode
	}
	return c
}
