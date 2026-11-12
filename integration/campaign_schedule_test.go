package integration

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
)

func TestCampaignScheduleContract(t *testing.T) {
	at := time.Date(2030, 5, 6, 12, 34, 0, 0, time.UTC)
	const revision uint64 = 9007199254740993
	calls := 0
	c := campaignOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/oauth/token" {
			_ = r.ParseForm()
			if r.Form.Get("scope") != "campaigns.write" || r.Form.Get("audience") != "campaign" {
				t.Error("incorrect scheduling authorization", r.Form)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "aho_" + base64.RawURLEncoding.EncodeToString(make([]byte, 32)), "token_type": "Bearer", "expires_in": 300, "scope": r.Form.Get("scope")})
			return
		}
		calls++
		if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/campaign/projects/"+crmOAuthProject+"/campaigns/"+crmOAuthSubject+"/") {
			t.Fatal("unexpected scheduling request", r.Method, r.URL.Path)
		}
		var body struct {
			ExpectedRevision uint64    `json:"expectedRevision"`
			ScheduledAt      time.Time `json:"scheduledAt"`
			TimeZone         string    `json:"timeZone"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.ExpectedRevision != revision {
			t.Fatal("revision precision lost", body.ExpectedRevision)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/schedule"):
			if !body.ScheduledAt.Equal(at) || body.TimeZone != "Europe/Moscow" {
				t.Fatal("schedule changed", body)
			}
			_ = json.NewEncoder(w).Encode(Campaign{ID: crmOAuthSubject, ProjectID: crmOAuthProject, Status: "scheduled", Revision: revision + 1, ScheduledAt: &at, TimeZone: body.TimeZone})
		case strings.HasSuffix(r.URL.Path, "/unschedule"):
			if !body.ScheduledAt.IsZero() || body.TimeZone != "" {
				t.Fatal("unschedule supplied schedule", body)
			}
			_ = json.NewEncoder(w).Encode(Campaign{ID: crmOAuthSubject, ProjectID: crmOAuthProject, Status: "draft", Revision: revision + 1})
		default:
			t.Fatal("unexpected scheduling path", r.URL.Path)
		}
	})
	input := ScheduleCampaignParams{ExpectedRevision: revision, ScheduledAt: at, TimeZone: "Europe/Moscow"}
	got, err := c.Campaigns.Schedule(t.Context(), crmOAuthProject, crmOAuthSubject, input)
	if err != nil || got.Status != "scheduled" || got.ScheduledAt == nil || !got.ScheduledAt.Equal(at) || got.TimeZone != input.TimeZone {
		t.Fatalf("scheduled response: %+v, %v", got, err)
	}
	got, err = c.Campaigns.Unschedule(t.Context(), crmOAuthProject, crmOAuthSubject, revision)
	if err != nil || got.Status != "draft" || got.ScheduledAt != nil {
		t.Fatalf("draft response: %+v, %v", got, err)
	}
	if _, err = c.Campaigns.Schedule(t.Context(), crmOAuthSubject, crmOAuthSubject, input); !errors.Is(err, integrationoauth.ErrRequest) {
		t.Fatal("cross-project scheduling", err)
	}
	if _, err = c.Campaigns.Unschedule(t.Context(), crmOAuthProject, "../other", revision); !errors.Is(err, integrationoauth.ErrRequest) {
		t.Fatal("invalid campaign accepted", err)
	}
	if calls != 2 {
		t.Fatal("invalid request made network call", calls)
	}
}
