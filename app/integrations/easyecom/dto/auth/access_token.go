// Package auth holds the EasyEcom authentication contracts.
//
// VERIFY: https://api-docs.easyecom.io is not machine-readable, so these
// shapes follow the public documentation as last observed (POST /access/token
// with email, password and location_key returning data.token.jwt_token). They
// must be confirmed against the live API before go-live.
package auth

import "github.com/gluzo/integration-gateway/app/integrations/easyecom/dto"

// AccessTokenRequest is the body of POST /access/token.
type AccessTokenRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	LocationKey string `json:"location_key"`
}

// AccessTokenResponse is the response of POST /access/token.
type AccessTokenResponse struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    AccessTokenData `json:"data"`
}

// AccessTokenData is the data member of AccessTokenResponse.
type AccessTokenData struct {
	Token TokenInfo `json:"token"`
}

// TokenInfo carries the issued JWT. EasyEcom documents the token as valid
// for 90 days; Expiry is kept when the API reports it.
type TokenInfo struct {
	JWT    string         `json:"jwt_token"`
	Expiry dto.FlexString `json:"expiry"`
}
