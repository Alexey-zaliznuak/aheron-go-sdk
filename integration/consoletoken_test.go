package integration

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// consoleJWKSServer serves the given public keys (by kid) as a JWKS document.
// Toggling the returned flag makes the endpoint start failing with 500, so a
// stale-cache fallback can be exercised.
func consoleJWKSServer(t *testing.T, keys map[string]ed25519.PublicKey) (*httptest.Server, *atomic.Bool) {
	t.Helper()
	jwkList := make([]map[string]string, 0, len(keys))
	for kid, pub := range keys {
		jwkList = append(jwkList, map[string]string{
			"kty": "OKP", "crv": "Ed25519", "use": "sig", "alg": "EdDSA",
			"kid": kid, "x": base64.RawURLEncoding.EncodeToString(pub),
		})
	}
	body, _ := json.Marshal(map[string]any{"keys": jwkList})
	fail := &atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	return srv, fail
}

// signConsoleToken builds an EdDSA JWT (RFC 8037) for tests, mirroring the
// platform signer. An empty alg defaults to "EdDSA".
func signConsoleToken(t *testing.T, priv ed25519.PrivateKey, kid, alg string, claims map[string]any) string {
	t.Helper()
	if alg == "" {
		alg = "EdDSA"
	}
	header := map[string]any{"alg": alg, "typ": "JWT"}
	if kid != "" {
		header["kid"] = kid
	}
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signingInput := enc(header) + "." + enc(claims)
	sig := ed25519.Sign(priv, []byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func validConsoleClaims(integrationID, projectID string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss":           consoleExpectedIssuer,
		"aud":           integrationID,
		"sub":           projectID,
		"purpose":       consoleExpectedPurpose,
		"projectId":     projectID,
		"integrationId": integrationID,
		"iat":           now.Unix(),
		"nbf":           now.Unix(),
		"exp":           now.Add(5 * time.Minute).Unix(),
	}
}

const (
	testConsoleIntegrationID = "11111111-1111-1111-1111-111111111111"
	testConsoleProjectID     = "22222222-2222-2222-2222-222222222222"
)

func TestConsoleVerifyValidToken(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	const kid = "aheron-console-test"

	srv, _ := consoleJWKSServer(t, map[string]ed25519.PublicKey{kid: pub})
	defer srv.Close()

	v, err := NewConsoleVerifier(ConsoleVerifierConfig{
		JWKSURL:       srv.URL,
		IntegrationID: testConsoleIntegrationID,
		HTTPClient:    srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewConsoleVerifier: %v", err)
	}

	token := signConsoleToken(t, priv, kid, "", validConsoleClaims(testConsoleIntegrationID, testConsoleProjectID))
	claims, err := v.Verify(context.Background(), "Bearer "+token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.ProjectID != testConsoleProjectID {
		t.Fatalf("ProjectID = %q, want %q", claims.ProjectID, testConsoleProjectID)
	}
	if claims.IntegrationID != testConsoleIntegrationID {
		t.Fatalf("IntegrationID = %q, want %q", claims.IntegrationID, testConsoleIntegrationID)
	}
	if claims.ExpiresAt.IsZero() {
		t.Fatal("ExpiresAt is zero")
	}
}

func TestConsoleVerifyAudienceArray(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	const kid = "k1"

	srv, _ := consoleJWKSServer(t, map[string]ed25519.PublicKey{kid: pub})
	defer srv.Close()

	v, _ := NewConsoleVerifier(ConsoleVerifierConfig{
		JWKSURL: srv.URL, IntegrationID: testConsoleIntegrationID, HTTPClient: srv.Client(),
	})

	claims := validConsoleClaims(testConsoleIntegrationID, testConsoleProjectID)
	claims["aud"] = []string{"other-one", testConsoleIntegrationID, "other-two"}
	token := signConsoleToken(t, priv, kid, "", claims)

	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatalf("Verify with array audience: %v", err)
	}
}

func TestConsoleVerifyRejects(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	_, otherPriv, _ := ed25519.GenerateKey(nil)
	const kid = "aheron-console-test"

	srv, _ := consoleJWKSServer(t, map[string]ed25519.PublicKey{kid: pub})
	defer srv.Close()

	v, _ := NewConsoleVerifier(ConsoleVerifierConfig{
		JWKSURL: srv.URL, IntegrationID: testConsoleIntegrationID, HTTPClient: srv.Client(),
	})

	mutate := func(f func(map[string]any)) string {
		c := validConsoleClaims(testConsoleIntegrationID, testConsoleProjectID)
		f(c)
		return signConsoleToken(t, priv, kid, "", c)
	}

	cases := map[string]string{
		"wrong alg":              signConsoleToken(t, priv, kid, "HS256", validConsoleClaims(testConsoleIntegrationID, testConsoleProjectID)),
		"wrong issuer":           mutate(func(c map[string]any) { c["iss"] = "evil" }),
		"wrong purpose":          mutate(func(c map[string]any) { c["purpose"] = "other" }),
		"wrong audience":         mutate(func(c map[string]any) { c["aud"] = "other-integration" }),
		"integrationId mismatch": mutate(func(c map[string]any) { c["integrationId"] = "someone-else" }),
		"expired":                mutate(func(c map[string]any) { c["exp"] = time.Now().Add(-10 * time.Minute).Unix() }),
		"nbf in future":          mutate(func(c map[string]any) { c["nbf"] = time.Now().Add(10 * time.Minute).Unix() }),
		"missing exp":            mutate(func(c map[string]any) { delete(c, "exp") }),
		"missing projectId":      mutate(func(c map[string]any) { delete(c, "projectId") }),
		"bad signature":          signConsoleToken(t, otherPriv, kid, "", validConsoleClaims(testConsoleIntegrationID, testConsoleProjectID)),
		"malformed":              "not-a-jwt",
	}

	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(context.Background(), token); !errors.Is(err, ErrConsoleTokenInvalid) {
				t.Fatalf("expected ErrConsoleTokenInvalid, got %v", err)
			}
		})
	}
}

func TestConsoleVerifierSelectsKeyByKID(t *testing.T) {
	pubA, privA, _ := ed25519.GenerateKey(nil)
	pubB, privB, _ := ed25519.GenerateKey(nil)

	srv, _ := consoleJWKSServer(t, map[string]ed25519.PublicKey{"key-a": pubA, "key-b": pubB})
	defer srv.Close()

	v, _ := NewConsoleVerifier(ConsoleVerifierConfig{
		JWKSURL: srv.URL, IntegrationID: testConsoleIntegrationID, HTTPClient: srv.Client(),
	})

	// A token signed by key-b must verify against the key selected by its kid,
	// not the other published key.
	token := signConsoleToken(t, privB, "key-b", "", validConsoleClaims(testConsoleIntegrationID, testConsoleProjectID))
	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatalf("Verify key-b token: %v", err)
	}

	// A token whose header kid does not match its signing key must fail.
	bad := signConsoleToken(t, privA, "key-b", "", validConsoleClaims(testConsoleIntegrationID, testConsoleProjectID))
	if _, err := v.Verify(context.Background(), bad); !errors.Is(err, ErrConsoleTokenInvalid) {
		t.Fatalf("expected ErrConsoleTokenInvalid for kid/key mismatch, got %v", err)
	}
}

func TestConsoleVerifierRefreshesOnUnknownKID(t *testing.T) {
	pubA, privA, _ := ed25519.GenerateKey(nil)
	pubB, privB, _ := ed25519.GenerateKey(nil)

	// The server starts with only key-a; the verifier caches it on first use.
	// After rotation the server publishes both keys and a token signed by the
	// new key-b forces a refresh (its kid is not cached).
	var current atomic.Value
	docFor := func(keys map[string]ed25519.PublicKey) []byte {
		jwkList := make([]map[string]string, 0, len(keys))
		for kid, pub := range keys {
			jwkList = append(jwkList, map[string]string{
				"kty": "OKP", "crv": "Ed25519", "use": "sig", "alg": "EdDSA",
				"kid": kid, "x": base64.RawURLEncoding.EncodeToString(pub),
			})
		}
		body, _ := json.Marshal(map[string]any{"keys": jwkList})
		return body
	}
	current.Store(docFor(map[string]ed25519.PublicKey{"key-a": pubA}))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(current.Load().([]byte))
	}))
	defer srv.Close()

	v, _ := NewConsoleVerifier(ConsoleVerifierConfig{
		JWKSURL: srv.URL, IntegrationID: testConsoleIntegrationID, HTTPClient: srv.Client(),
	})

	tokenA := signConsoleToken(t, privA, "key-a", "", validConsoleClaims(testConsoleIntegrationID, testConsoleProjectID))
	if _, err := v.Verify(context.Background(), tokenA); err != nil {
		t.Fatalf("Verify key-a token: %v", err)
	}

	current.Store(docFor(map[string]ed25519.PublicKey{"key-a": pubA, "key-b": pubB}))
	tokenB := signConsoleToken(t, privB, "key-b", "", validConsoleClaims(testConsoleIntegrationID, testConsoleProjectID))
	if _, err := v.Verify(context.Background(), tokenB); err != nil {
		t.Fatalf("Verify key-b token after refresh: %v", err)
	}
}

func TestConsoleVerifierStaleCacheFallback(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	const kid = "k1"

	srv, fail := consoleJWKSServer(t, map[string]ed25519.PublicKey{kid: pub})
	defer srv.Close()

	// A tiny TTL keeps the cache perpetually stale, so every Verify tries to
	// refresh; once the endpoint fails, the still-cached key must be reused.
	v, _ := NewConsoleVerifier(ConsoleVerifierConfig{
		JWKSURL: srv.URL, IntegrationID: testConsoleIntegrationID, HTTPClient: srv.Client(), CacheTTL: time.Nanosecond,
	})

	token := signConsoleToken(t, priv, kid, "", validConsoleClaims(testConsoleIntegrationID, testConsoleProjectID))
	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatalf("Verify (warms cache): %v", err)
	}

	fail.Store(true)
	if _, err := v.Verify(context.Background(), token); err != nil {
		t.Fatalf("Verify with failing JWKS should use stale cache: %v", err)
	}
}

func TestNewConsoleVerifierRequiresIntegrationID(t *testing.T) {
	if _, err := NewConsoleVerifier(ConsoleVerifierConfig{JWKSURL: "https://x"}); err == nil {
		t.Fatal("expected error for missing IntegrationID")
	}
}

func TestNewConsoleVerifierDefaultsJWKSURL(t *testing.T) {
	if _, err := NewConsoleVerifier(ConsoleVerifierConfig{IntegrationID: testConsoleIntegrationID}); err != nil {
		t.Fatalf("empty JWKSURL should fall back to default, got %v", err)
	}
}
