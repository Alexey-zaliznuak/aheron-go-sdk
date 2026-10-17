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
	"testing"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
)

func campaignOAuthFixture(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewTLSServer(h)
	t.Cleanup(server.Close)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := integrationoauth.NewProvider(integrationoauth.Config{ClientID: crmOAuthIntegration, KeyID: "key", PrivateKey: key, TokenEndpoint: server.URL + "/oauth/token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(Config{CampaignURL: server.URL + "/campaign", BackendURL: server.URL + "/api", CRMURL: server.URL + "/crm", CampaignOAuth: &CampaignOAuthConfig{Provider: provider, ProjectID: crmOAuthProject, InstallationID: crmOAuthSubject, HTTPClient: server.Client()}, CRMOAuth: &CRMOAuthConfig{Provider: provider, ProjectID: crmOAuthProject, InstallationID: crmOAuthSubject, HTTPClient: server.Client(), BackendURL: server.URL + "/api"}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCampaignOAuthScopesAndIsolation(t *testing.T) {
	tokens := map[string]string{}
	calls := 0
	c := campaignOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/oauth/token" {
			_ = r.ParseForm()
			if r.Form.Get("projectId") != crmOAuthProject || r.Form.Get("installationId") != crmOAuthSubject {
				t.Error("incorrect tenant binding")
			}
			raw := make([]byte, 32)
			raw[0] = byte(len(tokens) + 1)
			token := "aho_" + base64.RawURLEncoding.EncodeToString(raw)
			tokens[token] = r.Form.Get("audience") + ":" + r.Form.Get("scope")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 300, "scope": r.Form.Get("scope")})
			return
		}
		calls++
		scope := tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		switch {
		case strings.HasSuffix(r.URL.Path, "/named-triggers"):
			if scope != "backend:campaigns.read" {
				t.Error(scope)
			}
			_, _ = w.Write([]byte(`{"triggers":[]}`))
		case strings.Contains(r.URL.Path, "/variable-values/"):
			if scope != "backend:crm.read" {
				t.Error(scope)
			}
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`{"sources":{"channel_access":{"type":"remote"}}}`))
			} else {
				var input struct {
					Limit  int      `json:"limit"`
					Values []string `json:"values"`
				}
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if input.Limit == 0 && input.Values == nil {
					t.Error("empty resolve was changed into search")
				}
				_, _ = w.Write([]byte(`{"items":[]}`))
			}
		case r.Method == http.MethodGet:
			if scope != "campaign:campaigns.read" {
				t.Error(scope)
			}
			_, _ = w.Write([]byte(`{"campaigns":[]}`))
		default:
			if scope != "campaign:campaigns.write" {
				t.Error(scope)
			}
			var input map[string]any
			_ = json.NewDecoder(r.Body).Decode(&input)
			if input["commandId"] != crmOAuthSubject {
				t.Error("lost idempotency command")
			}
			for _, field := range []string{"filter", "requiredFilter", "metadata"} {
				if value, present := input[field]; present && value == nil {
					t.Errorf("optional %s must be omitted, not null", field)
				}
			}
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":"` + crmOAuthSubject + `","projectId":"` + crmOAuthProject + `"}`))
		}
	})
	ctx := context.Background()
	if _, err := c.Campaigns.List(ctx, crmOAuthProject); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Campaigns.Create(ctx, crmOAuthProject, CreateCampaignParams{CommandID: crmOAuthSubject, Name: "test", Filter: json.RawMessage(`{}`), RequiredFilter: json.RawMessage(`{}`), Metadata: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Campaigns.Create(ctx, crmOAuthProject, CreateCampaignParams{CommandID: crmOAuthSubject, Name: "minimal", ActivationKey: "send"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Campaigns.ListNamedTriggers(ctx, crmOAuthProject); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CRM.ListVariableValueSources(ctx, crmOAuthProject, crmOAuthIntegration); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CRM.LookupVariableValues(ctx, crmOAuthProject, crmOAuthIntegration, "channel_access", VariableValuesLookupParams{Limit: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CRM.LookupVariableValues(ctx, crmOAuthProject, crmOAuthIntegration, "channel_access", VariableValuesLookupParams{Values: []string{}}); err != nil {
		t.Fatal(err)
	}
	before := calls
	if _, err := c.Campaigns.List(ctx, crmOAuthSubject); !errors.Is(err, integrationoauth.ErrRequest) {
		t.Fatal("cross-project request accepted", err)
	}
	if _, err := c.CRM.LookupVariableValues(ctx, crmOAuthProject, crmOAuthIntegration, "../../token", VariableValuesLookupParams{}); !errors.Is(err, integrationoauth.ErrRequest) {
		t.Fatal("path injection accepted", err)
	}
	if calls != before {
		t.Fatal("invalid request performed network I/O")
	}
}

func TestCampaignOnlyCreationReplaysUnauthorizedWrite(t *testing.T) {
	for _, operation := range []string{"create", "update", "launch", "cancel"} {
		t.Run(operation, func(t *testing.T) {
			issues, calls := 0, 0
			c := campaignOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/oauth/token" {
					issues++
					_ = r.ParseForm()
					raw := make([]byte, 32)
					raw[0] = byte(issues)
					_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "aho_" + base64.RawURLEncoding.EncodeToString(raw), "token_type": "Bearer", "expires_in": 300, "scope": r.Form.Get("scope")})
					return
				}
				calls++
				w.WriteHeader(401)
			})
			ctx := context.Background()
			switch operation {
			case "create":
				_, _ = c.Campaigns.Create(ctx, crmOAuthProject, CreateCampaignParams{CommandID: crmOAuthSubject})
			case "update":
				_, _ = c.Campaigns.Update(ctx, crmOAuthProject, crmOAuthSubject, UpdateCampaignParams{})
			case "launch":
				_, _ = c.Campaigns.Launch(ctx, crmOAuthProject, crmOAuthSubject, 1)
			case "cancel":
				_, _ = c.Campaigns.Cancel(ctx, crmOAuthProject, crmOAuthSubject)
			}
			want := 1
			if operation == "create" {
				want = 2
			}
			if calls != want || issues != want {
				t.Fatalf("calls=%d issues=%d want=%d", calls, issues, want)
			}
		})
	}
}
