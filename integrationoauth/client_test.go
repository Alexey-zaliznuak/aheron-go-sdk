package integrationoauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientRetryPolicyAndBodyReplay(t *testing.T) {
	for _, tc := range []struct {
		name, method            string
		status                  int
		explicit, stream        bool
		wantResource, wantToken int
	}{
		{"read401", "GET", 401, false, false, 2, 2},
		{"head401", "HEAD", 401, false, false, 2, 2},
		{"write401", "POST", 401, false, false, 1, 1},
		{"deduplicatedWrite401", "POST", 401, true, false, 2, 2},
		{"stream401", "POST", 401, true, true, 1, 1},
		{"forbidden", "GET", 403, false, false, 1, 1},
		{"unavailable", "GET", 503, false, false, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tokenCalls, resourceCalls atomic.Int32
			p, _, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				tokenResponse(w, int(tokenCalls.Add(1)), "crm.read crm.write")
			})
			var bodies []string
			var mu sync.Mutex
			resource := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := int(resourceCalls.Add(1))
				if r.Header.Get("Authorization") != "Bearer "+bearer(n) || r.Header.Get("X-Integration-Signature") != "" {
					t.Error("wrong credentials")
				}
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				bodies = append(bodies, string(body))
				mu.Unlock()
				if n == 1 {
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte("original rejection"))
					return
				}
				w.WriteHeader(200)
			}))
			defer resource.Close()
			c, err := NewClient(ClientConfig{Provider: p, BaseURL: resource.URL + "/api/crm", TokenRequest: tokenRequest(), HTTPClient: resource.Client()})
			if err != nil {
				t.Fatal(err)
			}
			var body io.Reader
			if tc.method == "POST" {
				body = strings.NewReader("immutable operation")
			}
			req, _ := http.NewRequest(tc.method, resource.URL+"/api/crm/projects?limit=1", body)
			if tc.stream {
				req.GetBody = nil
			}
			var resp *http.Response
			if tc.explicit {
				resp, err = c.DoIdempotent(t.Context(), req)
			} else {
				resp, err = c.Do(t.Context(), req)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if int(resourceCalls.Load()) != tc.wantResource || int(tokenCalls.Load()) != tc.wantToken {
				t.Fatal("wrong attempts", resourceCalls.Load(), tokenCalls.Load())
			}
			if req.Header.Get("Authorization") != "" {
				t.Fatal("caller request mutated")
			}
			if tc.wantResource == 2 && bodies[0] != bodies[1] {
				t.Fatal("body changed on retry")
			}
			if tc.wantResource == 1 && tc.method != "HEAD" {
				raw, _ := io.ReadAll(resp.Body)
				if string(raw) != "original rejection" {
					t.Fatal("original response lost")
				}
			}
		})
	}
}

func TestClientSecond401StopsAndInvalidates(t *testing.T) {
	var tokenCalls, resourceCalls atomic.Int32
	p, _, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		tokenResponse(w, int(tokenCalls.Add(1)), "crm.read crm.write")
	})
	resource := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { resourceCalls.Add(1); w.WriteHeader(401) }))
	defer resource.Close()
	c, _ := NewClient(ClientConfig{Provider: p, BaseURL: resource.URL, TokenRequest: tokenRequest(), HTTPClient: resource.Client()})
	req, _ := http.NewRequest("GET", resource.URL+"/data", nil)
	resp, err := c.Do(t.Context(), req)
	if err != nil || resp.StatusCode != 401 {
		t.Fatal(err)
	}
	resp.Body.Close()
	if tokenCalls.Load() != 2 || resourceCalls.Load() != 2 {
		t.Fatal("unbounded 401 loop")
	}
	if _, err := p.Token(t.Context(), tokenRequest()); err != nil || tokenCalls.Load() != 3 {
		t.Fatal("rejected second token retained", err)
	}
}

func TestClientDestinationAndCredentialBoundaries(t *testing.T) {
	var tokenCalls, resourceCalls atomic.Int32
	p, _, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		tokenCalls.Add(1)
		tokenResponse(w, 1, "crm.read crm.write")
	})
	resource := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { resourceCalls.Add(1) }))
	defer resource.Close()
	c, _ := NewClient(ClientConfig{Provider: p, BaseURL: resource.URL + "/api/crm", TokenRequest: tokenRequest(), HTTPClient: resource.Client()})
	for _, target := range []string{"http://example.com/api/crm", "https://example.com/api/crm", resource.URL + "/api/crm-other", resource.URL + "/api/crm/../admin", resource.URL + "/api/crm/%2e%2e/admin", resource.URL + "/api/crm/%252e%252e/admin", resource.URL + "/api/crm%2f..%2fadmin", resource.URL + "/api/crm/data#fragment"} {
		req, err := http.NewRequest("GET", target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Do(t.Context(), req); !errors.Is(err, ErrRequest) {
			t.Fatal("destination accepted", target, err)
		}
	}
	for _, header := range []string{"Authorization", "Cookie", "Proxy-Authorization", "X-Api-Key", "X-Integration-Id", "X-Integration-Timestamp", "X-Integration-Signature"} {
		req, _ := http.NewRequest("GET", resource.URL+"/api/crm/data", nil)
		req.Header[header] = []string{"legacy-secret"}
		if _, err := c.Do(t.Context(), req); !errors.Is(err, ErrRequest) {
			t.Fatal("mixed credentials accepted", header, err)
		}
	}
	req, _ := http.NewRequest("GET", resource.URL+"/api/crm/data", nil)
	req.Host = "other.example"
	if _, err := c.Do(t.Context(), req); !errors.Is(err, ErrRequest) {
		t.Fatal("Host override accepted", err)
	}
	if tokenCalls.Load() != 0 || resourceCalls.Load() != 0 {
		t.Fatal("rejected call touched network")
	}
}

func TestTokenAndResourceRedirectsCookiesAndClientOwnership(t *testing.T) {
	var escaped atomic.Int32
	var redirect atomic.Bool
	var cookieSeen atomic.Bool
	redirect.Store(true)
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { escaped.Add(1) }))
	defer target.Close()
	p, issuer, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if redirect.Load() {
			http.Redirect(w, r, target.URL+"/stolen", 307)
			return
		}
		cookieSeen.Store(r.Header.Get("Cookie") != "")
		tokenResponse(w, 1, "crm.read crm.write")
	})
	if _, err := p.Token(t.Context(), tokenRequest()); !errors.Is(err, ErrToken) {
		t.Fatal("token redirect accepted", err)
	}
	if escaped.Load() != 0 {
		t.Fatal("assertion followed redirect")
	}
	if issuer.Client().CheckRedirect != nil {
		t.Fatal("caller client mutated")
	}
	redirect.Store(false)
	// Configure the cookie jar before acquisition; Provider's copied client must
	// never use it. Keep server/client mutation outside in-flight requests.
	jar, _ := cookiejar.New(nil)
	issuerURL, _ := url.Parse(issuer.URL)
	jar.SetCookies(issuerURL, []*http.Cookie{{Name: "session", Value: "legacy-secret"}})
	caller := issuer.Client()
	caller.Jar = jar
	p2, err := NewProvider(Config{ClientID: clientID, KeyID: "key", PrivateKey: p.privateKey, TokenEndpoint: issuer.URL + "/oauth/token", HTTPClient: caller})
	if err != nil {
		t.Fatal(err)
	}
	resource := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			cookieSeen.Store(true)
		}
		http.Redirect(w, r, target.URL+"/stolen", 302)
	}))
	defer resource.Close()
	resourceClient := resource.Client()
	resourceClient.Jar = jar
	c, _ := NewClient(ClientConfig{Provider: p2, BaseURL: resource.URL, TokenRequest: tokenRequest(), HTTPClient: resourceClient})
	req, _ := http.NewRequest("GET", resource.URL+"/data", nil)
	resp, err := c.Do(t.Context(), req)
	if err != nil || resp.StatusCode != 302 {
		t.Fatal("resource redirect", err)
	}
	resp.Body.Close()
	if escaped.Load() != 0 || cookieSeen.Load() || caller.Jar != jar || caller.CheckRedirect != nil || resourceClient.CheckRedirect != nil {
		t.Fatal("credentials escaped or client mutated")
	}
}

type failingTransport struct{ calls atomic.Int32 }

func (f *failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.calls.Add(1)
	if r.Body != nil {
		r.Body.Close()
	}
	return nil, errors.New("secret-body-and-assertion")
}

func TestTransportErrorsAreRedactedAndNotRetried(t *testing.T) {
	p, _, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) { tokenResponse(w, 1, "crm.read crm.write") })
	failed := &failingTransport{}
	c, _ := NewClient(ClientConfig{Provider: p, BaseURL: "https://resource.example/api", TokenRequest: tokenRequest(), HTTPClient: &http.Client{Transport: failed}})
	req, _ := http.NewRequest("POST", "https://resource.example/api/data", strings.NewReader("operation"))
	if _, err := c.DoIdempotent(t.Context(), req); !errors.Is(err, ErrTransport) || strings.Contains(err.Error(), "secret") {
		t.Fatal("unsafe resource error", err)
	}
	if failed.calls.Load() != 1 {
		t.Fatal("ambiguous write retried")
	}
	p.http.Transport = failed
	p.Invalidate(tokenRequest(), Token{value: bearer(1)})
	if _, err := p.Token(t.Context(), tokenRequest()); !errors.Is(err, ErrToken) || strings.Contains(err.Error(), "secret") {
		t.Fatal("unsafe token error", err)
	}
	if failed.calls.Load() != 2 {
		t.Fatal("token POST retried")
	}
}

func TestProviderTimeoutCancelsExchange(t *testing.T) {
	started := make(chan struct{})
	p, _, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) { _ = r.ParseForm(); close(started); <-r.Context().Done() })
	p.http.Timeout = 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err := p.Token(ctx, tokenRequest())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("acquisition deadline missing", err)
	}
	<-started
}
