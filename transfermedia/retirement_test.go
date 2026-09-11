package transfermedia

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSnapshotRetirementUsesOneStableBodylessDelete(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if r.Method != "DELETE" || r.URL.Path != "/internal/template-file-snapshots/"+operationID || len(body) != 0 || r.Header.Get("X-Internal-Token") != "token" {
			t.Error("invalid retirement request")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client, _ := New(server.URL, "token", server.Client())
	for range 2 {
		if err := client.RetireSnapshot(context.Background(), operationID); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("transport retried or cached command")
	}
	for _, id := range []string{"", projectID + "/assets/" + assetID, "../x", "00000000-0000-0000-0000-000000000000"} {
		if err := client.RetireSnapshot(context.Background(), id); !errors.Is(err, ErrInvalidRequest) {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("invalid address reached HTTP")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.RetireSnapshot(ctx, operationID); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("cancelled request reached HTTP")
	}
}

func TestRetirementRequiresExplicitAcknowledgementAndNeverRedirects(t *testing.T) {
	var foreignCalls atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { foreignCalls.Add(1) }))
	defer foreign.Close()
	for _, status := range []int{200, 202, 302, 307, 404, 409, 500} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", foreign.URL)
			w.WriteHeader(status)
			_, _ = io.WriteString(w, "private-response")
		}))
		client, _ := New(server.URL, "token", server.Client())
		err := client.RetireSnapshot(context.Background(), operationID)
		want := ErrUnavailable
		if status == 404 {
			want = ErrNotFound
		}
		if status == 409 {
			want = ErrConflict
		}
		if !errors.Is(err, want) || strings.Contains(err.Error(), "private") {
			t.Fatal(status, err)
		}
		server.Close()
	}
	if foreignCalls.Load() != 0 {
		t.Fatal("credential followed redirect")
	}
	// A 204 must not count as a successful capture/read.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	client, _ := New(server.URL, "token", server.Client())
	if _, err := client.GetAsset(context.Background(), operationID, assetID); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}
