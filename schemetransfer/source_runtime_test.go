package schemetransfer

import (
	"encoding/json"
	"testing"
)

func TestSourceLookupCatalogConstraints(t *testing.T) {
	sources := map[string]ResourceSource{
		"accounts": {ValueType: "string", Supports: []string{"resolve"}},
		"channels": {ValueType: "string", Supports: []string{"search", "resolve", "identify"}, Parameters: map[string]SourceParameter{"account": {Required: true, SourceKey: "accounts"}}, ConstraintsSchema: json.RawMessage(`{"type":"object","properties":{"capabilities":{"type":"array","items":{"enum":["text"]}}},"additionalProperties":false}`)},
	}
	valid := LookupRequest{Mode: "search", ProjectID: "p", IntegrationVersion: 1, SourceKey: "channels", Parameters: map[string]json.RawMessage{"account": json.RawMessage(`"trusted"`)}, Constraints: json.RawMessage(`{"capabilities":["text"]}`)}
	if err := ValidateSourceLookup(sources, valid); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*LookupRequest){
		func(r *LookupRequest) { r.Parameters = nil },
		func(r *LookupRequest) { r.Parameters = map[string]json.RawMessage{"account": json.RawMessage(`2`)} },
		func(r *LookupRequest) { r.Parameters = map[string]json.RawMessage{"fake": json.RawMessage(`"id"`)} },
		func(r *LookupRequest) { r.Constraints = json.RawMessage(`{"capabilities":["unknown"]}`) },
		func(r *LookupRequest) { r.SourceKey = "missing" },
		func(r *LookupRequest) { r.SourceKey = "accounts" },
		func(r *LookupRequest) { r.Mode = "identify"; r.Values = []json.RawMessage{json.RawMessage(`42`)} },
	} {
		req := valid
		change(&req)
		if err := ValidateSourceLookup(sources, req); err == nil {
			t.Fatalf("accepted %+v", req)
		}
	}
	sources["channels"] = ResourceSource{ValueType: "string", Supports: []string{"search"}, ConstraintsSchema: json.RawMessage(`{"$ref":"https://example.invalid/private"}`)}
	valid.Parameters = nil
	if err := ValidateSourceLookup(sources, valid); err == nil {
		t.Fatal("external schema accepted")
	}
}

func TestResourceValueKeepsExactNumericType(t *testing.T) {
	for _, tc := range []struct {
		kind, raw string
		valid     bool
	}{
		{"integer", "9007199254740993", true}, {"integer", "1.5", false},
		{"number", "1.5", true}, {"string", "42", false}, {"string", `"id"`, true},
		{"object", `{"id":9007199254740993}`, true}, {"object", `{"id":1,"id":2}`, false},
		{"string", "null", false},
	} {
		if err := ValidateResourceValue(tc.kind, json.RawMessage(tc.raw)); (err == nil) != tc.valid {
			t.Fatalf("%s %s: %v", tc.kind, tc.raw, err)
		}
	}
}
