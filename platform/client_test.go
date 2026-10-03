package platform

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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

const projectID = "b3f847bc-5ec7-405c-bf1b-b5952c4e2301"
const schemeID = "a41db43e-684c-4eb9-82e8-915419f92032"
const otherID = "9c082304-2449-4d08-a834-6a306f4feb86"

func TestSharedTransportOriginAndInputIsolation(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Cookie") != "" {
			t.Error("cookie forwarded")
		}
		w.WriteHeader(204)
	}))
	defer s.Close()
	c, err := New(Config{BaseURL: s.URL + "/api", TokenProvider: provider(t, testToken("user")), AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{s.URL + "/other", s.URL + "/api/../other", s.URL + "/api/%2e%2e/other", "https://other.example/api/projects"} {
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		if _, err := c.Do(context.Background(), req); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("escaped API base: %s %v", target, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request reached upstream")
	}
	req, _ := http.NewRequest(http.MethodGet, s.URL+"/api/projects", nil)
	req.Header.Set("Authorization", "original")
	req.Header.Set("Cookie", "original")
	resp, err := c.Do(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if req.Header.Get("Authorization") != "original" || req.Header.Get("Cookie") != "original" {
		t.Fatal("input request mutated")
	}
}

// Fixtures have JWT transport syntax only: authenticity is checked by Aheron,
// never by the HTTP client under test.
func testToken(subject string) string {
	e := base64.RawURLEncoding.EncodeToString
	return e([]byte(`{"alg":"EdDSA"}`)) + "." + e([]byte(subject)) + "." + e([]byte("signature"))
}

func provider(t *testing.T, raw string) TokenProvider {
	t.Helper()
	p, err := StaticToken(raw, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func clientFor(t *testing.T, server *httptest.Server, p TokenProvider) *Client {
	t.Helper()
	c, err := New(Config{BaseURL: server.URL + "/api", TokenProvider: p, HTTPClient: server.Client(), AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func jsonReply(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}

func TestResourceContractsAndUpdatedProvider(t *testing.T) {
	var n atomic.Int64
	p := TokenProviderFunc(func(context.Context) (AccessToken, error) {
		return NewAccessToken(testToken(fmt.Sprint(n.Add(1))), time.Now().Add(time.Hour))
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Accept") != "application/json" {
			t.Error("unexpected request contract")
		}
		if r.Header.Get("Authorization") != "Bearer "+testToken(fmt.Sprint(n.Load())) {
			t.Error("stale token")
		}
		switch r.URL.Path {
		case "/api/projects":
			jsonReply(w, []Project{{ID: projectID, Name: "Проект"}})
		case "/api/projects/" + projectID:
			jsonReply(w, Project{ID: projectID, Name: "Проект", Settings: json.RawMessage(`{"nested":true}`)})
		case "/api/projects/" + projectID + "/schemes":
			jsonReply(w, []Scheme{{ID: schemeID, ProjectID: projectID, ExecutionStatus: "inactive"}})
		case "/api/projects/" + projectID + "/schemes/" + schemeID:
			jsonReply(w, Scheme{ID: schemeID, ProjectID: projectID, Name: "Схема"})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := clientFor(t, server, p)
	ctx := context.Background()
	projects, err := c.Projects.List(ctx)
	if err != nil || len(projects) != 1 || projects[0].Name != "Проект" {
		t.Fatalf("projects: %v %v", projects, err)
	}
	project, err := c.Projects.Get(ctx, projectID)
	if err != nil || string(project.Settings) != `{"nested":true}` {
		t.Fatalf("project: %v %v", project, err)
	}
	schemes, err := c.Schemes.List(ctx, projectID)
	if err != nil || len(schemes) != 1 || schemes[0].ExecutionStatus != "inactive" {
		t.Fatalf("schemes: %v %v", schemes, err)
	}
	scheme, err := c.Schemes.Get(ctx, projectID, schemeID)
	if err != nil || scheme.Name != "Схема" {
		t.Fatalf("scheme: %v %v", scheme, err)
	}
	if n.Load() != 4 {
		t.Fatalf("provider called %d times", n.Load())
	}
}

func TestConcurrentUserIsolation(t *testing.T) {
	a, b := testToken("user A"), testToken("user B")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var id string
		switch r.Header.Get("Authorization") {
		case "Bearer " + a:
			id = projectID
		case "Bearer " + b:
			id = otherID
		default:
			t.Error("unexpected authorization")
			w.WriteHeader(401)
			return
		}
		jsonReply(w, []Project{{ID: id}})
	}))
	defer server.Close()
	first := clientFor(t, server, provider(t, a))
	second, err := first.WithTokenProvider(provider(t, b))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 20 {
		for _, pair := range []struct {
			c  *Client
			id string
		}{{first, projectID}, {second, otherID}} {
			wg.Go(func() {
				items, err := pair.c.Projects.List(context.Background())
				if err != nil || len(items) != 1 || items[0].ID != pair.id {
					t.Errorf("cross-user response: %v %v", items, err)
				}
			})
		}
	}
	wg.Wait()
}

func TestRedirectAndCookieBoundaries(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true); jsonReply(w, []Project{}) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			t.Error("client jar leaked a cookie")
		}
		http.Redirect(w, r, target.URL+"/api/projects", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse(server.URL)
	jar.SetCookies(u, []*http.Cookie{{Name: "session", Value: "secret"}})
	var redirectCalled atomic.Bool
	original := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { redirectCalled.Store(true); return nil }}
	c, err := New(Config{BaseURL: server.URL + "/api", TokenProvider: provider(t, testToken("user")), HTTPClient: original, AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Projects.List(context.Background())
	var api *APIError
	if !errors.As(err, &api) || api.StatusCode != 307 {
		t.Fatalf("expected redirect rejection, got %v", err)
	}
	if leaked.Load() || redirectCalled.Load() {
		t.Fatal("redirect was followed")
	}
	if original.Jar != jar || original.Timeout != 0 {
		t.Fatal("caller HTTP client was mutated")
	}
}

func TestEndpointAndCredentialValidation(t *testing.T) {
	p := provider(t, testToken("user"))
	for _, endpoint := range []string{"http://api.example/api", "https://user:secret@api.example/api", "https://api.example/api?token=secret", "https://api.example/api#fragment", "https://api.example/../api", "https://api.example/api%2Fother", "ftp://api.example/api", "https://api.example/api?"} {
		if _, err := New(Config{BaseURL: endpoint, TokenProvider: p, AllowLoopbackHTTP: true}); !errors.Is(err, ErrConfig) {
			t.Errorf("accepted endpoint %q", endpoint)
		}
	}
	if _, err := New(Config{BaseURL: "http://127.0.0.1:9999/api", TokenProvider: p}); !errors.Is(err, ErrConfig) {
		t.Fatal("plaintext loopback must be explicit")
	}
	for _, raw := range []string{"", "ahr_proj_prefix_secret", "aho_some_secret", "first.second", "..", testToken("user") + "\r\nX-Secret: true"} {
		if _, err := StaticToken(raw, time.Time{}); !errors.Is(err, ErrCredential) {
			t.Error("accepted non-user credential")
		}
	}
	if _, err := New(Config{}); !errors.Is(err, ErrConfig) {
		t.Fatal("accepted missing provider")
	}
}

func TestInvalidResourcePathsDoNotCallProvider(t *testing.T) {
	var called bool
	c, err := New(Config{TokenProvider: TokenProviderFunc(func(context.Context) (AccessToken, error) { called = true; return AccessToken{}, nil })})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "../auth", projectID + "?query=secret", "https://example.com", projectID + "/other"} {
		if _, err := c.Projects.Get(context.Background(), id); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("accepted %q", id)
		}
		if _, err := c.Schemes.List(context.Background(), id); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("accepted %q", id)
		}
		if _, err := c.Schemes.Get(context.Background(), projectID, id); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("accepted %q", id)
		}
	}
	if called {
		t.Fatal("invalid resource invoked provider")
	}
}

func TestErrorsAreBoundedAndDoNotExposeCredentials(t *testing.T) {
	raw := testToken("sensitive user token")
	for _, status := range []int{401, 403, 404, 409, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
				_, _ = w.Write([]byte(raw))
			}))
			defer server.Close()
			c := clientFor(t, server, provider(t, raw))
			_, err := c.Projects.List(context.Background())
			var api *APIError
			if !errors.As(err, &api) || api.StatusCode != status || strings.Contains(fmt.Sprintf("%+v", err), raw) {
				t.Fatalf("unsafe error: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatal("automatic retry")
			}
			if status == 401 && !errors.Is(err, ErrUnauthorized) {
				t.Fatal("unauthorized not classified")
			}
		})
	}
	p := TokenProviderFunc(func(context.Context) (AccessToken, error) { return AccessToken{}, errors.New(raw) })
	c, _ := New(Config{TokenProvider: p})
	_, err := c.Projects.List(context.Background())
	if err != ErrCredential || strings.Contains(err.Error(), raw) {
		t.Fatal("provider error leaked")
	}
	token, _ := NewAccessToken(raw, time.Time{})
	encoded, _ := json.Marshal(token)
	if strings.Contains(fmt.Sprintf("%v %+v %#v %s", token, token, token, encoded), raw) {
		t.Fatal("credential printed")
	}
}

func TestResponseValidation(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
		limit                   int64
		want                    error
	}{
		{"html", "text/html", `<html>login</html>`, 0, ErrResponse},
		{"null", "application/json", "null", 0, ErrResponse},
		{"malformed", "application/json", "[", 0, ErrResponse},
		{"multiple", "application/json", "[] []", 0, ErrResponse},
		{"missing id", "application/json", `[{"name":"project"}]`, 0, ErrResponse},
		{"size", "application/json", strings.Repeat(" ", 40) + "[]", 16, ErrResponseTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			c, err := New(Config{BaseURL: server.URL + "/api", TokenProvider: provider(t, testToken("user")), AllowLoopbackHTTP: true, MaxResponseBytes: tc.limit})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.Projects.List(context.Background()); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestResponseCannotSwitchResource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "schemes") {
			jsonReply(w, Scheme{ID: schemeID, ProjectID: otherID})
			return
		}
		jsonReply(w, Project{ID: otherID})
	}))
	defer server.Close()
	c := clientFor(t, server, provider(t, testToken("user")))
	if _, err := c.Projects.Get(context.Background(), projectID); !errors.Is(err, ErrResponse) {
		t.Fatal("accepted other project")
	}
	if _, err := c.Schemes.Get(context.Background(), projectID, schemeID); !errors.Is(err, ErrResponse) {
		t.Fatal("accepted cross-project scheme")
	}
}

func TestExpiredAndCancelledRequests(t *testing.T) {
	p, err := StaticToken(testToken("user"), time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := New(Config{TokenProvider: p})
	if _, err := c.Projects.List(context.Background()); !errors.Is(err, ErrTokenExpired) {
		t.Fatal("expired credential accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Projects.List(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled context not preserved")
	}
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer server.Close()
	c = clientFor(t, server, provider(t, testToken("user")))
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.Projects.List(ctx); done <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request not started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("request ignored cancellation")
	}
}
