package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
)

func applicationSDKFixture(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewTLSServer(h)
	t.Cleanup(server.Close)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := integrationoauth.NewProvider(integrationoauth.Config{ClientID: linksOAuthInstall, KeyID: "application-key", PrivateKey: key, TokenEndpoint: server.URL + "/oauth/token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(Config{IntegrationID: linksOAuthInstall, PrivateKey: base64.StdEncoding.EncodeToString(key), APIKey: "legacy-secret", CatalogURL: server.URL + "/api", LinksURL: server.URL + "/api",
		ApplicationOAuth: &ApplicationOAuthConfig{Provider: provider, HTTPClient: server.Client()},
		LinksOAuth:       &LinksOAuthConfig{Provider: provider, ProjectID: linksOAuthProject, InstallationID: linksOAuthInstall, HTTPClient: server.Client()}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestApplicationOAuthTypedManagement(t *testing.T) {
	for _, operation := range []string{"catalog", "callbacks"} {
		for _, state := range []string{"active", "refresh", "forbidden", "unavailable", "invalidResponse", "wrongProfile", "tokenFailure"} {
			t.Run(operation+"/"+state, func(t *testing.T) {
				var issues, calls atomic.Int32
				var previousToken string
				c := applicationSDKFixture(t, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/oauth/token" {
						n := issues.Add(1)
						_ = r.ParseForm()
						audience, scope := "catalog", "catalog.write"
						if operation == "callbacks" {
							audience, scope = "links", "links.callbacks.write"
						}
						if r.Form.Get("tokenKind") != "application" || r.Form.Has("projectId") || r.Form.Has("installationId") || r.Form.Get("audience") != audience || r.Form.Get("scope") != scope {
							t.Error("wrong application binding")
						}
						if state == "tokenFailure" {
							w.WriteHeader(503)
							_, _ = w.Write([]byte("upstream-secret"))
							return
						}
						raw := make([]byte, 32)
						raw[0] = byte(n)
						prefix := "aho_app_"
						if state == "wrongProfile" {
							prefix = "aho_"
						}
						w.Header().Set("Content-Type", "application/json")
						_ = json.NewEncoder(w).Encode(map[string]any{"access_token": prefix + base64.RawURLEncoding.EncodeToString(raw), "token_type": "Bearer", "expires_in": 300, "scope": scope})
						return
					}
					n := calls.Add(1)
					method, path := "POST", "/api/integrations/self/sync"
					if operation == "callbacks" {
						method, path = "PUT", "/api/integrations/self/link-callbacks/endpoint"
					}
					if r.Method != method || r.URL.Path != path || r.URL.RawQuery != "" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer aho_app_") {
						t.Error("wrong management request")
					}
					for _, h := range []string{"X-Integration-Id", "X-Integration-Timestamp", "X-Integration-Signature", "X-Api-Key", "Cookie"} {
						if _, present := r.Header[h]; present {
							t.Error("legacy credentials leaked")
						}
					}
					body, _ := io.ReadAll(r.Body)
					var payload map[string]any
					if json.Unmarshal(body, &payload) != nil {
						t.Error("invalid resource body")
					}
					if _, ok := payload["integrationId"]; ok {
						t.Error("caller supplied ownership")
					}
					if state == "refresh" && n == 1 {
						previousToken = r.Header.Get("Authorization")
						w.WriteHeader(401)
						return
					}
					if state == "refresh" && previousToken == r.Header.Get("Authorization") {
						t.Error("401 reused rejected token")
					}
					if state == "forbidden" {
						w.WriteHeader(403)
						_, _ = w.Write([]byte("upstream-secret"))
						return
					}
					if state == "unavailable" {
						w.WriteHeader(503)
						_, _ = w.Write([]byte("upstream-secret"))
						return
					}
					w.Header().Set("Content-Type", "application/json")
					if state == "invalidResponse" {
						_, _ = w.Write([]byte("{}"))
						return
					}
					if operation == "catalog" {
						_, _ = w.Write([]byte(`{"changed":false,"version":2,"published":true}`))
					} else {
						_ = json.NewEncoder(w).Encode(LinkEndpoint{IntegrationID: linksOAuthInstall, Key: "endpoint", URL: "https://integration.example/callback", Enabled: true, Version: 1})
					}
				})
				var err error
				if operation == "catalog" {
					_, err = c.Catalog.Sync(context.Background(), Manifest{})
				} else {
					_, err = c.Links.WithAPIKey("different-legacy-secret").RegisterCallback(context.Background(), "endpoint", LinkEndpointRequest{URL: "https://integration.example/callback", Enabled: true})
				}
				if state == "active" || state == "refresh" {
					if err != nil {
						t.Fatal(err)
					}
				} else if err == nil || strings.Contains(err.Error(), "upstream-secret") {
					t.Fatalf("error=%v", err)
				}
				wantIssues, wantCalls := int32(1), int32(1)
				if state == "refresh" {
					wantIssues, wantCalls = 2, 2
				}
				if state == "tokenFailure" || state == "wrongProfile" {
					wantCalls = 0
				}
				if issues.Load() != wantIssues || calls.Load() != wantCalls {
					t.Fatalf("issues=%d calls=%d", issues.Load(), calls.Load())
				}
			})
		}
	}
}

func TestApplicationOAuthCoexistsWithProjectLinks(t *testing.T) {
	var issues, calls atomic.Int32
	c := applicationSDKFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			issues.Add(1)
			_ = r.ParseForm()
			if r.Form.Has("tokenKind") || r.Form.Get("projectId") != linksOAuthProject || r.Form.Get("installationId") != linksOAuthInstall || r.Form.Get("scope") != "links.read" {
				t.Error("application token used for project operation")
			}
			linksOAuthIssue(t, w, r, 1, "links.read")
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"0123456789AbCdEf"}`))
	})
	if _, err := c.Links.Get(context.Background(), linksOAuthProject, linksOAuthID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Links.RegisterCallback(context.Background(), "../other", LinkEndpointRequest{}); !errors.Is(err, integrationoauth.ErrRequest) {
		t.Fatal("invalid callback path accepted")
	}
	if issues.Load() != 1 || calls.Load() != 1 {
		t.Fatal("unexpected I/O")
	}
}
