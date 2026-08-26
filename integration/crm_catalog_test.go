package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVariableDefinitionCRUD(t *testing.T) {
	type request struct {
		method string
		path   string
		body   map[string]any
	}
	var requests []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer project-key" {
			t.Errorf("Authorization = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		if len(body) > 0 {
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Errorf("decode request body: %v", err)
			}
		}
		requests = append(requests, request{method: r.Method, path: r.URL.Path, body: decoded})
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			_, _ = w.Write([]byte(`{"id":"definition-1","projectId":"project-1","name":"Tier","key":"tier","type":"string","ownerType":"project","createdAt":"2026-01-01T00:00:00Z"}`))
		}
	}))
	defer srv.Close()

	client, err := New(Config{CRMURL: srv.URL, APIKey: "project-key"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	name := "Customer tier"
	key := "customer_tier"
	if _, err := client.CRM.GetVariableDefinition(context.Background(), "project-1", "definition-1"); err != nil {
		t.Fatalf("GetVariableDefinition: %v", err)
	}
	if _, err := client.CRM.UpdateVariableDefinition(context.Background(), "project-1", "definition-1", UpdateVariableDefinitionParams{
		Name: &name, Key: &key, DefaultValue: "basic",
	}); err != nil {
		t.Fatalf("UpdateVariableDefinition: %v", err)
	}
	if err := client.CRM.DeleteVariableDefinition(context.Background(), "project-1", "definition-1"); err != nil {
		t.Fatalf("DeleteVariableDefinition: %v", err)
	}

	wantMethods := []string{http.MethodGet, http.MethodPatch, http.MethodDelete}
	for i, want := range wantMethods {
		if requests[i].method != want || requests[i].path != "/projects/project-1/variable-definitions/definition-1" {
			t.Errorf("request[%d] = %s %s", i, requests[i].method, requests[i].path)
		}
	}
	if requests[1].body["name"] != name || requests[1].body["key"] != key || requests[1].body["defaultValue"] != "basic" {
		t.Errorf("patch body = %#v", requests[1].body)
	}
}

func TestTagCRUD(t *testing.T) {
	type request struct {
		method string
		path   string
	}
	var requests []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, request{method: r.Method, path: r.URL.Path})
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[{"id":"tag-1","projectId":"project-1","name":"VIP","createdAt":"2026-01-01T00:00:00Z"}]`))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			_, _ = w.Write([]byte(`{"id":"tag-1","projectId":"project-1","name":"VIP","createdAt":"2026-01-01T00:00:00Z"}`))
		}
	}))
	defer srv.Close()

	client, err := New(Config{CRMURL: srv.URL, APIKey: "project-key"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if tags, err := client.CRM.ListTags(context.Background(), "project-1"); err != nil || len(tags) != 1 {
		t.Fatalf("ListTags = %v, %v", tags, err)
	}
	description := "High-value customer"
	if _, err := client.CRM.CreateTag(context.Background(), "project-1", CreateTagParams{Name: "VIP", Description: &description}); err != nil {
		t.Fatalf("CreateTag: %v", err)
	}
	name := "Priority"
	if _, err := client.CRM.UpdateTag(context.Background(), "project-1", "tag-1", UpdateTagParams{Name: &name}); err != nil {
		t.Fatalf("UpdateTag: %v", err)
	}
	if err := client.CRM.DeleteTag(context.Background(), "project-1", "tag-1"); err != nil {
		t.Fatalf("DeleteTag: %v", err)
	}

	want := []request{
		{method: http.MethodGet, path: "/projects/project-1/tags"},
		{method: http.MethodPost, path: "/projects/project-1/tags"},
		{method: http.MethodPatch, path: "/projects/project-1/tags/tag-1"},
		{method: http.MethodDelete, path: "/projects/project-1/tags/tag-1"},
	}
	if len(requests) != len(want) {
		t.Fatalf("requests = %v", requests)
	}
	for i := range want {
		if requests[i] != want[i] {
			t.Errorf("request[%d] = %+v, want %+v", i, requests[i], want[i])
		}
	}
}
