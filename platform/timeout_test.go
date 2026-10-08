package platform

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestExplicitTimeoutOptOutKeepsCancellationAndLegacyDefault(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { jsonReply(w, []Project{}) }))
	defer s.Close()
	original := &http.Client{Timeout: time.Nanosecond}
	cfg := Config{BaseURL: s.URL + "/api", TokenProvider: provider(t, testToken("user")), HTTPClient: original, AllowLoopbackHTTP: true}
	c, err := New(cfg)
	if err != nil || c.http.Timeout != 30*time.Second {
		t.Fatalf("legacy default changed: %v", err)
	}
	cfg.DisableTimeout = true
	c, err = New(cfg)
	if err != nil || c.http.Timeout != 0 {
		t.Fatalf("timeout still enabled: %v", err)
	}
	if _, err := c.Projects.List(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.Projects.List(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if original.Timeout != time.Nanosecond {
		t.Fatal("caller transport mutated")
	}
}
