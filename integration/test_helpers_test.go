package integration

import (
	"crypto/ed25519"
	"net/http"
	"testing"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/sign"
)

func signLifecycleInbound(t *testing.T, key ed25519.PrivateKey, body []byte) http.Header {
	t.Helper()
	h := signInbound(t, key, "lifecycle", body)
	h.Set(sign.HeaderPlatformSignature, sign.SignDomain(key, LifecycleProtocol, h.Get(sign.HeaderPlatformTimestamp), body))
	return h
}

func newCatalogTestClient(t *testing.T, privateKey ed25519.PrivateKey, baseURL string, httpClient *http.Client) *Client {
	t.Helper()
	provider, err := integrationoauth.NewProvider(integrationoauth.Config{
		ClientID:      "11111111-1111-4111-8111-111111111111",
		KeyID:         "catalog-test-key",
		PrivateKey:    privateKey,
		TokenEndpoint: baseURL + "/oauth/token",
		HTTPClient:    httpClient,
	})
	if err != nil {
		t.Fatalf("new catalog provider: %v", err)
	}
	client, err := New(Config{
		IntegrationID: "11111111-1111-4111-8111-111111111111",
		CatalogURL:    baseURL + "/api",
		PublicBaseURL: "https://integration.example",
		ApplicationOAuth: &ApplicationOAuthConfig{
			Provider: provider, HTTPClient: httpClient,
		},
	})
	if err != nil {
		t.Fatalf("new catalog client: %v", err)
	}
	return client
}
