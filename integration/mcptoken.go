package integration

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/sign"
)

const (
	MCPTokenPurpose    = "integration-mcp"
	MCPTokenIssuer     = "aheron"
	MCPPermissionRead  = "mcp.read"
	MCPPermissionWrite = "mcp.write"
	MCPMaxTokenTTL     = 5 * time.Minute
)

var ErrMCPTokenInvalid = errors.New("integration: invalid MCP delegation")

// MCPClaims delegate one user's access to one installation. They cannot be
// accepted as console, installation OAuth, or platform user credentials.
type MCPClaims struct {
	Issuer                   string   `json:"iss"`
	Audience                 string   `json:"aud"`
	ActorID                  string   `json:"sub"`
	Purpose                  string   `json:"purpose"`
	ProjectID                string   `json:"projectId"`
	IntegrationID            string   `json:"integrationId"`
	InstallationID           string   `json:"installationId"`
	AccessVersion            int64    `json:"accessVersion"`
	IntegrationAccessVersion int64    `json:"integrationAccessVersion"`
	Permissions              []string `json:"permissions"`
	IssuedAt                 int64    `json:"iat"`
	ExpiresAt                int64    `json:"exp"`
}

func (c MCPClaims) HasPermission(permission string) bool {
	return slices.Contains(c.Permissions, permission)
}

func (c MCPClaims) valid(now time.Time) bool {
	for _, id := range []string{c.ActorID, c.ProjectID, c.IntegrationID, c.InstallationID} {
		if !mcpUUID(id) {
			return false
		}
	}
	if c.Issuer != MCPTokenIssuer || c.Purpose != MCPTokenPurpose || c.Audience != c.IntegrationID ||
		c.AccessVersion < 1 || c.IntegrationAccessVersion < 1 || c.IssuedAt < 1 ||
		c.IssuedAt > now.Add(30*time.Second).Unix() || c.ExpiresAt <= now.Unix() ||
		c.ExpiresAt <= c.IssuedAt || c.ExpiresAt-c.IssuedAt > int64(MCPMaxTokenTTL/time.Second) ||
		!c.HasPermission(MCPPermissionRead) {
		return false
	}
	for _, permission := range c.Permissions {
		if permission != MCPPermissionRead && permission != MCPPermissionWrite {
			return false
		}
	}
	return true
}

func mcpUUID(id string) bool {
	if len(id) != 36 || id == "00000000-0000-0000-0000-000000000000" {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// SignMCPToken is used by the platform's delegation issuer, never the model.
func SignMCPToken(claims MCPClaims, key ed25519.PrivateKey, kid string, now time.Time) (string, error) {
	if len(key) != ed25519.PrivateKeySize || kid == "" || !claims.valid(now) {
		return "", ErrMCPTokenInvalid
	}
	header, _ := json.Marshal(struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
		Type      string `json:"typ"`
	}{"EdDSA", kid, "JWT"})
	body, err := json.Marshal(claims)
	if err != nil {
		return "", ErrMCPTokenInvalid
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(input))), nil
}

type MCPVerifierConfig struct {
	IntegrationID string
	JWKSURL       string
	HTTPClient    *http.Client
	CacheTTL      time.Duration
}

type MCPVerifier struct {
	integrationID string
	keys          *sign.KeySet
}

func NewMCPVerifier(cfg MCPVerifierConfig) (*MCPVerifier, error) {
	if !mcpUUID(cfg.IntegrationID) {
		return nil, ErrMCPTokenInvalid
	}
	if cfg.JWKSURL == "" {
		cfg.JWKSURL = DefaultJWKSURL
	}
	return &MCPVerifier{integrationID: cfg.IntegrationID, keys: sign.NewKeySet(cfg.JWKSURL, cfg.HTTPClient, cfg.CacheTTL)}, nil
}

func (v *MCPVerifier) Verify(ctx context.Context, token string) (MCPClaims, error) {
	invalid := MCPClaims{}
	if len(token) > 8192 {
		return invalid, ErrMCPTokenInvalid
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return invalid, ErrMCPTokenInvalid
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	var h struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
		Type      string `json:"typ"`
	}
	if err != nil || json.Unmarshal(header, &h) != nil || h.Algorithm != "EdDSA" || h.KeyID == "" || h.Type != "JWT" {
		return invalid, ErrMCPTokenInvalid
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return invalid, ErrMCPTokenInvalid
	}
	key, err := v.keys.Key(ctx, h.KeyID)
	if err != nil || !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), signature) {
		return invalid, ErrMCPTokenInvalid
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	var claims MCPClaims
	if err != nil || json.Unmarshal(body, &claims) != nil || !claims.valid(time.Now()) || claims.IntegrationID != v.integrationID {
		return invalid, ErrMCPTokenInvalid
	}
	return claims, nil
}
