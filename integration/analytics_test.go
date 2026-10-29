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
	"testing"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/platform"
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

func TestAnalyticsOAuthScopesReplayAndStableOwnership(t *testing.T) {
	ctx := context.Background()
	for _, installation := range []string{crmOAuthSubject, "00000000-0000-4000-8000-000000000009"} {
		t.Run(installation, func(t *testing.T) {
			tokens := map[string]string{}
			var bodies []string
			var scope, method, path string
			var reply any
			unauthorized := false
			status := 200
			c := analyticsOAuthFixture(t, installation, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/oauth/token" {
					_ = r.ParseForm()
					if r.Form.Get("projectId") != crmOAuthProject || r.Form.Get("installationId") != installation || r.Form.Get("audience") != "analytics" {
						t.Error("wrong OAuth tenant/audience")
					}
					raw := make([]byte, 32)
					raw[0] = byte(len(tokens) + 1)
					token := "aho_" + base64.RawURLEncoding.EncodeToString(raw)
					tokens[token] = r.Form.Get("scope")
					_ = json.NewEncoder(w).Encode(map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 300, "scope": r.Form.Get("scope")})
					return
				}
				if r.URL.Path != path || r.Method != method || tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")] != scope || r.Header.Get("X-Internal-Token") != "" || r.Header.Get("X-Api-Key") != "" {
					t.Errorf("wrong route/authority: %s %s", r.Method, r.URL)
				}
				raw, _ := io.ReadAll(r.Body)
				bodies = append(bodies, string(raw))
				if unauthorized {
					unauthorized = false
					w.WriteHeader(401)
					return
				}
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(reply)
			})
			base := "/analytics/projects/" + crmOAuthProject + "/sources/messengers"
			template := platform.AnalyticsStageTemplate{TemplateID: crmOAuthIntegration, Name: "Stages", Revision: "9007199254740993", ParentRevision: "9007199254740992", Stages: []platform.AnalyticsTemplateStage{{Key: "received", Name: "Отправлено"}}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
			scope, method, path, reply = "analytics.sources.read", "GET", base+"/stage-templates", platform.AnalyticsStageTemplatePage{Items: []platform.AnalyticsStageTemplate{template}}
			if _, err := c.ListStageTemplates(ctx, crmOAuthProject, false, platform.AnalyticsListOptions{}); err != nil {
				t.Fatal(err)
			}
			path, reply = base+"/stage-templates/"+template.TemplateID+"/versions/"+template.Revision, template
			if got, err := c.GetStageTemplateVersion(ctx, crmOAuthProject, template.TemplateID, template.Revision); err != nil || got.Revision != template.Revision {
				t.Fatal(got, err)
			}
			scope, method, path = "analytics.sources.write", "POST", base+"/stage-templates/default"
			if _, err := c.EnsureDefaultStageTemplate(ctx, crmOAuthProject, crmOAuthSubject); err != nil {
				t.Fatal(err)
			}
			method, path, status = "GET", base+"/write-authorization", 204
			if err := c.AuthorizeStageWrite(ctx, crmOAuthProject); err != nil {
				t.Fatal(err)
			}
			status = 403
			beforeDenied := len(bodies)
			if err := c.AuthorizeStageWrite(ctx, crmOAuthProject); err == nil || len(bodies) != beforeDenied+1 {
				t.Fatal("revoked grant hidden or retried", err)
			}
			status = 200
			binding := platform.AnalyticsCampaignStageBinding{ProjectID: crmOAuthProject, IntegrationID: crmOAuthIntegration, CampaignID: crmOAuthSubject, ExperimentID: crmOAuthProject, TemplateID: template.TemplateID, TemplateRevision: template.Revision, StageSetID: crmOAuthIntegration, Stages: []platform.AnalyticsBoundStage{{Key: "received", StageID: crmOAuthSubject}}, Revision: "1", SourceGeneration: "1", MigrationState: "native", CreatedAt: time.Now()}
			path = base + "/installations/" + installation + "/campaigns/" + crmOAuthSubject + "/binding"
			method, reply, unauthorized = "PUT", binding, true
			input := platform.AnalyticsCreateCampaignStageBinding{OperationID: crmOAuthProject, ExpectedRevision: "0", TemplateID: template.TemplateID, TemplateRevision: template.Revision}
			got, err := c.CreateCampaignStageBinding(ctx, crmOAuthProject, crmOAuthSubject, input)
			if err != nil || got.ExperimentID != binding.ExperimentID {
				t.Fatalf("installation changed stable binding: %#v %v", got, err)
			}
			if bodies[len(bodies)-1] != bodies[len(bodies)-2] {
				t.Fatal("401 retry changed frozen body")
			}
			scope, method = "analytics.sources.read", "GET"
			if _, err := c.GetCampaignStageBinding(ctx, crmOAuthProject, crmOAuthSubject); err != nil {
				t.Fatal(err)
			}
			before := len(bodies)
			if _, err := c.GetCampaignStageBinding(ctx, crmOAuthIntegration, crmOAuthSubject); !errors.Is(err, integrationoauth.ErrRequest) || len(bodies) != before {
				t.Fatal("cross-project network access", err)
			}
			scope, method, status = "analytics.sources.write", "PUT", 503
			if _, err := c.CreateCampaignStageBinding(ctx, crmOAuthProject, crmOAuthSubject, input); err == nil || len(bodies) != before+1 {
				t.Fatal("ambiguous write retried or hidden", err)
			}
		})
	}
}

func TestAnalyticsDoesNotFallBackToProjectKey(t *testing.T) {
	client, err := New(Config{APIKey: "not-a-source-credential"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Analytics.GetCampaignStageBinding(context.Background(), crmOAuthProject, crmOAuthSubject); !errors.Is(err, integrationoauth.ErrRequest) {
		t.Fatal("missing installation OAuth accepted", err)
	}
}
