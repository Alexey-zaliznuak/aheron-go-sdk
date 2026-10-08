package docs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTimeoutOptOutDoesNotImmediatelyCancelReads(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) }))
	defer s.Close()
	original := &http.Client{Timeout: time.Nanosecond}
	cfg := Config{BaseURL: s.URL, AllowLoopbackHTTP: true, HTTPClient: original}
	c, err := New(cfg)
	if err != nil || c.timeout != 10*time.Second {
		t.Fatalf("legacy default changed: %v", err)
	}
	cfg.DisableTimeout = true
	c, err = New(cfg)
	if err != nil || c.timeout != 0 || c.http.Timeout != 0 {
		t.Fatalf("timeout not removed: %v", err)
	}
	var out any
	if err := c.request(t.Context(), "GET", "/test", nil, &out, false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.request(ctx, "GET", "/test", nil, &out, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if original.Timeout != time.Nanosecond {
		t.Fatal("caller transport mutated")
	}
}
