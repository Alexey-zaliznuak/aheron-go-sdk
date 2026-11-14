package platform

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBillingSubscriptionCommands(t *testing.T) {
	id := uuid.NewString()
	operation := uuid.NewString()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer "+testToken("user") {
			t.Error("user credential missing")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case "GET":
			if r.URL.Path != "/api/billing/projects/"+id {
				t.Error(r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"subscription":{"projectId":"` + id + `"},"quotas":[{"remaining":"9007199254740991.000001"}]}`))
		case "PUT":
			var in ChangeProjectPlan
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.OperationID != operation || in.Revision != 7 {
				t.Error("lost command identity")
			}
			_, _ = w.Write([]byte(`{"projectId":"` + id + `","revision":8}`))
		default:
			t.Error(r.Method)
		}
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL + "/api", TokenProvider: provider(t, testToken("user")), AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Billing.Project(context.Background(), id)
	if err != nil || got.Quotas[0].Remaining != "9007199254740991.000001" {
		t.Fatal(got, err)
	}
	command := ChangeProjectPlan{ProjectID: id, PlanID: uuid.NewString(), Revision: 7, OperationID: operation}
	if _, err = client.Billing.ChangePlan(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Billing.Project(context.Background(), "../other"); !errors.Is(err, ErrInvalidInput) || calls != 2 {
		t.Fatal("invalid identity sent", err)
	}
}
