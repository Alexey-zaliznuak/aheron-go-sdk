package integrationoauth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const clientID = "7bca4a39-b830-4eb1-99dd-845f5b996200"

func tokenRequest() Request {
	return Request{ProjectID: "7bca4a39-b830-4eb1-99dd-845f5b996201", InstallationID: "7bca4a39-b830-4eb1-99dd-845f5b996202", Audience: "crm", Scopes: []string{"crm.write", "crm.read"}}
}

func bearer(n int) string {
	key := make([]byte, 32)
	key[0], key[1] = byte(n), byte(n>>8)
	return "aho_" + base64.RawURLEncoding.EncodeToString(key)
}

func tokenResponse(w http.ResponseWriter, n int, scope string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": bearer(n), "token_type": "Bearer", "expires_in": 300, "scope": scope})
}

func fixture(t *testing.T, handler http.HandlerFunc) (*Provider, *httptest.Server, ed25519.PrivateKey) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewProvider(Config{ClientID: clientID, KeyID: "key-1", PrivateKey: private, TokenEndpoint: server.URL + "/oauth/token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return p, server, private
}

func TestProviderWireContractAndKeyCopy(t *testing.T) {
	var public ed25519.PublicKey
	var endpoint string
	var assertions []string
	var mu sync.Mutex
	p, server, private := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/oauth/token" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Error("wrong token request")
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if len(r.Form) != 8 || r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_id") != clientID || r.Form.Get("client_assertion_type") != "urn:ietf:params:oauth:client-assertion-type:jwt-bearer" || r.Form.Get("projectId") != tokenRequest().ProjectID || r.Form.Get("installationId") != tokenRequest().InstallationID || r.Form.Get("audience") != "crm" || r.Form.Get("scope") != "crm.read crm.write" {
			t.Error("wrong form fields")
		}
		parts := strings.Split(r.Form.Get("client_assertion"), ".")
		if len(parts) != 3 {
			t.Error("wrong JWT")
			w.WriteHeader(400)
			return
		}
		h, _ := base64.RawURLEncoding.DecodeString(parts[0])
		payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		if !ed25519.Verify(public, []byte(parts[0]+"."+parts[1]), sig) {
			t.Error("invalid assertion signature")
		}
		var header map[string]string
		var claims map[string]json.RawMessage
		if json.Unmarshal(h, &header) != nil || len(header) != 3 || header["kid"] != "key-1" || header["typ"] != "JWT" || header["alg"] != "EdDSA" || json.Unmarshal(payload, &claims) != nil || len(claims) != 6 {
			t.Error("invalid assertion objects")
		}
		for name, expected := range map[string]string{"iss": clientID, "sub": clientID, "aud": endpoint} {
			var got string
			if json.Unmarshal(claims[name], &got) != nil || got != expected {
				t.Errorf("wrong %s", name)
			}
		}
		var issued, expires int64
		var jti string
		if json.Unmarshal(claims["iat"], &issued) != nil || json.Unmarshal(claims["exp"], &expires) != nil || expires-issued != 60 || issued > time.Now().Unix() || issued < time.Now().Add(-5*time.Second).Unix() || json.Unmarshal(claims["jti"], &jti) != nil || len(jti) != 43 {
			t.Error("invalid temporal/replay claims")
		}
		mu.Lock()
		assertions = append(assertions, jti)
		n := len(assertions)
		mu.Unlock()
		tokenResponse(w, n, r.Form.Get("scope"))
	})
	public = slices.Clone(private.Public().(ed25519.PublicKey))
	endpoint = server.URL + "/oauth/token"
	clear(private) // Provider must own its copy of the signing key.
	req := tokenRequest()
	before := time.Now()
	a, err := p.Token(t.Context(), req)
	if err != nil || a.Bearer() != bearer(1) || a.Scope() != "crm.read crm.write" || a.ExpiresAt().Before(before.Add(299*time.Second)) || a.ExpiresAt().After(time.Now().Add(300*time.Second)) {
		t.Fatal("token contract", a, err)
	}
	p.Invalidate(req, a)
	b, err := p.Token(t.Context(), req)
	if err != nil || a.Bearer() == b.Bearer() || len(assertions) != 2 || assertions[0] == assertions[1] {
		t.Fatal("assertion/token reuse", err)
	}
	if !slices.Equal(req.Scopes, []string{"crm.write", "crm.read"}) {
		t.Fatal("caller scopes mutated")
	}
	for _, text := range []string{fmt.Sprintf("%v %#v", a, a), fmt.Sprintf("%v %#v", p, p), fmt.Sprintf("%v %#v", Config{PrivateKey: p.privateKey}, Config{PrivateKey: p.privateKey})} {
		if strings.Contains(text, a.Bearer()) || !strings.Contains(text, "redacted") {
			t.Fatal("unsafe fmt output")
		}
	}
}

func TestProviderCacheIsolationExpiryAndCapacity(t *testing.T) {
	var calls atomic.Int32
	p, _, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		tokenResponse(w, int(calls.Add(1)), r.Form.Get("scope"))
	})
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	p.now = func() time.Time { return time.Unix(0, clock.Load()) }
	req := tokenRequest()
	a, err := p.Token(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	reversed := req
	reversed.Scopes = []string{"crm.read", "crm.write"}
	b, _ := p.Token(t.Context(), reversed)
	if b.Bearer() != a.Bearer() || calls.Load() != 1 {
		t.Fatal("scope order misses cache")
	}
	for _, change := range []func(*Request){
		func(r *Request) { r.ProjectID = "7bca4a39-b830-4eb1-99dd-845f5b996203" },
		func(r *Request) { r.InstallationID = "7bca4a39-b830-4eb1-99dd-845f5b996204" },
		func(r *Request) { r.Audience = "another-resource" },
		func(r *Request) { r.Scopes = []string{"crm.read"} },
	} {
		other := req
		change(&other)
		token, err := p.Token(t.Context(), other)
		if err != nil || token.Bearer() == a.Bearer() {
			t.Fatal("cache boundary", err)
		}
	}
	key, _ := requestKey(req)
	p.mu.Lock()
	refresh := p.cache[key].refreshAt
	p.mu.Unlock()
	margin := a.ExpiresAt().Sub(refresh)
	if margin < 30*time.Second || margin > 45*time.Second {
		t.Fatal("missing early refresh/jitter", margin)
	}
	clock.Store(refresh.UnixNano())
	c, err := p.Token(t.Context(), req)
	if err != nil || c.Bearer() == a.Bearer() {
		t.Fatal("not refreshed", err)
	}
	p.Invalidate(req, a)
	d, _ := p.Token(t.Context(), req)
	if d.Bearer() != c.Bearer() {
		t.Fatal("late 401 evicted new token")
	}
	p.maxEntries = 5
	for n := 10; n < 30; n++ {
		other := req
		other.InstallationID = fmt.Sprintf("7bca4a39-b830-4eb1-99dd-%012d", n)
		if _, err := p.Token(t.Context(), other); err != nil {
			t.Fatal(err)
		}
	}
	p.mu.Lock()
	count := len(p.cache)
	p.mu.Unlock()
	if count > 5 {
		t.Fatal("unbounded cache", count)
	}
}

func waitFor(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not complete")
}

func TestProviderSingleFlightAndCancellation(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	p, _, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		tokenResponse(w, 1, "crm.read crm.write")
	})
	defer close(release)
	firstCtx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() { _, err := p.Token(firstCtx, tokenRequest()); first <- err }()
	<-started
	const waiters = 20
	results := make(chan error, waiters)
	for n := 0; n < waiters; n++ {
		go func() { _, err := p.Token(t.Context(), tokenRequest()); results <- err }()
	}
	key, _ := requestKey(tokenRequest())
	waitFor(t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.flights[key] != nil && p.flights[key].waiters == waiters+1
	})
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// Release without closing twice at cleanup.
	release <- struct{}{}
	for n := 0; n < waiters; n++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate acquisitions", calls.Load())
	}
}

func TestProviderAllWaitersCancelAndBoundFlights(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	p, _, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		close(started)
		<-r.Context().Done()
		close(cancelled)
	})
	p.maxEntries = 1
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { _, err := p.Token(ctx, tokenRequest()); result <- err }()
	<-started
	other := tokenRequest()
	other.Audience = "another"
	if _, err := p.Token(t.Context(), other); !errors.Is(err, ErrCapacity) {
		t.Fatal("unbounded flights", err)
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("orphan token request")
	}
	waitFor(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return len(p.flights) == 0 && len(p.cache) == 0 })
}

func TestProviderErrorsNoRetryNoStaleFallback(t *testing.T) {
	var calls atomic.Int32
	var unavailable atomic.Bool
	var mu sync.Mutex
	var assertions []string
	p, _, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		assertions = append(assertions, r.Form.Get("client_assertion"))
		mu.Unlock()
		calls.Add(1)
		if unavailable.Load() {
			w.WriteHeader(503)
			_, _ = w.Write([]byte("private-token-body"))
			return
		}
		tokenResponse(w, 1, "crm.read crm.write")
	})
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	p.now = func() time.Time { return time.Unix(0, clock.Load()) }
	a, err := p.Token(t.Context(), tokenRequest())
	if err != nil {
		t.Fatal(err)
	}
	unavailable.Store(true)
	clock.Add(int64(280 * time.Second))
	for n := 0; n < 2; n++ {
		token, err := p.Token(t.Context(), tokenRequest())
		if !errors.Is(err, ErrToken) || token.Bearer() != "" || strings.Contains(fmt.Sprintf("%+v", err), "private-token-body") {
			t.Fatal("unsafe failure", err)
		}
	}
	if calls.Load() != 3 || assertions[1] == assertions[2] || !p.now().Before(a.ExpiresAt()) {
		t.Fatal("retries or stale fallback")
	}
}

func TestProviderStrictResponseAndConservativeTTL(t *testing.T) {
	valid := `{"access_token":"` + bearer(1) + `","token_type":"Bearer","expires_in":300,"scope":"crm.read crm.write"}`
	for name, body := range map[string]string{
		"missing": `{}`, "null": strings.Replace(valid, `300`, `null`, 1), "fraction": strings.Replace(valid, `300`, `1.5`, 1),
		"hugeTTL": strings.Replace(valid, `300`, `301`, 1), "zeroTTL": strings.Replace(valid, `300`, `0`, 1),
		"duplicate": strings.Replace(valid, `"expires_in":300`, `"expires_in":300,"expires_in":300`, 1),
		"caseAlias": strings.Replace(valid, `"expires_in":300`, `"expires_in":300,"Expires_In":300`, 1),
		"type":      strings.Replace(valid, `Bearer`, `Basic`, 1), "scopeExpansion": strings.Replace(valid, `crm.read crm.write`, `crm.read crm.write extra`, 1),
		"scopeReduction": strings.Replace(valid, `crm.read crm.write`, `crm.read`, 1), "duplicateScope": strings.Replace(valid, `crm.read crm.write`, `crm.read crm.read crm.write`, 1),
		"token": strings.Replace(valid, bearer(1), "legacy-secret", 1), "trailing": valid + `{}`, "oversized": valid + strings.Repeat(" ", 16<<10),
	} {
		t.Run(name, func(t *testing.T) {
			p, _, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			})
			token, err := p.Token(t.Context(), tokenRequest())
			if !errors.Is(err, ErrToken) || token.Bearer() != "" {
				t.Fatal("bad response accepted", err)
			}
		})
	}
	t.Run("processingDelay", func(t *testing.T) {
		var clock atomic.Int64
		start := time.Now()
		clock.Store(start.UnixNano())
		p, _, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
			clock.Add(int64(4 * time.Second))
			tokenResponse(w, 1, "crm.write crm.read")
		})
		p.now = func() time.Time { return time.Unix(0, clock.Load()) }
		token, err := p.Token(t.Context(), tokenRequest())
		if err != nil || !token.ExpiresAt().Equal(start.Add(300*time.Second)) {
			t.Fatal("lifetime inflated by response latency", err)
		}
	})
}

func TestProviderConfigurationAndRequestValidation(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	good := Config{ClientID: clientID, KeyID: "key", PrivateKey: key, TokenEndpoint: "https://auth.example/oauth/token"}
	for name, change := range map[string]func(*Config){
		"client": func(c *Config) { c.ClientID = strings.ToUpper(clientID) }, "kid": func(c *Config) { c.KeyID = "bad key" },
		"seed": func(c *Config) { c.PrivateKey = key.Seed() }, "corruptKey": func(c *Config) { c.PrivateKey = slices.Clone(key); c.PrivateKey[63] ^= 1 },
		"http": func(c *Config) { c.TokenEndpoint = "http://auth.example/oauth/token" }, "query": func(c *Config) { c.TokenEndpoint += "?" }, "fragment": func(c *Config) { c.TokenEndpoint += "#" },
		"userinfo": func(c *Config) { c.TokenEndpoint = "https://user:secret@auth.example/oauth/token" }, "capacity": func(c *Config) { c.MaxEntries = -1 }, "timeout": func(c *Config) { c.HTTPClient = &http.Client{Timeout: time.Minute} },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := good
			change(&cfg)
			if _, err := NewProvider(cfg); !errors.Is(err, ErrConfig) {
				t.Fatal(err)
			}
		})
	}
	for _, req := range []Request{{}, {ProjectID: tokenRequest().ProjectID, InstallationID: tokenRequest().InstallationID, Audience: "crm", Scopes: []string{"a", "a"}}, {ProjectID: tokenRequest().ProjectID, InstallationID: tokenRequest().InstallationID, Audience: "crm", Scopes: []string{"a b"}}} {
		if _, err := requestKey(req); !errors.Is(err, ErrRequest) {
			t.Fatal("invalid request accepted")
		}
	}
}
