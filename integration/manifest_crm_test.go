package integration

import (
	"encoding/json"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/crmdefinitions"
	"strings"
	"testing"
)

func TestManifestCRMVariableDefinitions(t *testing.T) {
	m := Manifest{CRMVariableDefinitions: crmdefinitions.Set{"member": {Name: "Member", Type: "boolean"}}}
	b, err := m.resolve("https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(b)
	if err != nil || !strings.Contains(string(raw), `"crmVariableDefinitions":{"member":{"name":"Member","type":"boolean"}}`) {
		t.Fatalf("manifest: %s, %v", raw, err)
	}
	m.CRMVariableDefinitions["member"] = crmdefinitions.Definition{Name: "Member", Type: "invalid"}
	if _, err = m.resolve("https://example.com"); err == nil {
		t.Fatal("invalid type accepted")
	}
}
