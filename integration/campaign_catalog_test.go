package integration

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
)

func TestCampaignCatalogOAuthAndQuery(t *testing.T) {
	calls := 0
	c := campaignOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/oauth/token" {
			_ = r.ParseForm()
			if r.Form.Get("scope") != "campaigns.read" {
				t.Error("wrong scope")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "aho_" + base64.RawURLEncoding.EncodeToString(make([]byte, 32)), "token_type": "Bearer", "expires_in": 300, "scope": "campaigns.read"})
			return
		}
		calls++
		if r.Method != "GET" || r.URL.Path != "/campaign/projects/"+crmOAuthProject+"/campaigns/page" || r.URL.Query().Get("name") != "Привет & мир" || r.URL.Query().Get("cursor") != crmOAuthSubject || r.URL.Query().Get("limit") != "2" || r.URL.Query().Get("status") != "draft" {
			t.Error("bad request", r.URL)
		}
		_, _ = w.Write([]byte(`{"items":[],"nextCursor":"` + crmOAuthSubject + `","scannedCount":2}`))
	})
	page, err := c.Campaigns.Catalog(t.Context(), crmOAuthProject, CampaignCatalogOptions{Cursor: crmOAuthSubject, Name: "Привет & мир", Status: "draft", Limit: 2})
	if err != nil || page.Items == nil || page.NextCursor != crmOAuthSubject {
		t.Fatal(page, err)
	}
	if _, err := c.Campaigns.Catalog(t.Context(), crmOAuthSubject, CampaignCatalogOptions{}); err == nil {
		t.Fatal("foreign project accepted")
	}
	if _, err := c.Campaigns.Catalog(t.Context(), crmOAuthProject, CampaignCatalogOptions{Limit: 101}); err == nil {
		t.Fatal("unbounded page")
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}
