package integration

import (
	"crypto/ed25519"
	"crypto/rand"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
	"net/http"
	"net/http/httptest"
	"testing"
)

func analyticsOAuthFixture(t *testing.T, installation string, handler http.HandlerFunc) *AnalyticsClient {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := integrationoauth.NewProvider(integrationoauth.Config{ClientID: crmOAuthIntegration, KeyID: "key", PrivateKey: key, TokenEndpoint: server.URL + "/oauth/token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{AnalyticsURL: server.URL + "/analytics", AnalyticsOAuth: &AnalyticsOAuthConfig{Provider: provider, ProjectID: crmOAuthProject, InstallationID: installation, HTTPClient: server.Client()}})
	if err != nil {
		t.Fatal(err)
	}
	return client.Analytics
}
