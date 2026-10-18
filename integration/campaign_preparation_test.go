package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCampaignPreparationContractAndNoLegacyFallback(t *testing.T) {
	var paths []string
	created := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	c := campaignOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/oauth/token" {
			_ = r.ParseForm()
			if r.Form.Get("audience") != "campaign" || r.Form.Get("scope") != "campaigns.write" {
				t.Error("wrong authorization")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "aho_" + base64.RawURLEncoding.EncodeToString(make([]byte, 32)), "token_type": "Bearer", "expires_in": 300, "scope": r.Form.Get("scope")})
			return
		}
		paths = append(paths, r.URL.Path)
		if r.Method != http.MethodPost {
			t.Error(r.Method)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/prepare"):
			var body map[string]uint64
			if json.NewDecoder(r.Body).Decode(&body) != nil || body["expectedRevision"] != 7 {
				t.Error("lost draft revision")
			}
			w.WriteHeader(http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "/start-prepared"):
			var body PreparedAudience
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.ExpectedRecipients != 100 || !body.CampaignCreatedAt.Equal(created) {
				t.Error("lost audience identity")
			}
			_, _ = w.Write([]byte(`{"status":"running"}`))
		default:
			t.Error("unexpected fallback", r.URL.Path)
		}
	})
	if _, err := c.Campaigns.Prepare(context.Background(), crmOAuthProject, crmOAuthSubject, 7); StatusCode(err) != 404 {
		t.Fatal(err)
	}
	if len(paths) != 1 {
		t.Fatal("prepare fell back to an unguarded launch", paths)
	}
	if _, err := c.Campaigns.StartPrepared(context.Background(), crmOAuthProject, crmOAuthSubject, PreparedAudience{CampaignCreatedAt: created, ExpectedRecipients: 100}); err != nil {
		t.Fatal(err)
	}
}
