package aheron

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integration"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/platform"
)

const testProject = "b3f847bc-5ec7-405c-bf1b-b5952c4e2301"
const testSubject = "a41db43e-684c-4eb9-82e8-915419f92032"

func testProvider(t *testing.T, user string) platform.TokenProvider {
	t.Helper()
	e := base64.RawURLEncoding.EncodeToString
	p, err := platform.StaticToken(e([]byte("{}"))+"."+e([]byte(user))+"."+e([]byte("sig")), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestSharedResourceClients(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/projects":
			fmt.Fprintf(w, `[{"id":%q,"name":"project"}]`, testProject)
		case "/crm/projects/" + testProject + "/subjects/" + testSubject:
			fmt.Fprintf(w, `{"id":%q,"displayName":%q}`, testSubject, r.Header.Get("Authorization"))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := testProvider(t, fmt.Sprint(i))
			c, err := New(Config{UserTokenProvider: p, BaseURL: server.URL + "/api", CRMURL: server.URL + "/crm", AllowLoopbackHTTP: true})
			if err != nil {
				t.Error(err)
				return
			}
			projects, err := c.Projects.List(context.Background())
			if err != nil || len(projects) != 1 {
				t.Errorf("projects %v", err)
			}
			subject, err := c.CRM.GetSubject(context.Background(), testProject, testSubject)
			e := base64.RawURLEncoding.EncodeToString
			expected := "Bearer " + e([]byte("{}")) + "." + e([]byte(fmt.Sprint(i))) + "." + e([]byte("sig"))
			if err != nil || subject.DisplayName == nil || *subject.DisplayName != expected {
				t.Errorf("credential isolation failed: %v", err)
			}
		}(i)
	}
	wg.Wait()
	c, err := New(Config{ProjectAPIKey: "ahr_proj_test", CRMURL: server.URL + "/crm"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.CRM.GetSubject(context.Background(), testProject, testSubject)
	if err != nil || s.DisplayName == nil || *s.DisplayName != "Bearer ahr_proj_test" {
		t.Fatalf("project credential %v", err)
	}
	if _, err := c.Projects.List(context.Background()); err == nil {
		t.Fatal("project key used as user token")
	}
}
func TestUserCRMNoReplayOrCredentialFallback(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(503)
		_, _ = w.Write([]byte("SECRET"))
	}))
	defer s.Close()
	p := testProvider(t, "user")
	c, err := New(Config{UserTokenProvider: p, CRMURL: s.URL, AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.CRM.CreateTag(context.Background(), testProject, integration.CreateTagParams{Name: "tag"})
	if err == nil || strings.Contains(err.Error(), "SECRET") || calls.Load() != 1 {
		t.Fatalf("write replay or secret leak: %v calls=%d", err, calls.Load())
	}
	if _, err := New(Config{UserTokenProvider: p, ProjectAPIKey: "ahr_proj_test"}); err == nil {
		t.Fatal("ambiguous credentials accepted")
	}
	if _, err := integration.New(integration.Config{CRMUser: &platform.Config{TokenProvider: p}, CRMOAuth: &integration.CRMOAuthConfig{}}); err == nil {
		t.Fatal("ambiguous integration credentials accepted")
	}
}
