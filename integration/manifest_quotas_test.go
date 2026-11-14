package integration

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestManifestQuotaDeclarations(t *testing.T) {
	m := Manifest{Quotas: []Quota{{SystemName: "execution.runs", Name: "Runs", UnitName: "Executions", BillingStep: 1000}}}
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

func TestCatalogQuotaLookupByIDAndName(t *testing.T) {
	const id = "12345678-1234-4321-9876-123456789abc"
	_, priv, _ := ed25519.GenerateKey(nil)
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/oauth/token" {
			_, _ = w.Write([]byte(`{"access_token":"aho_app_` + base64.RawURLEncoding.EncodeToString(make([]byte, 32)) + `","token_type":"Bearer","expires_in":300}`))
			return
		}
		calls++
		if r.Method != "GET" || r.URL.Path != "/api/integrations/self/quotas" {
			t.Errorf("unexpected lookup %s %s", r.Method, r.URL.String())
		}
		if calls == 1 && r.URL.Query().Get("id") != id || calls == 2 && r.URL.Query().Get("systemName") != "execution.runs" {
			t.Error("lookup selector missing")
		}
		_, _ = w.Write([]byte(`{"id":"` + id + `","systemName":"runs","purchasePriceMinor":12345}`))
	}))
	defer server.Close()
	client := newCatalogTestClient(t, priv, server.URL, server.Client())
	q, err := client.Catalog.Quota(context.Background(), id)
	if err != nil || q.ID != id || q.PurchasePriceMinor == nil || *q.PurchasePriceMinor != 12345 {
		t.Fatalf("lookup %+v %v", q, err)
	}
	q, err = client.Catalog.QuotaByName(context.Background(), "execution.runs")
	if err != nil || q.ID != id {
		t.Fatalf("name lookup %+v %v", q, err)
	}
	if _, err = client.Catalog.Quota(context.Background(), "bad"); err == nil || calls != 2 {
		t.Fatal("invalid lookup sent")
	}
}
