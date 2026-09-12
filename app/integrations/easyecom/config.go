// Package easyecom is the EasyEcom integration: HTTP client, authentication,
// endpoint methods and webhook parsing. Its DTOs live in the dto subpackages
// and never leave this package tree; mappers translate them to the domain.
package easyecom

import (
	"errors"
	"strings"
	"time"
)

// PlatformName is how EasyEcom is identified in routing, logs and the
// platforms table.
const PlatformName = "easyecom"

// DefaultBaseURL is EasyEcom's production API host.
const DefaultBaseURL = "https://api.easyecom.io"

// Config holds the credentials and tuning for outbound EasyEcom calls.
//
// Every request carries the account API key (X-API-Key) and a JWT
// (Authorization: Bearer). The JWT is either supplied directly (JWTToken) or
// obtained by logging in with Email, Password and LocationKey.
type Config struct {
	BaseURL string
	APIKey  string

	JWTToken string

	Email       string
	Password    string
	LocationKey string

	Timeout time.Duration
}

// Validate reports whether the configuration is usable for outbound calls.
func (c Config) Validate() error {
	var errs []error
	if strings.TrimSpace(c.BaseURL) == "" {
		errs = append(errs, errors.New("easyecom: base URL is required"))
	}
	if strings.TrimSpace(c.APIKey) == "" {
		errs = append(errs, errors.New("easyecom: API key is required"))
	}
	if strings.TrimSpace(c.JWTToken) == "" {
		if strings.TrimSpace(c.Email) == "" || strings.TrimSpace(c.Password) == "" || strings.TrimSpace(c.LocationKey) == "" {
			errs = append(errs, errors.New("easyecom: either a JWT token or email, password and location key are required"))
		}
	}
	if c.Timeout < 0 {
		errs = append(errs, errors.New("easyecom: timeout must not be negative"))
	}
	return errors.Join(errs...)
}
