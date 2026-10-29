package integrationoauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIntegrationOAuthIntrospection(t *testing.T) {
	const id = "11111111-1111-1111-1111-111111111111"
	token := "aho_" + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	now := time.Now().Unix()
	good := map[string]any{"active": true, "client_id": id, "token_type": "Bearer", "scope": "crm.read variables.write", "aud": "campaign", "sub": id, "exp": now + 290, "iat": now, "projectId": id, "integrationId": id, "installationId": id}
	for _, name := range []string{"active", "inactive", "wrongAudience", "wrongSubject", "expired", "future", "longLifetime", "badID", "missing", "unknown", "badScope", "wrongType", "duplicate", "null", "trailing", "emptyScope", "duplicateScope", "serverError", "redirect", "html", "oversize", "invalidBearer"} {
		t.Run(name, func(t *testing.T) {
			wire := map[string]any{}
			for k, v := range good {
				wire[k] = v
			}
			want := ErrIntegrationOAuthUnavailable
			switch name {
			case "active":
				want = nil
			case "inactive":
				wire = map[string]any{"active": false}
				want = ErrInvalidIntegrationToken
			case "wrongAudience":
				wire["aud"] = "execution"
			case "wrongSubject":
				wire["sub"] = "different"
			case "expired":
				wire["iat"] = now - 100
				wire["exp"] = now - 1
				want = ErrInvalidIntegrationToken
			case "future":
				wire["iat"] = now + 60
				wire["exp"] = now + 100
			case "longLifetime":
				wire["exp"] = now + 301
			case "badID":
				wire["projectId"] = "not-a-project"
			case "unknown":
				wire["extra"] = true
			case "badScope":
				wire["scope"] = "campaign.read\tbad"
			case "wrongType":
				wire["token_type"] = "bearer"
			case "missing":
				delete(wire, "installationId")
			case "null":
				wire["active"] = nil
			case "emptyScope":
				wire["scope"] = ""
			case "duplicateScope":
				wire["scope"] = "integration.resolve integration.resolve"
			case "invalidBearer":
				want = ErrInvalidIntegrationToken
			}
			raw, _ := json.Marshal(wire)
			if name == "duplicate" {
				raw = append([]byte(`{"active":true,`), raw[1:]...)
			}
			if name == "trailing" {
				raw = append(raw, []byte(`{}`)...)
			}
			if name == "oversize" {
				raw = append(raw, []byte(strings.Repeat(" ", 16<<10))...)
			}
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/internal/oauth/introspect" || r.Header.Get("X-Internal-Token") != "internal" {
					t.Error("incorrect introspection transport")
				}
				if err := r.ParseForm(); err != nil || r.Form.Get("token") != token || r.Form.Get("audience") != "campaign" {
					t.Error("incorrect introspection form")
				}
				w.Header().Set("Content-Type", "application/json")
				if name == "serverError" {
					w.WriteHeader(503)
				}
				if name == "redirect" {
					w.Header().Set("Location", "/must-not-follow")
					w.WriteHeader(307)
				}
				if name == "html" {
					w.Header().Set("Content-Type", "text/html")
				}
				_, _ = w.Write(raw)
			}))
			defer upstream.Close()
			verifier, err := NewResourceVerifier(upstream.URL, "internal", "campaign", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			candidate := token
			if name == "invalidBearer" {
				candidate = "ahr_proj_secret"
			}
			for i := 0; i < 2; i++ {
				p, err := verifier.Verify(context.Background(), candidate)
				if !errors.Is(err, want) {
					t.Fatalf("error=%v want=%v", err, want)
				}
				if err == nil && (p.ProjectID != id || p.InstallationID != id || !p.HasScope("variables.write")) {
					t.Fatal("lost principal")
				}
			}
			expected := 2
			if name == "invalidBearer" {
				expected = 0
			}
			if calls != expected {
				t.Fatalf("calls=%d; introspection must be uncached and redirect-free", calls)
			}
		})
	}
}

func TestIntegrationOAuthCancellation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer upstream.Close()
	v, err := NewResourceVerifier(upstream.URL, "internal", "campaign", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = v.Verify(ctx, "aho_"+base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	if !errors.Is(err, ErrIntegrationOAuthUnavailable) {
		t.Fatalf("error=%v", err)
	}
}

func TestResourceVerifierRevocation(t *testing.T) {
	const id = "11111111-1111-1111-1111-111111111111"
	token := "aho_" + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls > 1 {
			_, _ = w.Write([]byte(`{"active":false}`))
			return
		}
		now := time.Now().Unix()
		_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "client_id": id, "token_type": "Bearer", "scope": "campaign.read", "aud": "campaign", "sub": id, "exp": now + 290, "iat": now, "projectId": id, "integrationId": id, "installationId": id})
	}))
	defer srv.Close()
	verifier, _ := NewResourceVerifier(srv.URL, "secret", "campaign", time.Second)
	if _, err := verifier.Verify(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(context.Background(), token); !errors.Is(err, ErrInvalidIntegrationToken) {
		t.Fatalf("revocation ignored: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestResourcePrincipalContextIsolation(t *testing.T) {
	p := ResourcePrincipal{Scopes: []string{"campaign.read"}}
	ctx := ContextWithResourcePrincipal(context.Background(), p)
	p.Scopes[0] = "campaign.write"
	read, ok := ResourcePrincipalFromContext(ctx)
	if !ok || !read.HasScope("campaign.read") || read.HasScope("campaign.write") {
		t.Fatal("context shares input scopes")
	}
	read.Scopes[0] = "campaign.write"
	again, _ := ResourcePrincipalFromContext(ctx)
	if !again.HasScope("campaign.read") {
		t.Fatal("context shares returned scopes")
	}
	if _, ok := ResourcePrincipalFromContext(context.Background()); ok {
		t.Fatal("unexpected principal")
	}
}

func TestResourceVerifierConfiguration(t *testing.T) {
	for _, url := range []string{"", "ftp://example.test", "https://u:p@example.test", "https://example.test?q=1", "https://example.test#fragment"} {
		if _, err := NewResourceVerifier(url, "secret", "campaign", time.Second); !errors.Is(err, ErrIntegrationOAuthConfig) {
			t.Errorf("accepted URL %q", url)
		}
	}
	for _, audience := range []string{"", "campaign execution", "campaign\n", "campaign\t", strings.Repeat("a", 129)} {
		if _, err := NewResourceVerifier("https://example.test", "secret", audience, time.Second); !errors.Is(err, ErrIntegrationOAuthConfig) {
			t.Error("accepted invalid audience")
		}
	}
	for _, token := range []string{"", " ", "secret\nvalue"} {
		if _, err := NewResourceVerifier("https://example.test", token, "campaign", time.Second); !errors.Is(err, ErrIntegrationOAuthConfig) {
			t.Error("accepted invalid token")
		}
	}
	for _, timeout := range []time.Duration{0, -1, 11 * time.Second} {
		if _, err := NewResourceVerifier("https://example.test", "secret", "campaign", timeout); !errors.Is(err, ErrIntegrationOAuthConfig) {
			t.Error("accepted timeout")
		}
	}
}
