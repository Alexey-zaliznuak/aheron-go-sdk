package integration

import (
	"encoding/json"
	"testing"
)

func TestManifestQuotaDeclarations(t *testing.T) {
	m := Manifest{Quotas: []Quota{{SystemName: "runs", Name: "Runs", UnitName: "Executions", BillingStep: 1000}}}
	body, err := m.resolve("https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Quotas []Quota `json:"quotas"`
	}
	if err = json.Unmarshal(data, &wire); err != nil || len(wire.Quotas) != 1 || wire.Quotas[0].BillingStep != 1000 {
		t.Fatalf("wire %s %v", data, err)
	}
	m.Quotas = append(m.Quotas, m.Quotas[0])
	if _, err = m.resolve("https://example.com"); err == nil {
		t.Fatal("duplicate quota accepted")
	}
}
