package integration

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestMCPDelegationIsBoundAndRejectsOtherPurposes(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(nil)
	server, _ := consoleJWKSServer(t, map[string]ed25519.PublicKey{"mcp": pub})
	defer server.Close()
	verifier, err := NewMCPVerifier(MCPVerifierConfig{IntegrationID: testConsoleIntegrationID, JWKSURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	claims := MCPClaims{Issuer: MCPTokenIssuer, Audience: testConsoleIntegrationID, Purpose: MCPTokenPurpose,
		ActorID: "33333333-3333-4333-8333-333333333333", ProjectID: testConsoleProjectID, IntegrationID: testConsoleIntegrationID,
		InstallationID: "44444444-4444-4444-8444-444444444444", AccessVersion: 4, IntegrationAccessVersion: 2,
		Permissions: []string{MCPPermissionRead}, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Minute).Unix()}
	token, err := SignMCPToken(claims, key, "mcp", now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := verifier.Verify(context.Background(), token)
	if err != nil || verified.ActorID != claims.ActorID || verified.InstallationID != claims.InstallationID || verified.HasPermission(MCPPermissionWrite) {
		t.Fatalf("delegation changed: %+v %v", verified, err)
	}
	for _, mutate := range []func(map[string]any){
		func(c map[string]any) { c["purpose"] = "integration-console" },
		func(c map[string]any) { c["aud"] = testConsoleProjectID },
		func(c map[string]any) { c["exp"] = now.Add(-time.Second).Unix() },
		func(c map[string]any) { c["exp"] = now.Add(time.Hour).Unix() },
		func(c map[string]any) { delete(c, "installationId") },
		func(c map[string]any) { c["accessVersion"] = 0 },
	} {
		b, _ := json.Marshal(claims)
		var c map[string]any
		_ = json.Unmarshal(b, &c)
		mutate(c)
		if _, err := verifier.Verify(context.Background(), signConsoleToken(t, key, "mcp", "EdDSA", c)); !errors.Is(err, ErrMCPTokenInvalid) {
			t.Fatalf("accepted invalid delegation: %v", err)
		}
	}
	console, _ := NewConsoleVerifier(ConsoleVerifierConfig{IntegrationID: testConsoleIntegrationID, JWKSURL: server.URL})
	if _, err := console.Verify(context.Background(), token); !errors.Is(err, ErrConsoleTokenInvalid) {
		t.Fatal("MCP token accepted by console")
	}
}
