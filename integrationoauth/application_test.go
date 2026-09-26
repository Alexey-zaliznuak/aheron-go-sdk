package integrationoauth

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestApplicationOAuthProviderProfiles(t *testing.T) {
	var calls atomic.Int32
	var public ed25519.PublicKey
	var endpoint string
	var jtis sync.Map
	p, server, private := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.ParseForm() != nil {
			t.Error("invalid form")
			w.WriteHeader(400)
			return
		}
		parts := strings.Split(r.Form.Get("client_assertion"), ".")
		if len(parts) != 3 {
			t.Error("invalid assertion")
			w.WriteHeader(400)
			return
		}
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var claims map[string]any
		if json.Unmarshal(payload, &claims) != nil || !ed25519.Verify(public, []byte(parts[0]+"."+parts[1]), sig) || claims["aud"] != endpoint || claims["iss"] != clientID || claims["sub"] != clientID {
			t.Error("invalid signed client identity")
		}
		if _, seen := jtis.LoadOrStore(claims["jti"], true); seen {
			t.Error("assertion reused")
		}
		token := bearer(int(n))
		if r.Form.Get("tokenKind") == "application" {
			if len(r.Form) != 6 || r.Form.Has("projectId") || r.Form.Has("installationId") || r.Form.Has("scope") {
				t.Error("application request contains installation identity")
			}
			if r.Form.Get("audience") != "catalog" && r.Form.Get("audience") != "links" {
				t.Error("wrong application audience")
			}
			token = "aho_app_" + strings.TrimPrefix(token, "aho_")
		} else if r.Form.Get("projectId") != tokenRequest().ProjectID || r.Form.Get("installationId") != tokenRequest().InstallationID {
			t.Error("lost installation identity")
		}
		w.Header().Set("Content-Type", "application/json")
		response := map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 300}
		if r.Form.Get("tokenKind") != "application" {
			response["scope"] = r.Form.Get("scope")
		}
		_ = json.NewEncoder(w).Encode(response)
	})
	public = private.Public().(ed25519.PublicKey)
	endpoint = server.URL + "/oauth/token"
	ctx := context.Background()
	catalog := ApplicationRequest{Audience: "catalog"}
	links := ApplicationRequest{Audience: "links"}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := p.ApplicationToken(ctx, catalog)
			if err != nil || !strings.HasPrefix(tok.Bearer(), "aho_app_") {
				t.Errorf("application token: %v", err)
			}
		}()
	}
	wg.Wait()
	a, err := p.ApplicationToken(ctx, catalog)
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.ApplicationToken(ctx, links)
	if err != nil {
		t.Fatal(err)
	}
	c, err := p.Token(ctx, tokenRequest())
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || a.Bearer() == b.Bearer() || b.Bearer() == c.Bearer() {
		t.Fatal("profiles or audiences shared tokens")
	}
	p.InvalidateApplication(links, a)
	if _, err := p.ApplicationToken(ctx, links); err != nil || calls.Load() != 3 {
		t.Fatal("foreign invalidation")
	}
	p.InvalidateApplication(catalog, a)
	fresh, err := p.ApplicationToken(ctx, catalog)
	if err != nil || calls.Load() != 4 || fresh.Bearer() == a.Bearer() {
		t.Fatal("failed to refresh application token")
	}
	p.InvalidateApplication(catalog, a)
	if _, err := p.ApplicationToken(ctx, catalog); err != nil || calls.Load() != 4 {
		t.Fatal("late 401 evicted newer token")
	}
}

func TestApplicationOAuthRejectsProfileConfusion(t *testing.T) {
	for _, application := range []bool{true, false} {
		p, _, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			value := bearer(1)
			if !application {
				value = "aho_app_" + strings.TrimPrefix(value, "aho_")
			}
			w.Header().Set("Content-Type", "application/json")
			response := map[string]any{"access_token": value, "token_type": "Bearer", "expires_in": 300}
			if r.Form.Get("tokenKind") != "application" {
				response["scope"] = r.Form.Get("scope")
			}
			_ = json.NewEncoder(w).Encode(response)
		})
		var err error
		if application {
			_, err = p.ApplicationToken(context.Background(), ApplicationRequest{Audience: "catalog"})
		} else {
			_, err = p.Token(context.Background(), tokenRequest())
		}
		if !errors.Is(err, ErrToken) {
			t.Fatalf("cross-profile response accepted: %v", err)
		}
	}
	for _, req := range []ApplicationRequest{{}, {"crm"}, {""}} {
		var p *Provider
		if _, err := p.ApplicationToken(context.Background(), req); !errors.Is(err, ErrRequest) {
			t.Fatal("invalid application grant reached provider")
		}
	}
}

func TestApplicationOAuthResourceBoundary(t *testing.T) {
	var calls atomic.Int32
	p, server, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	c, err := NewApplicationClient(ApplicationClientConfig{Provider: p, BaseURL: server.URL + "/api", HTTPClient: server.Client(), TokenRequest: ApplicationRequest{Audience: "catalog"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/outside", "/api/../other", "/api/%2fother"} {
		r, _ := http.NewRequest("POST", server.URL+path, nil)
		if _, err := c.DoIdempotent(context.Background(), r); !errors.Is(err, ErrRequest) {
			t.Errorf("accepted path %s: %v", path, err)
		}
	}
	r, _ := http.NewRequest("POST", server.URL+"/api/integrations/self/sync", nil)
	r.Header.Set("X-Integration-Signature", "legacy")
	if _, err := c.Do(context.Background(), r); !errors.Is(err, ErrRequest) || calls.Load() != 0 {
		t.Fatal("mixed credential escaped")
	}
}
