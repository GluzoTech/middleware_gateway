// Package auth holds the Uniware OAuth contracts
// (documentation.unicommerce.com/docs/oauth.html and oauth-refreshtoken.html).
package auth

// TokenResponse is returned by GET /oauth/token for both the password and
// the refresh_token grants.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"` // seconds
	Scope        string `json:"scope"`
}
