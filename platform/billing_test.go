package platform

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBillingCatalog(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer "+testToken("user") {
			t.Error("missing user credential or wrong method")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/billing/plans":
			_, _ = w.Write([]byte(`[{"id":"plan","quotas":[{"quotaId":"quota","includedUnits":"9007199254740990.000001"}]}]`))
		case "/api/billing/quotas":
			if calls == 3 && r.URL.Query().Get("integrationId") != projectID {
				t.Error("missing integration selector")
			}
			_, _ = w.Write([]byte(`[{"id":"quota","systemName":"execution.runs","integrationId":null,"purchasePriceMinor":null,"billingStep":1000}]`))
		default:
			t.Error(r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer s.Close()
	c, err := New(Config{BaseURL: s.URL + "/api", TokenProvider: provider(t, testToken("user")), AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	plans, err := c.Billing.Plans(context.Background())
	if err != nil || plans[0].Quotas[0].IncludedUnits != "9007199254740990.000001" {
		t.Fatal(plans, err)
	}
	q, err := c.Billing.Quotas(context.Background(), "")
	if err != nil || q[0].PurchasePriceMinor != nil || q[0].IntegrationID != nil {
		t.Fatal(q, err)
	}
	if _, err := c.Billing.Quotas(context.Background(), projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Billing.Quotas(context.Background(), "../other"); !errors.Is(err, ErrInvalidInput) || calls != 3 {
		t.Fatal("invalid selector sent", err)
	}
}
