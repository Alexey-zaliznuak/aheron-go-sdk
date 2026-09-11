package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/sign"
)

func signLifecycleInbound(t *testing.T, key ed25519.PrivateKey, body []byte) http.Header {
	t.Helper()
	h := signInbound(t, key, "lifecycle", body)
	h.Set(sign.HeaderPlatformSignature, sign.SignDomain(key, LifecycleProtocol, h.Get(sign.HeaderPlatformTimestamp), body))
	return h
}

func TestLifecycleVerifiedHTTP(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	jwks := platformJWKS("lifecycle", pub)
	t.Cleanup(jwks.Close)
	v, _ := NewVerifier(VerifierConfig{JWKSURL: jwks.URL, HTTPClient: jwks.Client()})
	var mu sync.Mutex
	state := LifecycleState{}
	storedKey, writes, failure := "", 0, false
	snapshot := func() (LifecycleState, string, int) {
		mu.Lock()
		defer mu.Unlock()
		return state, storedKey, writes
	}
	setFailure := func(value bool) { mu.Lock(); defer mu.Unlock(); failure = value }
	handler, err := v.HandleLifecycle(lifecycleTestIntegration, func(_ context.Context, req LifecycleRequest) (LifecycleReceipt, error) {
		mu.Lock()
		defer mu.Unlock()
		decision, err := DecideLifecycle(state, req)
		if err != nil {
			return LifecycleReceipt{}, err
		}
		if failure {
			return LifecycleReceipt{}, errors.New("database secret-detail")
		}
		if decision.Receipt.Outcome == LifecycleApplied {
			state, storedKey = decision.State, req.ProjectAPIKey
			writes++
		}
		return decision.Receipt, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	sender, err := NewLifecycleSender(LifecycleSenderConfig{PrivateKey: priv, KeyID: "lifecycle", HTTPClient: server.Client(), AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	install := lifecycleTestRequest(1, LifecycleInstall, lifecycleTestFirst)
	install.ProjectAPIKey = "secret-key"
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := sender.Deliver(context.Background(), server.URL, install)
			failures <- err
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, key, count := snapshot(); count != 1 || key != install.ProjectAPIKey {
		t.Fatal("concurrent retries applied twice")
	}
	changed := install
	changed.ProjectAPIKey = "different-key"
	if _, err := sender.Deliver(context.Background(), server.URL, changed); !errors.Is(err, ErrLifecycleDelivery) {
		t.Fatalf("conflict acknowledged: %v", err)
	}
	uninstall := lifecycleTestRequest(2, LifecycleUninstall, lifecycleTestFirst)
	setFailure(true)
	if _, err := sender.Deliver(context.Background(), server.URL, uninstall); !errors.Is(err, ErrLifecycleDelivery) {
		t.Fatalf("failed commit acknowledged: %v", err)
	}
	if current, key, count := snapshot(); current.Sequence != 1 || count != 1 || key != install.ProjectAPIKey {
		t.Fatal("failed commit changed state")
	}
	setFailure(false)
	if _, err := sender.Deliver(context.Background(), server.URL, uninstall); err != nil {
		t.Fatal(err)
	}
	if current, key, _ := snapshot(); current.Sequence != 2 || key != "" {
		t.Fatal("uninstall did not retain tombstone and clear credential")
	}
	newInstall := lifecycleTestRequest(3, LifecycleInstall, lifecycleTestNext)
	newInstall.ProjectAPIKey = "new-key"
	if _, err := sender.Deliver(context.Background(), server.URL, newInstall); err != nil {
		t.Fatal(err)
	}
	if ack, err := sender.Deliver(context.Background(), server.URL, uninstall); err != nil || ack.Outcome != LifecycleSuperseded {
		t.Fatal("late uninstall damaged reinstall", err)
	}
	if _, key, _ := snapshot(); key != "new-key" {
		t.Fatal("late uninstall cleared replacement key")
	}

	raw, _ := json.Marshal(install)
	call := func(method string, body []byte, signed bool, want int) {
		t.Helper()
		r := httptest.NewRequest(method, "/lifecycle", bytes.NewReader(body))
		if signed {
			r.Header = signLifecycleInbound(t, priv, body)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "secret") {
			t.Fatal("credential or storage error escaped")
		}
	}
	for _, body := range []string{
		`{}`, `null`, string(raw) + `{}`,
		strings.Replace(string(raw), `"sequence":1`, `"sequence":null`, 1),
		strings.Replace(string(raw), `"sequence":1`, `"sequence":1.5`, 1),
		strings.Replace(string(raw), `"sequence":1`, `"Sequence":1`, 1),
		strings.Replace(string(raw), `"sequence":1`, `"sequence":1,"sequence":2`, 1),
		strings.Replace(string(raw), `"projectApiKey":"secret-key"`, `"projectApiKey":null`, 1),
		strings.Replace(string(raw), `"projectApiKey":"secret-key"`, `"projectApiKey":""`, 1),
		strings.Replace(string(raw), `"action":"install"`, `"action":"install","unknown":true`, 1),
		strings.Replace(string(raw), `"eventId":`, `"EventId":`, 1),
	} {
		call(http.MethodPost, []byte(body), true, 400)
	}
	wrong := install
	wrong.IntegrationID = lifecycleTestProject
	wrongRaw, _ := json.Marshal(wrong)
	call(http.MethodPost, wrongRaw, true, 403)
	call(http.MethodPost, raw, false, 401)
	call(http.MethodGet, raw, true, 405)
	call(http.MethodPost, append(raw, bytes.Repeat([]byte(" "), maxLifecycleBody)...), true, 401)
	if _, err := v.HandleLifecycle("bad", func(context.Context, LifecycleRequest) (LifecycleReceipt, error) { return LifecycleReceipt{}, nil }); err == nil {
		t.Fatal("unbound recipient handler accepted")
	}
	badHandler, _ := v.HandleLifecycle(lifecycleTestIntegration, func(context.Context, LifecycleRequest) (LifecycleReceipt, error) { return LifecycleReceipt{}, nil })
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/lifecycle", bytes.NewReader(raw))
	r.Header = signLifecycleInbound(t, priv, raw)
	badHandler.ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatal("empty receipt acknowledged")
	}
	// A signed action with author-controlled JSON must not become a lifecycle command.
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/lifecycle", bytes.NewReader(raw))
	r.Header = signInbound(t, priv, "lifecycle", raw)
	handler.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("legacy signature authorized lifecycle")
	}
	// A lifecycle message must not enter a legacy destructive handler either.
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/uninstall", bytes.NewReader(raw))
	r.Header = signLifecycleInbound(t, priv, raw)
	v.HandleUninstall(func(context.Context, UninstallRequest) error { t.Fatal("lifecycle fell through to legacy"); return nil }).ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("lifecycle signature accepted by legacy")
	}
}

func TestLifecycleSenderRequiresExactReceipt(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	req := lifecycleTestRequest(1, LifecycleInstall, lifecycleTestFirst)
	req.ProjectAPIKey = "request-secret"
	decision, _ := DecideLifecycle(LifecycleState{}, req)
	valid, _ := json.Marshal(decision.Receipt)
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"legacy empty success", 200, ""}, {"queued only", 202, string(valid)},
		{"credential in error", 500, "request-secret"},
		{"duplicate receipt field", 200, strings.Replace(string(valid), `"sequence":1`, `"sequence":1,"sequence":2`, 1)},
		{"unknown receipt field", 200, string(valid[:len(valid)-1]) + `,"secret":"x"}`},
		{"different event", 200, strings.Replace(string(valid), req.EventID, lifecycleTestNext, 1)},
		{"extra body", 200, string(valid) + `{}`}, {"oversized", 200, string(valid) + strings.Repeat(" ", maxLifecycleBody)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			sender, _ := NewLifecycleSender(LifecycleSenderConfig{PrivateKey: priv, KeyID: "key", AllowHTTP: true})
			ack, err := sender.Deliver(context.Background(), srv.URL, req)
			if !errors.Is(err, ErrLifecycleDelivery) || ack != (LifecycleReceipt{}) || strings.Contains(err.Error(), "request-secret") {
				t.Fatalf("bad response accepted/leaked: %+v %v", ack, err)
			}
		})
	}
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	original := source.Client()
	sender, _ := NewLifecycleSender(LifecycleSenderConfig{PrivateKey: priv, KeyID: "key", HTTPClient: original, AllowHTTP: true})
	if _, err := sender.Deliver(context.Background(), source.URL, req); !errors.Is(err, ErrLifecycleDelivery) || hits.Load() != 0 || original.CheckRedirect != nil {
		t.Fatalf("redirect sent credential or mutated client: %d %v", hits.Load(), err)
	}
	secure, _ := NewLifecycleSender(LifecycleSenderConfig{PrivateKey: priv, KeyID: "key"})
	for _, address := range []string{source.URL, "https://user:password@example.com/lifecycle", "https://example.com/lifecycle#fragment", "file:///lifecycle"} {
		if _, err := secure.Deliver(context.Background(), address, req); !errors.Is(err, ErrLifecycleInvalid) {
			t.Fatalf("invalid destination accepted: %v", err)
		}
	}
}
