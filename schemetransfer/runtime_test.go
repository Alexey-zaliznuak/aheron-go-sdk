package schemetransfer

import (
	"encoding/json"
	"testing"
)

func TestLookupRuntimeRejectsInvalidRelationships(t *testing.T) {
	request := LookupRequest{Mode: "resolve", ProjectID: "p", IntegrationVersion: 1, SourceKey: "channels", IDs: []string{"a", "b"}}
	item := ResourceItem{ID: "a", Value: json.RawMessage(`"trusted-value"`), Title: "Channel", Selectable: true}
	for name, response := range map[string]LookupResponse{
		"foreign identity":        {Items: []ResourceItem{{ID: "c", Value: json.RawMessage(`1`), Title: "Foreign", Selectable: true}}},
		"duplicate":               {Items: []ResourceItem{item, item}},
		"resolve cursor":          {Items: []ResourceItem{item}, NextCursor: "next"},
		"unexplained unavailable": {Items: []ResourceItem{{ID: "a", Value: json.RawMessage(`1`), Title: "Channel"}}},
		"null items":              {},
	} {
		t.Run(name, func(t *testing.T) {
			if err := response.ValidateFor(request); err == nil {
				t.Fatal("accepted invalid response")
			}
		})
	}
	for _, response := range []LookupResponse{
		{Items: []ResourceItem{}}, // Missing resources are not replaced.
		{Items: []ResourceItem{item}},
		{Items: []ResourceItem{{ID: "b", Value: json.RawMessage(`1`), Title: "Disabled", Reason: "Channel disconnected"}}},
	} {
		if err := response.ValidateFor(request); err != nil {
			t.Fatal(err)
		}
	}
	request.Mode, request.IDs, request.Limit = "search", nil, 1
	if err := (LookupResponse{Items: []ResourceItem{item, {ID: "b", Value: json.RawMessage(`2`), Title: "B", Selectable: true}}}).ValidateFor(request); err == nil {
		t.Fatal("ignored search limit")
	}
}

func TestIdentifyRuntimeRequiresCompleteIndexedResponse(t *testing.T) {
	req := LookupRequest{Mode: "identify", ProjectID: "p", IntegrationVersion: 1, SourceKey: "sheets", Values: []json.RawMessage{json.RawMessage(`{"id":1}`), json.RawMessage(`{"id":2}`)}}
	for _, matches := range [][]IdentifyMatch{
		{}, {{InputIndex: 0, Status: "missing"}},
		{{InputIndex: 0, Status: "missing"}, {InputIndex: 0, Status: "ambiguous"}},
		{{InputIndex: 0, Status: "missing"}, {InputIndex: 2, Status: "missing"}},
		{{InputIndex: 0, Status: "found"}, {InputIndex: 1, Status: "missing"}},
	} {
		if err := (IdentifyResponse{Matches: matches}).ValidateFor(req); err == nil {
			t.Fatalf("accepted %+v", matches)
		}
	}
	if err := (IdentifyResponse{Matches: []IdentifyMatch{{InputIndex: 1, Status: "ambiguous"}, {InputIndex: 0, Status: "missing"}}}).ValidateFor(req); err != nil {
		t.Fatal(err)
	}
}

func TestCallbackRuntimeValidatesOriginalBytes(t *testing.T) {
	for _, raw := range []string{
		`{"mode":"resolve","projectId":"p","integrationVersion":1,"sourceKey":"s","ids":[]}`,
		`{"mode":"search","projectId":"p","integrationVersion":1,"sourceKey":"s","ids":[]}`,
		`{"mode":"search","projectId":"p","projectId":"q","integrationVersion":1,"sourceKey":"s"}`,
		`{"mode":"identify","projectId":"p","integrationVersion":1,"sourceKey":"s","values":[]}`,
		`{"mode":"search","projectId":"p","integrationVersion":1,"sourceKey":"s","limit":0}`,
	} {
		if err := ValidateCallbackRequest(ResourceValues, []byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	req := []byte(`{"protocolVersion":1,"projectId":"p","integrationVersion":1,"blockKey":"send","settings":{}}`)
	for _, raw := range []string{
		`{"status":"passed","issues":[{"severity":"error","code":"invalid","path":"/channel","message":"Unavailable"}]}`,
		`{"status":"passed","status":"blocked","issues":[]}`,
		`{"status":"passed","issues":[]} {}`,
	} {
		if err := ValidateCallbackResponse(ValidateCopySettings, req, []byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if err := ValidateCallbackResponse(ValidateCopySettings, req, []byte(`{"status":"blocked","issues":[{"severity":"error","code":"invalid","path":"/channel","message":"Unavailable"}]}`)); err != nil {
		t.Fatal(err)
	}
}
