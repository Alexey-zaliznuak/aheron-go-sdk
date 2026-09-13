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

const linksOAuthProject = "11111111-1111-1111-1111-111111111111"
const linksOAuthInstall = "22222222-2222-2222-2222-222222222222"
const linksOAuthID = "0123456789AbCdEf"

func linksOAuthFixture(t *testing.T, handler http.HandlerFunc) *LinksClient {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := integrationoauth.NewProvider(integrationoauth.Config{ClientID: linksOAuthInstall, KeyID: "key", PrivateKey: key, TokenEndpoint: server.URL + "/oauth/token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(Config{LinksURL: server.URL + "/api", IntegrationID: linksOAuthInstall, PrivateKey: base64.StdEncoding.EncodeToString(key), APIKey: "ahr_proj_must_not_be_sent", LinksOAuth: &LinksOAuthConfig{Provider: provider, ProjectID: linksOAuthProject, InstallationID: linksOAuthInstall, HTTPClient: server.Client()}})
	if err != nil {
		t.Fatal(err)
	}
	return c.Links.WithAPIKey("ahr_proj_other")
}
func linksOAuthIssue(t *testing.T, w http.ResponseWriter, r *http.Request, n int, scope string) {
	t.Helper()
	if err := r.ParseForm(); err != nil || r.Form.Get("audience") != "links" || r.Form.Get("projectId") != linksOAuthProject || r.Form.Get("installationId") != linksOAuthInstall || r.Form.Get("scope") != scope {
		t.Error("wrong link OAuth binding or scope")
	}
	raw := make([]byte, 32)
	raw[0] = byte(n)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "aho_" + base64.RawURLEncoding.EncodeToString(raw), "token_type": "Bearer", "expires_in": 300, "scope": scope})
}
func linksResultError[T any](_ T, err error) error { return err }

func TestLinksOAuthTypedProjectMethods(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, method, path, scope, body string
		status                          int
		call                            func(*LinksClient) error
	}{
		{"create", "POST", "", "links.write", `{"id":"` + linksOAuthID + `"}`, 201, func(c *LinksClient) error {
			return linksResultError(c.Create(ctx, linksOAuthProject, CreateLinkRequest{TargetURL: "https://example.com", Callback: &LinkCallback{EndpointKey: "endpoint", Data: json.RawMessage(`{"x":1}`)}}, "stable-key"))
		}},
		{"get", "GET", "/" + linksOAuthID, "links.read", `{"id":"` + linksOAuthID + `"}`, 200, func(c *LinksClient) error { return linksResultError(c.Get(ctx, linksOAuthProject, linksOAuthID)) }},
		{"list", "GET", "", "links.read", `{"items":[]}`, 200, func(c *LinksClient) error {
			return linksResultError(c.List(ctx, linksOAuthProject, "cursor+value", 10))
		}},
		{"disable", "DELETE", "/" + linksOAuthID, "links.write", "", 204, func(c *LinksClient) error { return c.Disable(ctx, linksOAuthProject, linksOAuthID) }},
		{"deliveries", "GET", "/" + linksOAuthID + "/deliveries", "links.read", `{"items":[]}`, 200, func(c *LinksClient) error {
			return linksResultError(c.Deliveries(ctx, linksOAuthProject, linksOAuthID, "cursor+value", 10))
		}},
		{"replay", "POST", "/" + linksOAuthID + "/deliveries/" + linksOAuthInstall + "/replay", "links.write", "", 204, func(c *LinksClient) error { return c.Replay(ctx, linksOAuthProject, linksOAuthID, linksOAuthInstall) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var issues, calls atomic.Int32
			c := linksOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/oauth/token" {
					linksOAuthIssue(t, w, r, int(issues.Add(1)), tc.scope)
					return
				}
				calls.Add(1)
				if r.Method != tc.method || r.URL.Path != "/api/projects/"+linksOAuthProject+"/links"+tc.path || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer aho_") || r.Header.Get("X-Integration-Id") != "" || r.Header.Get("X-Integration-Signature") != "" {
					t.Error("wrong resource request or legacy fallback")
				}
				if tc.name == "create" && r.Header.Get("Idempotency-Key") != "stable-key" {
					t.Error("lost creation key")
				}
				if (tc.name == "list" || tc.name == "deliveries") && (r.URL.Query().Get("cursor") != "cursor+value" || r.URL.Query().Get("limit") != "10") {
					t.Error("lost pagination")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				if tc.body != "" {
					_, _ = w.Write([]byte(tc.body))
				}
			})
			if err := tc.call(c); err != nil {
				t.Fatal(err)
			}
			if issues.Load() != 1 || calls.Load() != 1 {
				t.Fatal("unexpected retries")
			}
		})
	}
}

func TestLinksOAuthRetriesAndErrors(t *testing.T) {
	for _, name := range []string{"get401", "create401", "disable401", "replay401", "forbidden", "unavailable", "tokenError", "decodeSecret", "null", "trailing", "oversize", "redirect"} {
		t.Run(name, func(t *testing.T) {
			var issues, calls atomic.Int32
			scope := "links.read"
			if name == "create401" || name == "disable401" || name == "replay401" {
				scope = "links.write"
			}
			var firstBody string
			c := linksOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/oauth/token" {
					n := issues.Add(1)
					if name == "tokenError" {
						w.WriteHeader(503)
						_, _ = w.Write([]byte("secret"))
						return
					}
					linksOAuthIssue(t, w, r, int(n), scope)
					return
				}
				n := calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if name == "create401" {
					body, _ := io.ReadAll(r.Body)
					if r.Header.Get("Idempotency-Key") != "stable" {
						t.Error("changed creation key")
					}
					if n == 1 {
						firstBody = string(body)
					} else if firstBody != string(body) {
						t.Error("changed replay body")
					}
				}
				switch name {
				case "get401", "create401", "disable401", "replay401":
					if n == 1 {
						w.WriteHeader(401)
						return
					}
				case "forbidden":
					w.WriteHeader(403)
					_, _ = w.Write([]byte("secret"))
					return
				case "unavailable":
					w.WriteHeader(503)
					return
				case "decodeSecret":
					_, _ = w.Write([]byte(`{"createdAt":"secret"}`))
					return
				case "null":
					_, _ = w.Write([]byte(`null`))
					return
				case "trailing":
					_, _ = w.Write([]byte(`{} {}`))
					return
				case "oversize":
					_, _ = w.Write([]byte(strings.Repeat(" ", (1<<20)+1)))
					return
				case "redirect":
					w.Header().Set("Location", "/api/other")
					w.WriteHeader(307)
					return
				}
				if name == "create401" {
					w.WriteHeader(201)
				}
				_, _ = w.Write([]byte(`{"id":"` + linksOAuthID + `"}`))
			})
			var err error
			switch name {
			case "create401":
				_, err = c.Create(context.Background(), linksOAuthProject, CreateLinkRequest{TargetURL: "https://example.com"}, "stable")
			case "disable401":
				err = c.Disable(context.Background(), linksOAuthProject, linksOAuthID)
			case "replay401":
				err = c.Replay(context.Background(), linksOAuthProject, linksOAuthID, linksOAuthInstall)
			default:
				_, err = c.Get(context.Background(), linksOAuthProject, linksOAuthID)
			}
			ok := name == "get401" || name == "create401"
			wantIssues, wantCalls := int32(1), int32(1)
			if ok {
				wantIssues, wantCalls = 2, 2
			}
			if name == "tokenError" {
				wantCalls = 0
			}
			if (err == nil) != ok || err != nil && strings.Contains(err.Error(), "secret") || issues.Load() != wantIssues || calls.Load() != wantCalls {
				t.Fatalf("error=%v issues=%d calls=%d", err, issues.Load(), calls.Load())
			}
		})
	}
}

func TestLinksOAuthRejectsForeignProjectAndGlobalManagement(t *testing.T) {
	var calls atomic.Int32
	c := linksOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) })
	ctx := context.Background()
	for _, call := range []func() error{
		func() error {
			return linksResultError(c.Create(ctx, linksOAuthInstall, CreateLinkRequest{TargetURL: "https://example.com"}, "key"))
		},
		func() error { return linksResultError(c.Get(ctx, linksOAuthInstall, linksOAuthID)) },
		func() error { return linksResultError(c.List(ctx, linksOAuthInstall, "", 10)) },
		func() error { return c.Disable(ctx, linksOAuthInstall, linksOAuthID) },
		func() error { return linksResultError(c.Deliveries(ctx, linksOAuthInstall, linksOAuthID, "", 10)) },
		func() error { return c.Replay(ctx, linksOAuthInstall, linksOAuthID, linksOAuthInstall) },
		func() error {
			return linksResultError(c.RegisterCallback(ctx, "endpoint", LinkEndpointRequest{URL: "https://example.com", Enabled: true}))
		},
	} {
		if err := call(); !errors.Is(err, integrationoauth.ErrRequest) {
			t.Fatalf("error=%v", err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("project/global boundary crossed")
	}
}
