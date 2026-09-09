package integration

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCatalogSyncObserverReportsPublishedAndDraftResults(t *testing.T) {
	for _, tc := range []struct {
		body      string
		published bool
	}{
		{`{"changed":true,"version":7,"published":true}`, true},
		{`{"changed":false,"version":7,"published":true}`, true},
		{`{"changed":true,"version":7,"published":false,"reason":"subflow"}`, false},
	} {
		_, priv, _ := ed25519.GenerateKey(nil)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.body)) }))
		client := newCatalogTestClient(t, priv, server.URL)
		calls := 0
		client.Catalog.StartSyncWithObserver(context.Background(), Manifest{ActionPath: "/api/actions"}, func(result SyncResult) {
			calls++
			if result.Version != 7 || result.Published != tc.published {
				t.Errorf("bad result %+v", result)
			}
		})
		server.Close()
		if calls != 1 {
			t.Fatalf("observer calls %d", calls)
		}
	}
}

func TestCatalogSyncObserverDoesNotRunOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &CatalogClient{}
	client.StartSyncWithObserver(ctx, Manifest{}, func(SyncResult) { t.Error("observed unregistered version") })
}
