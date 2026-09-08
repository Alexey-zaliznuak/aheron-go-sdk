package schemetransfer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocumentedContractFixtures(t *testing.T) {
	for _, name := range []string{"copyRules", "resourceSources", "lookupRequest", "lookupResponse", "identifyResponse", "importPlan"} {
		raw, err := os.ReadFile(filepath.Join("testdata", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := Validate(name, raw); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	raw, _ := os.ReadFile(filepath.Join("testdata", "copyRules.json"))
	r, err := ParseCopyRules(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join("testdata", "resourceSources.json"))
	var sources map[string]ResourceSource
	if err := json.Unmarshal(raw, &sources); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDeclaration(sources, map[string]*CopyRules{"invoice": &r}, "resources", "validate"); err != nil {
		t.Fatal(err)
	}
}

const validRules = `{"version":1,"mode":"declared","validation":{"mode":"integration"},"settings":{"kind":"object","unknownFields":"reject","fields":{"channel":{"kind":"reference","resource":{"kind":"integrationResource","sourceKey":"channels"},"readAs":"value","writeAs":"value"},"text":{"kind":"template","dialect":"aheronVarsV1"}}}}`

func TestCopyRulesRejectInvalidOrAmbiguousContracts(t *testing.T) {
	if _, err := ParseCopyRules([]byte(validRules)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{}`, `{"version":1,"mode":"literal"}`, strings.Replace(validRules, `"version":1`, `"version":2`, 1), strings.Replace(validRules, `"fields":{`, `"resourceType":"channel","fields":{`, 1), strings.Replace(validRules, `"readAs":"value"`, `"readAs":"id"`, 1), strings.Replace(validRules, `"writeAs":"value"`, `"writeAs":"value","empty":"bindDefault"`, 1), strings.Replace(validRules, `"kind":"template"`, `"kind":"template","valueType":"string"`, 1)} {
		if _, err := ParseCopyRules([]byte(raw)); err == nil {
			t.Errorf("accepted invalid rules: %s", raw)
		}
	}
}

func TestDeclarationDependenciesAndVersionCapabilities(t *testing.T) {
	r, _ := ParseCopyRules([]byte(validRules))
	sources := map[string]ResourceSource{"channels": {ValueType: "string", IdentityScope: "project", Supports: []string{"search", "resolve"}}}
	rules := map[string]*CopyRules{"send": &r}
	if err := ValidateDeclaration(sources, rules, "https://integration/resources", "https://integration/validate"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDeclaration(sources, rules, "", "validate"); err == nil {
		t.Fatal("accepted missing lookup")
	}
	if err := ValidateDeclaration(sources, rules, "lookup", ""); err == nil {
		t.Fatal("accepted missing required validator")
	}
	if err := ValidateDeclaration(nil, rules, "lookup", "validate"); err == nil {
		t.Fatal("accepted unknown source")
	}
	s := sources["channels"]
	s.Parameters = map[string]SourceParameter{"parent": {Required: true, SourceKey: "channels"}}
	sources["channels"] = s
	if err := ValidateDeclaration(sources, rules, "lookup", "validate"); err == nil {
		t.Fatal("accepted dependency cycle")
	}
	s.Parameters = nil
	s.ConstraintsSchema = json.RawMessage(`{"$ref":"file:///secret.json"}`)
	sources["channels"] = s
	if err := ValidateDeclaration(sources, rules, "lookup", "validate"); err == nil {
		t.Fatal("accepted external schema loading")
	}
}

func TestStrictJSONDoesNotLoseNumbersOrLeakValues(t *testing.T) {
	v, err := DecodeJSON([]byte(`{"amount":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	if v.(map[string]any)["amount"] != json.Number("9007199254740993") {
		t.Fatal(v)
	}
	for _, raw := range []string{`{"token":"supersecret","token":"other"}`, `{"token":"supersecret"} []`, strings.Repeat("[", 66) + strings.Repeat("]", 66)} {
		v, err := DecodeJSON([]byte(raw))
		if err == nil || v != nil {
			t.Fatal("accepted malformed data")
		}
		if strings.Contains(err.Error(), "supersecret") {
			t.Fatal("error leaks value")
		}
	}
}

func TestLookupModesAndValidationStatus(t *testing.T) {
	for _, raw := range []string{`{"mode":"resolve","projectId":"p","integrationVersion":1,"sourceKey":"channels","ids":["c"]}`, `{"mode":"search","projectId":"p","integrationVersion":1,"sourceKey":"channels","query":"","limit":20}`, `{"mode":"identify","projectId":"p","integrationVersion":1,"sourceKey":"columns","values":[{"index":2}]}`} {
		if err := Validate("lookupRequest", []byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{`{"mode":"resolve","projectId":"p","integrationVersion":1,"sourceKey":"channels","ids":[]}`, `{"mode":"search","projectId":"p","integrationVersion":1,"sourceKey":"channels","ids":["c"]}`, `{"mode":"search","projectId":"p","integrationVersion":0,"sourceKey":"channels"}`} {
		if err := Validate("lookupRequest", []byte(raw)); err == nil {
			t.Fatal("accepted invalid lookup")
		}
	}
	r := ValidationResult{Status: "passed", Issues: []Issue{{Severity: "error", Code: "incompatibleChannel", Path: "/channel", Message: "Channel does not support this feature"}}}
	if err := r.Validate(); err == nil {
		t.Fatal("error marked as passed")
	}
	r.Status = "blocked"
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
}
