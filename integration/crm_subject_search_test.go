package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/httpclient"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/platform"
)

func TestSearchSubjectsAPIKeyContract(t *testing.T) {
	input := SearchSubjectsRequest{
		Filter: &SubjectFilter{
			Tags: &SubjectTagsFilter{Groups: []SubjectTagConditionGroup{
				{Conditions: []SubjectTagCondition{{TagID: "tag-a", Op: "has"}, {TagID: "tag-b", Op: "notHas"}}},
				{Conditions: []SubjectTagCondition{{TagID: "tag-c", Op: "has"}}},
			}},
			Variables: []SubjectVariableCondition{
				{DefinitionID: "null", Op: "eq", Value: json.RawMessage(`null`)},
				{DefinitionID: "boolean", Op: "eq", Value: json.RawMessage(`false`)},
				{DefinitionID: "number", Op: "eq", Value: json.RawMessage(`0`)},
				{DefinitionID: "array", Op: "contains", Value: json.RawMessage(`"gold"`)},
				{DefinitionID: "object", Op: "eq", Value: json.RawMessage(`{"active":true}`)},
				{DefinitionID: "missing", Op: "notExists"},
				{DefinitionID: "present", Op: "exists"},
			},
			IncludeSubjectIDs: []string{"include"}, ExcludeSubjectIDs: []string{"exclude"},
		},
		Mode: "all", Search: "Ан", Cursor: "opaque-cursor", Limit: 37,
	}
	wantBody := `{"filter":{"tags":{"groups":[{"conditions":[{"tagId":"tag-a","op":"has"},{"tagId":"tag-b","op":"notHas"}]},{"conditions":[{"tagId":"tag-c","op":"has"}]}]},"variables":[{"definitionId":"null","op":"eq","value":null},{"definitionId":"boolean","op":"eq","value":false},{"definitionId":"number","op":"eq","value":0},{"definitionId":"array","op":"contains","value":"gold"},{"definitionId":"object","op":"eq","value":{"active":true}},{"definitionId":"missing","op":"notExists"},{"definitionId":"present","op":"exists"}],"includeSubjectIds":["include"],"excludeSubjectIds":["exclude"]},"mode":"all","search":"Ан","cursor":"opaque-cursor","limit":37}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/crm/projects/"+crmOAuthProject+"/subjects/search" || r.URL.RawQuery != "" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer project-key" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("lost auth or JSON content type")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		var got, want any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Error(err)
		}
		if err := json.Unmarshal([]byte(wantBody), &want); err != nil {
			t.Error(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("body = %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":"one","displayName":"Анна","description":"VIP","createdAt":"2026-10-04T10:20:30Z","matched":true},{"id":"two","displayName":null,"description":null,"createdAt":"2026-10-03T10:20:30Z","matched":false}],"nextCursor":"next-page"}`))
	}))
	defer server.Close()
	client, err := New(Config{CRMURL: server.URL + "/api/crm", APIKey: "project-key"})
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.CRM.SearchSubjects(t.Context(), crmOAuthProject, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.NextCursor == nil || *page.NextCursor != "next-page" {
		t.Fatalf("unexpected page: %+v", page)
	}
	first, second := page.Items[0], page.Items[1]
	if first.ID != "one" || first.DisplayName == nil || *first.DisplayName != "Анна" || first.Description == nil || *first.Description != "VIP" || !first.Matched || first.CreatedAt.Format(time.RFC3339) != "2026-10-04T10:20:30Z" || second.DisplayName != nil || second.Description != nil || second.Matched {
		t.Fatalf("lost response values: %+v", page.Items)
	}
}

func TestSearchSubjectsOptionalFilterAndLegacyTags(t *testing.T) {
	for _, tc := range []struct {
		input SearchSubjectsRequest
		want  string
	}{
		{SearchSubjectsRequest{}, `{"filter":null}`},
		{SearchSubjectsRequest{Filter: &SubjectFilter{Tags: &SubjectTagsFilter{AnyOf: []string{"a"}, NoneOf: []string{"b"}}}}, `{"filter":{"tags":{"anyOf":["a"],"noneOf":["b"]}}}`},
	} {
		body, err := json.Marshal(tc.input)
		if err != nil || string(body) != tc.want {
			t.Fatalf("body=%s err=%v", body, err)
		}
	}
}

func TestSearchSubjectsAPIKeyRetryAndValidation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"nextCursor":null}`))
	}))
	defer server.Close()
	client, err := New(Config{CRMURL: server.URL, APIKey: "project-key"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "../other", crmOAuthProject + "/../" + crmOAuthSubject} {
		if _, err := client.CRM.SearchSubjects(t.Context(), id, SearchSubjectsRequest{}); err == nil {
			t.Fatalf("invalid project accepted: %q", id)
		}
	}
	if _, err := client.CRM.SearchSubjects(t.Context(), crmOAuthProject, SearchSubjectsRequest{Filter: &SubjectFilter{Variables: []SubjectVariableCondition{{Value: json.RawMessage(`{`)}}}}); err == nil {
		t.Fatal("invalid JSON reached transport")
	}
	if _, err := client.CRM.WithAPIKey("").SearchSubjects(t.Context(), crmOAuthProject, SearchSubjectsRequest{}); !errors.Is(err, errNoAPIKey) {
		t.Fatalf("missing credential: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input reached network")
	}
	page, err := client.CRM.SearchSubjects(t.Context(), crmOAuthProject, SearchSubjectsRequest{})
	if err != nil || calls.Load() != 2 || page.Items == nil || len(page.Items) != 0 || page.NextCursor != nil {
		t.Fatalf("read-only retry: page=%+v calls=%d err=%v", page, calls.Load(), err)
	}
}

func TestSearchSubjectsOAuthInventoryAndProjectBoundary(t *testing.T) {
	c := crmOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid operation reached network")
		w.WriteHeader(500)
	})
	if _, err := c.SearchSubjects(t.Context(), crmOAuthSubject, SearchSubjectsRequest{}); !errors.Is(err, integrationoauth.ErrRequest) {
		t.Fatalf("cross-project search: %v", err)
	}
	for _, req := range []httpclient.Request{
		{Method: http.MethodGet, Path: "/projects/" + crmOAuthProject + "/subjects/search"},
		{Method: http.MethodPost, Path: "/projects/" + crmOAuthProject + "/subjects/search/count"},
		{Method: http.MethodPost, Path: "/projects/" + crmOAuthProject + "/subjects/search/"},
	} {
		if _, _, err := c.oauth.operation(req); !errors.Is(err, integrationoauth.ErrRequest) {
			t.Fatalf("unclassified operation accepted: %+v err=%v", req, err)
		}
	}
}

func TestSearchSubjectsOAuthReadRetryBoundary(t *testing.T) {
	for _, status := range []int{200, 401, 403, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var mu sync.Mutex
			issues, calls := 0, 0
			var firstBody []byte
			token := ""
			c := crmOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.URL.Path == "/oauth/token" {
					issues++
					token = crmOAuthIssue(t, w, r, issues)
					if r.Form.Get("scope") != "crm.read" {
						t.Errorf("unexpected scope: %s", r.Form.Get("scope"))
					}
					return
				}
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/api/crm/projects/"+crmOAuthProject+"/subjects/search" || r.Header.Get("Authorization") != "Bearer "+token {
					t.Error("wrong resource request")
				}
				body, _ := io.ReadAll(r.Body)
				if calls == 1 {
					firstBody = body
				} else if !bytes.Equal(body, firstBody) {
					t.Error("body changed on retry")
				}
				if calls == 1 && status != 200 {
					w.WriteHeader(status)
					_, _ = w.Write([]byte("reflected-secret"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"items":[],"nextCursor":null}`))
			})
			_, err := c.WithAPIKey("must-not-be-sent").SearchSubjects(t.Context(), crmOAuthProject, SearchSubjectsRequest{Mode: "all", Cursor: "opaque"})
			mu.Lock()
			defer mu.Unlock()
			wantCalls := 1
			if status == 401 {
				wantCalls = 2
			}
			if calls != wantCalls || issues != wantCalls {
				t.Fatalf("calls=%d issues=%d want=%d", calls, issues, wantCalls)
			}
			if status == 200 || status == 401 {
				if err != nil {
					t.Fatal(err)
				}
			} else if StatusCode(err) != status || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unexpected or unsafe error: %v", err)
			}
		})
	}
}

func TestSearchSubjectsSharedUserInventory(t *testing.T) {
	const jwt = "e30.dXNlcg.c2ln"
	provider, err := platform.StaticToken(jwt, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/crm/projects/"+crmOAuthProject+"/subjects/search" || r.Header.Get("Authorization") != "Bearer "+jwt {
			t.Error("wrong user request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"nextCursor":null}`))
	}))
	defer server.Close()
	c, err := New(Config{CRMURL: server.URL + "/api/crm", CRMUser: &platform.Config{TokenProvider: provider, HTTPClient: server.Client()}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CRM.WithAPIKey("must-not-be-sent").SearchSubjects(t.Context(), crmOAuthProject, SearchSubjectsRequest{}); err != nil {
		t.Fatal(err)
	}
}
