package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/platform"
)

func TestAnalyticsDefinitionsUseGenericRoutesAndScopedOAuth(t *testing.T) {
	var calls int
	status := http.StatusOK
	var scope string
	c := analyticsOAuthFixture(t, crmOAuthSubject, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/oauth/token" {
			_ = r.ParseForm()
			scope = r.Form.Get("scope")
			if scope != "analytics.write" || r.Form.Get("audience") != "analytics" || r.Form.Get("projectId") != crmOAuthProject || r.Form.Get("installationId") != crmOAuthSubject {
				t.Error("incorrect token binding")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "aho_" + base64.RawURLEncoding.EncodeToString(make([]byte, 32)), "token_type": "Bearer", "expires_in": 300, "scope": scope})
			return
		}
		calls++
		if r.URL.Path != "/analytics/clients/projects/"+crmOAuthProject+"/experiments/"+crmOAuthSubject || r.Method != "PUT" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer aho_") || r.Header.Get("X-Internal-Token") != "" {
			t.Error("wrong resource request")
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body) != 1 || body["name"] != "My folder" {
			t.Error("business metadata leaked into analytics", body)
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(platform.AnalyticsDefinition{ID: crmOAuthSubject, AnalyticsDefinitionInput: platform.AnalyticsDefinitionInput{Name: "My folder"}, CreatedAt: time.Now()})
	})
	input := platform.AnalyticsDefinitionInput{Name: "My folder"}
	if _, err := c.CreateDefinition(context.Background(), crmOAuthProject, "", platform.AnalyticsFolders, crmOAuthSubject, input); err != nil {
		t.Fatal(err)
	}
	status = 503
	if _, err := c.CreateDefinition(context.Background(), crmOAuthProject, "", platform.AnalyticsFolders, crmOAuthSubject, input); err == nil || calls != 2 {
		t.Fatal("write failure hidden or retried", err, calls)
	}
	if _, err := c.CreateDefinition(context.Background(), crmOAuthIntegration, "", platform.AnalyticsFolders, crmOAuthSubject, input); err == nil || calls != 2 {
		t.Fatal("foreign project sent")
	}
}
