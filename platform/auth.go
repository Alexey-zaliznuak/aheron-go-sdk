// Package platform is the user-facing Aheron API client. It calls existing
// resource APIs using an ordinary user access JWT. It does not mint tokens,
// impersonate users, or turn project/integration credentials into user access.
package platform

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"time"
)

var (
	ErrCredential   = errors.New("platform: a user access token is required")
	ErrTokenExpired = errors.New("platform: user access token has expired")
)

// AccessToken holds an ordinary user JWT, not a separate platform token type.
// Its value is intentionally private and omitted from fmt/JSON output.
type AccessToken struct {
	value     string
	expiresAt time.Time
}

func (AccessToken) String() string   { return "[user access token redacted]" }
func (AccessToken) GoString() string { return "platform.AccessToken{redacted}" }

// NewAccessToken checks transport syntax only, NOT authenticity or permissions.
// The receiving API must validate signature, issuer, audience and expiration.
// expiresAt is optional; a zero value leaves expiry checking to the API.
func NewAccessToken(raw string, expiresAt time.Time) (AccessToken, error) {
	if len(raw) > 16<<10 {
		return AccessToken{}, ErrCredential
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return AccessToken{}, ErrCredential
	}
	for _, part := range parts {
		if part == "" || strings.ContainsAny(part, "\r\n") {
			return AccessToken{}, ErrCredential
		}
		if _, err := base64.RawURLEncoding.Strict().DecodeString(part); err != nil {
			return AccessToken{}, ErrCredential
		}
	}
	return AccessToken{value: raw, expiresAt: expiresAt}, nil
}

// TokenProvider supplies the CURRENT user's access token for one HTTP request.
// Implementations must be safe for concurrent calls and remain bound to one
// user's authorization. OAuth refresh/exchange belongs to the provider, not to
// individual resource methods. This interface never requests a refresh token.
type TokenProvider interface {
	Token(context.Context) (AccessToken, error)
}

type TokenProviderFunc func(context.Context) (AccessToken, error)

func (f TokenProviderFunc) Token(ctx context.Context) (AccessToken, error) {
	if f == nil {
		return AccessToken{}, ErrCredential
	}
	return f(ctx)
}

type staticToken struct{ token AccessToken }

func (s staticToken) Token(ctx context.Context) (AccessToken, error) {
	if err := ctx.Err(); err != nil {
		return AccessToken{}, err
	}
	return s.token, nil
}

// StaticToken binds a client to an existing access JWT. No credentials are
// exchanged or renewed. Use a new client/provider when the website refreshes it.
func StaticToken(raw string, expiresAt time.Time) (TokenProvider, error) {
	token, err := NewAccessToken(raw, expiresAt)
	if err != nil {
		return nil, err
	}
	return staticToken{token: token}, nil
}
