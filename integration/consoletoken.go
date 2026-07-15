package integration

// This file verifies the platform-issued integration console view-token.
//
// The console iframe (integrations.console_url) is opened inside a project and
// never receives the platform's auth tokens. Instead the platform hands the
// iframe a short-lived, signed view-token via postMessage; the iframe forwards
// it to the integration backend. The backend verifies its Ed25519 signature
// against the platform's published JWKS and only then trusts its projectId
// claim.
//
// The token is a standard EdDSA JWT (RFC 8037) with a `kid` header selecting the
// public key from the platform JWKS (see DefaultJWKSURL). The algorithm is
// pinned to EdDSA, so "alg=none" and algorithm-confusion attacks do not apply.
// The JWKS is fetched and cached through the same internal/sign.KeySet used for
// inbound request signatures, so there is a single JWKS fetcher in the SDK.

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/sign"
)

// Fixed claim values a console view-token must carry. They mirror the platform
// signer.
const (
	consoleExpectedIssuer  = "aheron"
	consoleExpectedPurpose = "integration-console"
)

// consoleLeeway absorbs small clock skew between the platform and the
// integration when checking exp/nbf.
const consoleLeeway = 60 * time.Second

// ErrConsoleTokenInvalid is the sentinel wrapped by every console view-token
// verification failure, so a caller can answer 401 without leaking which
// specific check failed.
var ErrConsoleTokenInvalid = errors.New("integration: console view-token verification failed")

// ConsoleClaims is the verified, trusted content of a console view-token.
type ConsoleClaims struct {
	ProjectID     string
	IntegrationID string
	ExpiresAt     time.Time
}

// ConsoleVerifierConfig configures a ConsoleVerifier.
type ConsoleVerifierConfig struct {
	// JWKSURL is the platform's well-known integration JWKS endpoint. An empty
	// value falls back to DefaultJWKSURL (aheron.pro).
	JWKSURL string
	// IntegrationID is this integration's id; a token's `aud` must contain it.
	// Required.
	IntegrationID string
	// HTTPClient fetches the JWKS. Defaults to http.DefaultClient.
	HTTPClient *http.Client
	// CacheTTL is how long fetched keys are cached. Defaults to 5 minutes.
	CacheTTL time.Duration
}

// ConsoleVerifier checks console view-tokens against the platform JWKS. It is
// safe for concurrent use.
type ConsoleVerifier struct {
	keys          *sign.KeySet
	integrationID string
	now           func() time.Time
}

// NewConsoleVerifier builds a ConsoleVerifier. An empty JWKSURL falls back to
// DefaultJWKSURL; it returns an error when IntegrationID is empty.
func NewConsoleVerifier(cfg ConsoleVerifierConfig) (*ConsoleVerifier, error) {
	if cfg.IntegrationID == "" {
		return nil, fmt.Errorf("integration: ConsoleVerifierConfig.IntegrationID is required")
	}
	if cfg.JWKSURL == "" {
		cfg.JWKSURL = DefaultJWKSURL
	}
	return &ConsoleVerifier{
		keys:          sign.NewKeySet(cfg.JWKSURL, cfg.HTTPClient, cfg.CacheTTL),
		integrationID: cfg.IntegrationID,
		now:           time.Now,
	}, nil
}

type consoleJWTHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

type consoleJWTClaims struct {
	Iss           string          `json:"iss"`
	Aud           json.RawMessage `json:"aud"`
	Exp           int64           `json:"exp"`
	Nbf           int64           `json:"nbf"`
	ProjectID     string          `json:"projectId"`
	IntegrationID string          `json:"integrationId"`
	Purpose       string          `json:"purpose"`
}

// Verify parses and fully validates a console view-token: the EdDSA signature
// against the JWKS (by kid), then the iss/aud/purpose/exp/nbf claims. An
// optional "Bearer " prefix is stripped. On success it returns the trusted
// claims. Every failure wraps ErrConsoleTokenInvalid.
func (v *ConsoleVerifier) Verify(ctx context.Context, token string) (ConsoleClaims, error) {
	token = strings.TrimSpace(token)
	token = strings.TrimPrefix(token, "Bearer ")
	token = strings.TrimPrefix(token, "bearer ")

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ConsoleClaims{}, fmt.Errorf("%w: malformed token", ErrConsoleTokenInvalid)
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ConsoleClaims{}, fmt.Errorf("%w: decode header: %v", ErrConsoleTokenInvalid, err)
	}
	var header consoleJWTHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return ConsoleClaims{}, fmt.Errorf("%w: parse header: %v", ErrConsoleTokenInvalid, err)
	}
	if header.Alg != "EdDSA" {
		return ConsoleClaims{}, fmt.Errorf("%w: unexpected alg %q", ErrConsoleTokenInvalid, header.Alg)
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return ConsoleClaims{}, fmt.Errorf("%w: decode signature: %v", ErrConsoleTokenInvalid, err)
	}

	key, err := v.keys.Key(ctx, header.Kid)
	if err != nil {
		return ConsoleClaims{}, fmt.Errorf("%w: select key: %v", ErrConsoleTokenInvalid, err)
	}

	signingInput := parts[0] + "." + parts[1]
	if !ed25519.Verify(key, []byte(signingInput), sig) {
		return ConsoleClaims{}, fmt.Errorf("%w: bad signature", ErrConsoleTokenInvalid)
	}

	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ConsoleClaims{}, fmt.Errorf("%w: decode claims: %v", ErrConsoleTokenInvalid, err)
	}
	var claims consoleJWTClaims
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return ConsoleClaims{}, fmt.Errorf("%w: parse claims: %v", ErrConsoleTokenInvalid, err)
	}

	if claims.Iss != consoleExpectedIssuer {
		return ConsoleClaims{}, fmt.Errorf("%w: unexpected issuer %q", ErrConsoleTokenInvalid, claims.Iss)
	}
	if claims.Purpose != consoleExpectedPurpose {
		return ConsoleClaims{}, fmt.Errorf("%w: unexpected purpose %q", ErrConsoleTokenInvalid, claims.Purpose)
	}
	if !consoleAudienceContains(claims.Aud, v.integrationID) {
		return ConsoleClaims{}, fmt.Errorf("%w: audience mismatch", ErrConsoleTokenInvalid)
	}
	if claims.IntegrationID != "" && claims.IntegrationID != v.integrationID {
		return ConsoleClaims{}, fmt.Errorf("%w: integrationId mismatch", ErrConsoleTokenInvalid)
	}
	if claims.ProjectID == "" {
		return ConsoleClaims{}, fmt.Errorf("%w: missing projectId", ErrConsoleTokenInvalid)
	}

	now := v.now()
	if claims.Exp == 0 {
		return ConsoleClaims{}, fmt.Errorf("%w: missing exp", ErrConsoleTokenInvalid)
	}
	exp := time.Unix(claims.Exp, 0)
	if now.After(exp.Add(consoleLeeway)) {
		return ConsoleClaims{}, fmt.Errorf("%w: token expired", ErrConsoleTokenInvalid)
	}
	if claims.Nbf != 0 {
		nbf := time.Unix(claims.Nbf, 0)
		if now.Add(consoleLeeway).Before(nbf) {
			return ConsoleClaims{}, fmt.Errorf("%w: token not yet valid", ErrConsoleTokenInvalid)
		}
	}

	return ConsoleClaims{
		ProjectID:     claims.ProjectID,
		IntegrationID: v.integrationID,
		ExpiresAt:     exp,
	}, nil
}

// consoleAudienceContains reports whether the JWT `aud` claim (a string or an
// array of strings, per RFC 7519) contains want.
func consoleAudienceContains(raw json.RawMessage, want string) bool {
	if len(raw) == 0 {
		return false
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return single == want
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		for _, a := range many {
			if a == want {
				return true
			}
		}
	}
	return false
}
