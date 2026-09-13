package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
)

const crmOAuthProject = "11111111-1111-1111-1111-111111111111"
const crmOAuthSubject = "22222222-2222-2222-2222-222222222222"
const crmOAuthIntegration = "33333333-3333-3333-3333-333333333333"

func crmOAuthFixture(t *testing.T, handler http.HandlerFunc) *CRMClient {
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
	c, err := New(Config{CRMURL: server.URL + "/api/crm", APIKey: "ahr_proj_must_not_be_sent", CRMOAuth: &CRMOAuthConfig{Provider: provider, ProjectID: crmOAuthProject, InstallationID: crmOAuthSubject, HTTPClient: server.Client()}})
	if err != nil {
		t.Fatal(err)
	}
	return c.CRM
}

func crmOAuthIssue(t *testing.T, w http.ResponseWriter, r *http.Request, n int) string {
	t.Helper()
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	if r.Form.Get("audience") != "crm" || r.Form.Get("projectId") != crmOAuthProject || r.Form.Get("installationId") != crmOAuthSubject {
		t.Error("wrong CRM token binding")
	}
	raw := make([]byte, 32)
	raw[0] = byte(n)
	token := "aho_" + base64.RawURLEncoding.EncodeToString(raw)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 300, "scope": r.Form.Get("scope")})
	return token
}

func crmCallError[T any](_ T, err error) error { return err }

func TestCRMOAuthTypedMethods(t *testing.T) {
	ctx := context.Background()
	name := "updated"
	cases := []struct {
		name, method, path, scope, body string
		status                          int
		call                            func(*CRMClient) error
	}{
		{"upsert", "POST", "subjects/upsert", "crm.write", `{"created":true,"subject":{"id":"` + crmOAuthSubject + `","projectId":"` + crmOAuthProject + `"},"values":[]}`, 201, func(c *CRMClient) error {
			return crmCallError(c.UpsertSubject(ctx, crmOAuthProject, UpsertSubjectParams{Find: []Field{{Field: "displayName", Value: "name"}}}))
		}},
		{"upsertVariables", "POST", "subjects/upsert", "crm.write variables.write", `{"created":false,"subject":{},"values":[]}`, 200, func(c *CRMClient) error {
			return crmCallError(c.UpsertSubject(ctx, crmOAuthProject, UpsertSubjectParams{Find: []Field{{Field: "displayName", Value: "name"}}, Update: []Field{{Field: "variable", Value: 1}}}))
		}},
		{"getSubject", "GET", "subjects/" + crmOAuthSubject, "crm.read", `{"id":"` + crmOAuthSubject + `","projectId":"` + crmOAuthProject + `"}`, 200, func(c *CRMClient) error { return crmCallError(c.GetSubject(ctx, crmOAuthProject, crmOAuthSubject)) }},
		{"listValues", "GET", "subjects/" + crmOAuthSubject + "/variable-values", "crm.read", "[]", 200, func(c *CRMClient) error {
			return crmCallError(c.ListSubjectVariables(ctx, crmOAuthProject, crmOAuthSubject))
		}},
		{"setValues", "PUT", "subjects/" + crmOAuthSubject + "/variable-values", "variables.write", "[]", 200, func(c *CRMClient) error {
			return crmCallError(c.SetSubjectVariables(ctx, crmOAuthProject, crmOAuthSubject, []VariableValue{{VariableDefinitionID: crmOAuthIntegration, Value: 1}}))
		}},
		{"listDefinitions", "GET", "variable-definitions", "crm.read", "[]", 200, func(c *CRMClient) error {
			return crmCallError(c.ListVariableDefinitions(ctx, crmOAuthProject, ListVariableDefinitionsParams{OwnerType: "integration", IntegrationID: crmOAuthIntegration}))
		}},
		{"getDefinition", "GET", "variable-definitions/" + crmOAuthSubject, "crm.read", "{}", 200, func(c *CRMClient) error {
			return crmCallError(c.GetVariableDefinition(ctx, crmOAuthProject, crmOAuthSubject))
		}},
		{"createDefinition", "POST", "variable-definitions", "variables.write", "{}", 201, func(c *CRMClient) error {
			return crmCallError(c.CreateVariableDefinition(ctx, crmOAuthProject, CreateVariableDefinitionParams{Name: "Name", Key: "key", Type: "string"}))
		}},
		{"updateDefinition", "PATCH", "variable-definitions/" + crmOAuthSubject, "variables.write", "{}", 200, func(c *CRMClient) error {
			return crmCallError(c.UpdateVariableDefinition(ctx, crmOAuthProject, crmOAuthSubject, UpdateVariableDefinitionParams{Name: &name}))
		}},
		{"deleteDefinition", "DELETE", "variable-definitions/" + crmOAuthSubject, "variables.write", "", 204, func(c *CRMClient) error { return c.DeleteVariableDefinition(ctx, crmOAuthProject, crmOAuthSubject) }},
		{"ensureDefinition", "POST", "variable-definitions", "variables.write", "reflected-secret", 409, func(c *CRMClient) error {
			return c.EnsureVariableDefinition(ctx, crmOAuthProject, CreateVariableDefinitionParams{Name: "Name", Key: "key", Type: "string"})
		}},
		{"createIntegrationDefinition", "POST", "integrations/" + crmOAuthIntegration + "/variable-definitions", "variables.write", "{}", 201, func(c *CRMClient) error {
			return crmCallError(c.CreateIntegrationVariableDefinition(ctx, crmOAuthProject, crmOAuthIntegration, CreateVariableDefinitionParams{Name: "Name", Key: "key", Type: "string"}))
		}},
		{"ensureIntegrationDefinition", "POST", "integrations/" + crmOAuthIntegration + "/variable-definitions", "variables.write", "reflected-secret", 409, func(c *CRMClient) error {
			return c.EnsureIntegrationVariableDefinition(ctx, crmOAuthProject, crmOAuthIntegration, CreateVariableDefinitionParams{Name: "Name", Key: "key", Type: "string"})
		}},
		{"listTags", "GET", "tags", "crm.read", "[]", 200, func(c *CRMClient) error { return crmCallError(c.ListTags(ctx, crmOAuthProject)) }},
		{"createTag", "POST", "tags", "crm.write", "{}", 201, func(c *CRMClient) error {
			return crmCallError(c.CreateTag(ctx, crmOAuthProject, CreateTagParams{Name: "Name"}))
		}},
		{"updateTag", "PATCH", "tags/" + crmOAuthSubject, "crm.write", "{}", 200, func(c *CRMClient) error {
			return crmCallError(c.UpdateTag(ctx, crmOAuthProject, crmOAuthSubject, UpdateTagParams{Name: &name}))
		}},
		{"deleteTag", "DELETE", "tags/" + crmOAuthSubject, "crm.write", "", 204, func(c *CRMClient) error { return c.DeleteTag(ctx, crmOAuthProject, crmOAuthSubject) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			issues, calls := 0, 0
			token := ""
			c := crmOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.URL.Path == "/oauth/token" {
					issues++
					token = crmOAuthIssue(t, w, r, issues)
					if r.Form.Get("scope") != tc.scope {
						t.Errorf("scope=%s want=%s", r.Form.Get("scope"), tc.scope)
					}
					return
				}
				calls++
				if r.Method != tc.method || r.URL.Path != "/api/crm/projects/"+crmOAuthProject+"/"+tc.path || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Integration-Signature") != "" {
					t.Error("wrong resource request")
				}
				if tc.name == "listDefinitions" && (r.URL.Query().Get("integrationId") != crmOAuthIntegration || r.URL.Query().Get("ownerType") != "integration") {
					t.Error("lost filters")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				if tc.status != 204 {
					_, _ = w.Write([]byte(tc.body))
				}
			})
			// Deriving a legacy API-key copy cannot disable an explicit OAuth binding.
			if err := tc.call(c.WithAPIKey("ahr_proj_other")); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if issues != 1 || calls != 1 {
				t.Fatalf("issues=%d calls=%d", issues, calls)
			}
		})
	}
}

func TestCRMOAuthRetryBoundary(t *testing.T) {
	for _, name := range []string{"read401", "write401", "write503", "forbidden", "tokenFailure"} {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			issues, calls := 0, 0
			c := crmOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.URL.Path == "/oauth/token" {
					issues++
					if name == "tokenFailure" {
						w.WriteHeader(503)
						return
					}
					crmOAuthIssue(t, w, r, issues)
					return
				}
				calls++
				if name == "write503" {
					w.WriteHeader(503)
					return
				}
				if name == "forbidden" {
					w.WriteHeader(403)
					_, _ = w.Write([]byte("reflected-secret"))
					return
				}
				if calls == 1 {
					w.WriteHeader(401)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte("[]"))
			})
			var err error
			if strings.HasPrefix(name, "write") {
				_, err = c.SetSubjectVariables(t.Context(), crmOAuthProject, crmOAuthSubject, []VariableValue{{VariableDefinitionID: crmOAuthIntegration, Value: 1}})
			} else {
				_, err = c.ListTags(t.Context(), crmOAuthProject)
			}
			mu.Lock()
			defer mu.Unlock()
			wantCalls, wantIssues := 1, 1
			if name == "read401" {
				wantCalls, wantIssues = 2, 2
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("expected failure")
			}
			if name == "tokenFailure" {
				wantCalls = 0
			}
			if calls != wantCalls || issues != wantIssues {
				t.Fatalf("calls=%d issues=%d", calls, issues)
			}
			if name == "forbidden" && (StatusCode(err) != 403 || !IsUnauthorized(err) || strings.Contains(err.Error(), "secret")) {
				t.Fatal("unsafe error")
			}
		})
	}
}

func TestCRMOAuthBindingAndResponse(t *testing.T) {
	c := crmOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid binding reached network")
		w.WriteHeader(500)
	})
	for _, id := range []string{crmOAuthSubject, crmOAuthProject + "/../" + crmOAuthSubject} {
		if _, err := c.ListTags(t.Context(), id); !errors.Is(err, integrationoauth.ErrRequest) {
			t.Fatal(err)
		}
	}
	if _, err := c.GetSubject(t.Context(), crmOAuthProject, "../tags"); !errors.Is(err, integrationoauth.ErrRequest) {
		t.Fatal(err)
	}
	if _, err := New(Config{CRMOAuth: &CRMOAuthConfig{}}); err == nil {
		t.Fatal("invalid config fell back")
	}
	for _, raw := range []string{"null", "[]{}", strings.Repeat(" ", 1<<20) + "[]"} {
		t.Run("malformed", func(t *testing.T) {
			c := crmOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/oauth/token" {
					crmOAuthIssue(t, w, r, 1)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(raw))
			})
			if _, err := c.ListTags(t.Context(), crmOAuthProject); !errors.Is(err, errCRMOAuthResponse) {
				t.Fatal(err)
			}
		})
	}
}

func TestCRMOAuthDecodeDoesNotReflectSecrets(t *testing.T) {
	c := crmOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			crmOAuthIssue(t, w, r, 1)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"createdAt":"reflected-secret"}`))
	})
	_, err := c.GetSubject(t.Context(), crmOAuthProject, crmOAuthSubject)
	if !errors.Is(err, errCRMOAuthResponse) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe decode error: %v", err)
	}
}
