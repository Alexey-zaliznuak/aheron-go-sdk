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

const executionOAuthProject = "22222222-2222-2222-2222-222222222222"
const executionOAuthInstallation = "33333333-3333-3333-3333-333333333333"

func executionOAuthFixture(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := integrationoauth.NewProvider(integrationoauth.Config{ClientID: "11111111-1111-1111-1111-111111111111", KeyID: "key-1", PrivateKey: key, TokenEndpoint: server.URL + "/oauth/token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{ExecutionURL: server.URL + "/api/execution", ExecutionOAuth: &ExecutionOAuthConfig{Provider: provider, ProjectID: executionOAuthProject, InstallationID: executionOAuthInstallation, HTTPClient: server.Client()}})
	if err != nil {
		t.Fatal(err)
	}
	return client, server
}

func executionOAuthIssue(t *testing.T, w http.ResponseWriter, r *http.Request, n int) string {
	t.Helper()
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	if r.Form.Get("audience") != "execution" || r.Form.Get("projectId") != executionOAuthProject || r.Form.Get("installationId") != executionOAuthInstallation {
		t.Error("wrong token binding")
	}
	raw := make([]byte, 32)
	raw[0] = byte(n)
	token := "aho_" + base64.RawURLEncoding.EncodeToString(raw)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 300, "scope": r.Form.Get("scope")})
	return token
}

func TestExecutionOAuthTypedMethods(t *testing.T) {
	var mu sync.Mutex
	tokens := map[string]string{}
	calls := map[string]int{}
	issues := 0
	metadataResolveSeen := false
	c, _ := executionOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/oauth/token" {
			issues++
			token := executionOAuthIssue(t, w, r, issues)
			tokens[token] = r.Form.Get("scope")
			return
		}
		for _, header := range []string{"X-Integration-Id", "X-Integration-Timestamp", "X-Integration-Signature", "X-Api-Key", "Cookie"} {
			if r.Header.Get(header) != "" {
				t.Error("legacy credential on OAuth call")
			}
		}
		scope, ok := tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if !ok {
			t.Error("no issued bearer")
		}
		calls[r.URL.Path]++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/execution/integrations/resolve":
			var body resolveBody
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Error("bad resolve body")
			}
			expected := "integration.resolve"
			if len(body.Variables) != 0 {
				expected += " variables.write"
			}
			if scope != expected {
				t.Errorf("resolve scope=%s", scope)
			}
			if len(body.Metadata) > 0 {
				metadataResolveSeen = string(body.Metadata) == `{"startEvent":{"id":"new"}}`
			}
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"status":"accepted"}`))
		case "/api/execution/integrations/triggers/activate":
			if scope != "triggers.activate" {
				t.Error("wrong activate scope")
			}
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"executionContextIds":["ctx"]}`))
		case "/api/execution/integrations/triggers":
			if scope != "triggers.activate" || r.URL.Query().Get("projectId") != executionOAuthProject || r.URL.Query().Get("blockKey") != "block" {
				t.Error("wrong list binding")
			}
			_, _ = w.Write([]byte(`{"configVersion":7,"triggers":[]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	ctx := context.Background()
	ec := ExecutionContext{ID: "context", StepID: "step", Version: 4, ProjectID: executionOAuthProject}
	if err := c.Steps.Resolve(ctx, ec, "ok", nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Steps.Resolve(ctx, ec, "ok", map[string]any{"value": 1}); err != nil {
		t.Fatal(err)
	}
	if err := c.Steps.ReactivateWithOptions(ctx, ec, "ok", nil, ResolveOptions{IdempotencyKey: "stable"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Steps.ResolveWithOptions(ctx, ec, "ok", nil, ResolveOptions{Metadata: json.RawMessage(`{"startEvent":{"id":"new"}}`)}); err != nil {
		t.Fatal(err)
	}
	ids, err := c.Triggers.Activate(ctx, ActivateParams{ProjectID: executionOAuthProject, SubjectID: "subject", ActivationKey: "go"})
	if err != nil || len(ids) != 1 {
		t.Fatalf("activate: %v", err)
	}
	listing, err := c.Triggers.ListTriggers(ctx, executionOAuthProject, "block")
	if err != nil || listing.ConfigVersion != 7 {
		t.Fatalf("list: %v", err)
	}
	if _, err = c.Triggers.List(ctx, executionOAuthProject, "block"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if issues != 3 || calls["/api/execution/integrations/resolve"] != 4 || calls["/api/execution/integrations/triggers"] != 2 || !metadataResolveSeen {
		t.Fatalf("cache/scopes requests issues=%d calls=%v", issues, calls)
	}
}

func TestExecutionOAuthTriggerEventAndSuppression(t *testing.T) {
	var gotEvent ActivationEvent
	var gotMetadata json.RawMessage
	var gotIdempotencyKey string
	c, _ := executionOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			executionOAuthIssue(t, w, r, 1)
			return
		}
		if r.URL.Path != "/api/execution/integrations/triggers/activate" {
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body activateBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode activation: %v", err)
		}
		if body.Event == nil {
			t.Error("activation event missing")
		} else {
			gotEvent = *body.Event
		}
		gotMetadata = body.Metadata
		gotIdempotencyKey = body.IdempotencyKey
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"executionContextIds":[],"matchedTriggers":1,"skippedOnce":1}`))
	})
	result, err := c.Triggers.ActivateDetailed(t.Context(), ActivateParams{
		ProjectID: executionOAuthProject, SubjectID: "subject", ActivationKey: "comment",
		Event: &ActivationEvent{EventID: "instagram:1", Kind: "messengers.postComment", Version: 1, Data: json.RawMessage(`{"commentId":"1"}`)},
		Metadata: json.RawMessage(`{"startEvent":{"id":"instagram:1"}}`), IdempotencyKey: "instagram:1",
	})
	if err != nil {
		t.Fatalf("activate detailed: %v", err)
	}
	if result.MatchedTriggers != 1 || result.SkippedOnce != 1 || len(result.ExecutionContextIDs) != 0 {
		t.Fatalf("suppression result = %+v", result)
	}
	if gotEvent.EventID != "instagram:1" || string(gotEvent.Data) != `{"commentId":"1"}` {
		t.Fatalf("activation event = %+v", gotEvent)
	}
	if string(gotMetadata) != `{"startEvent":{"id":"instagram:1"}}` || gotIdempotencyKey != "instagram:1" {
		t.Fatalf("metadata/idempotency key = %s / %q", gotMetadata, gotIdempotencyKey)
	}
}

func TestExecutionOAuthRetriesAndNoFallback(t *testing.T) {
	for _, name := range []string{"list401", "activate401", "resolve401", "keyed401", "reactivate401", "forbidden", "unavailable", "tokenRejected"} {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			issues, calls := 0, 0
			var bodies []string
			c, _ := executionOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.URL.Path == "/oauth/token" {
					issues++
					if name == "tokenRejected" {
						w.WriteHeader(401)
						return
					}
					executionOAuthIssue(t, w, r, issues)
					return
				}
				calls++
				if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer aho_") || r.Header.Get("X-Integration-Signature") != "" {
					t.Error("not OAuth")
				}
				var raw json.RawMessage
				if r.Method == "POST" {
					_ = json.NewDecoder(r.Body).Decode(&raw)
					bodies = append(bodies, string(raw))
				}
				if name == "forbidden" {
					w.WriteHeader(403)
					_, _ = w.Write([]byte("reflected-secret"))
					return
				}
				if name == "unavailable" {
					w.WriteHeader(503)
					return
				}
				if calls == 1 {
					w.WriteHeader(401)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if r.Method == "GET" {
					_, _ = w.Write([]byte(`{"configVersion":1,"triggers":[]}`))
				} else {
					w.WriteHeader(202)
				}
			})
			ctx := context.Background()
			ec := ExecutionContext{ID: "context", StepID: "step"}
			var err error
			switch name {
			case "list401", "forbidden", "unavailable", "tokenRejected":
				_, err = c.Triggers.ListTriggers(ctx, executionOAuthProject, "block")
			case "activate401":
				_, err = c.Triggers.Activate(ctx, ActivateParams{ProjectID: executionOAuthProject, SubjectID: "subject", ActivationKey: "go"})
			case "resolve401":
				err = c.Steps.Resolve(ctx, ec, "ok", nil)
			case "keyed401":
				err = c.Steps.ResolveWithOptions(ctx, ec, "ok", nil, ResolveOptions{IdempotencyKey: "stable"})
			case "reactivate401":
				err = c.Steps.Reactivate(ctx, ec, "ok", nil)
			}
			mu.Lock()
			defer mu.Unlock()
			expectedCalls, expectedIssues := 1, 1
			switch name {
			case "list401", "keyed401":
				expectedCalls, expectedIssues = 2, 2
				if err != nil {
					t.Fatal(err)
				}
			case "tokenRejected":
				expectedCalls = 0
				if err == nil {
					t.Fatal("missing token failure")
				}
			default:
				if err == nil {
					t.Fatal("missing resource failure")
				}
			}
			if calls != expectedCalls || issues != expectedIssues {
				t.Fatalf("calls=%d issues=%d", calls, issues)
			}
			if len(bodies) == 2 && bodies[0] != bodies[1] {
				t.Fatal("retry body changed")
			}
			if name == "forbidden" && (StatusCode(err) != 403 || !IsUnauthorized(err) || strings.Contains(err.Error(), "secret")) {
				t.Fatal("unsafe error classification")
			}
		})
	}
}

func TestExecutionOAuthProjectBinding(t *testing.T) {
	c, _ := executionOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("foreign project reached network")
		w.WriteHeader(500)
	})
	_, err := c.Triggers.ListTriggers(t.Context(), executionOAuthInstallation, "block")
	if !errors.Is(err, integrationoauth.ErrRequest) {
		t.Fatal(err)
	}
	_, err = c.Triggers.Activate(t.Context(), ActivateParams{ProjectID: executionOAuthInstallation, SubjectID: "subject", ActivationKey: "go"})
	if !errors.Is(err, integrationoauth.ErrRequest) {
		t.Fatal(err)
	}
	err = c.Steps.Resolve(t.Context(), ExecutionContext{ID: "context", ProjectID: executionOAuthInstallation}, "ok", nil)
	if !errors.Is(err, integrationoauth.ErrRequest) {
		t.Fatal(err)
	}
	if _, err = New(Config{ExecutionOAuth: &ExecutionOAuthConfig{}}); err == nil {
		t.Fatal("invalid OAuth config fell back to legacy")
	}
}

func TestExecutionOAuthMalformedResourceResponse(t *testing.T) {
	for _, name := range []string{"empty", "null", "missingArray", "negativeVersion", "oversize", "contentType", "trailing"} {
		t.Run(name, func(t *testing.T) {
			c, _ := executionOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/oauth/token" {
					executionOAuthIssue(t, w, r, 1)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				raw := `{"configVersion":1,"triggers":[]}`
				switch name {
				case "empty":
					raw = ""
				case "null":
					raw = "null"
				case "missingArray":
					raw = "{}"
				case "negativeVersion":
					raw = `{"configVersion":-1,"triggers":[]}`
				case "oversize":
					raw += strings.Repeat(" ", 1<<20)
				case "contentType":
					w.Header().Set("Content-Type", "text/html")
				case "trailing":
					raw += "{}"
				}
				_, _ = w.Write([]byte(raw))
			})
			if _, err := c.Triggers.ListTriggers(t.Context(), executionOAuthProject, "block"); !errors.Is(err, errExecutionOAuthResponse) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
