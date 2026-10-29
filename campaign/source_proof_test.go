package campaign

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSourceProofTransport(t *testing.T) {
	const project = "11111111-1111-1111-1111-111111111111"
	const campaign = "22222222-2222-2222-2222-222222222222"
	for _, name := range []string{"valid", "foreignProject", "foreignCampaign", "badOwner", "missing", "unknown", "null", "duplicate", "trailing", "oversize", "html", "notFound", "serverError", "redirect", "invalidID"} {
		t.Run(name, func(t *testing.T) {
			wire := map[string]any{"projectId": project, "campaignId": campaign, "name": "Campaign", "ownerIntegrationId": project, "preparationMode": "executionSnapshot", "status": "draft", "createdAt": "2026-01-01T00:00:00Z"}
			want := ErrSourceProofUnavailable
			switch name {
			case "valid":
				want = nil
			case "foreignProject":
				wire["projectId"] = campaign
			case "foreignCampaign":
				wire["campaignId"] = project
			case "badOwner":
				wire["ownerIntegrationId"] = "bad"
			case "missing":
				delete(wire, "name")
			case "unknown":
				wire["extra"] = true
			case "null":
				wire["name"] = nil
			case "notFound":
				want = ErrSourceProofNotFound
			case "invalidID":
				want = ErrSourceProofInvalidRequest
			}
			raw, _ := json.Marshal(wire)
			switch name {
			case "duplicate":
				raw = append([]byte(`{"name":"duplicate",`), raw[1:]...)
			case "trailing":
				raw = append(raw, []byte(` {}`)...)
			case "oversize":
				raw = []byte(strings.Repeat(" ", 17<<10))
			}
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" || r.URL.Path != "/internal/projects/"+project+"/campaigns/"+campaign+"/source-proof" || r.Header.Get("X-Internal-Token") != "secret" || r.Header.Get("Accept") != "application/json" {
					t.Error("unexpected proof request")
				}
				w.Header().Set("Content-Type", "application/json")
				switch name {
				case "html":
					w.Header().Set("Content-Type", "text/html")
				case "notFound":
					w.WriteHeader(404)
				case "serverError":
					w.WriteHeader(503)
				case "redirect":
					w.Header().Set("Location", "/redirect")
					w.WriteHeader(307)
				}
				_, _ = w.Write(raw)
			}))
			defer srv.Close()
			c, err := NewSourceProofClient(srv.URL, "secret", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			requestedProject := project
			if name == "invalidID" {
				requestedProject = "../foreign"
			}
			for range 2 {
				out, err := c.Get(context.Background(), requestedProject, campaign)
				if !errors.Is(err, want) {
					t.Fatalf("error %v want %v", err, want)
				}
				if want == nil && (out.ProjectID != project || out.CampaignID != campaign || out.Name != "Campaign") {
					t.Fatalf("incorrect proof: %+v", out)
				}
			}
			expected := 2
			if name == "invalidID" {
				expected = 0
			}
			if calls != expected {
				t.Fatalf("calls=%d want=%d", calls, expected)
			}
		})
	}
}

func TestSourceProofConfigurationAndCancellation(t *testing.T) {
	for _, url := range []string{"", "ftp://example.test", "https://u:p@example.test", "https://example.test?q=1", "https://example.test#fragment"} {
		if _, err := NewSourceProofClient(url, "secret", time.Second); !errors.Is(err, ErrSourceProofConfig) {
			t.Errorf("accepted URL %q", url)
		}
	}
	for _, token := range []string{"", " ", "secret\nvalue"} {
		if _, err := NewSourceProofClient("https://example.test", token, time.Second); !errors.Is(err, ErrSourceProofConfig) {
			t.Errorf("accepted invalid token")
		}
	}
	for _, timeout := range []time.Duration{0, -1, 11 * time.Second} {
		if _, err := NewSourceProofClient("https://example.test", "secret", timeout); !errors.Is(err, ErrSourceProofConfig) {
			t.Error("accepted timeout")
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("cancelled request reached server") }))
	defer srv.Close()
	c, _ := NewSourceProofClient(srv.URL, "secret", time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Get(ctx, "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222")
	if !errors.Is(err, ErrSourceProofUnavailable) {
		t.Fatal(err)
	}
}
