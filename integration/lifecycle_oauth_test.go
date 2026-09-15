package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
)

func lifecycleOAuthRequest() LifecycleRequest {
	r := lifecycleTestRequest(1, LifecycleInstall, lifecycleTestFirst)
	r.OAuth = &integrationoauth.InstallationSettings{IntegrationID: r.IntegrationID, ProjectID: r.ProjectID, InstallationID: r.InstallationID, ClientID: lifecycleTestNext, KeyID: "key-1", AccessVersion: 2, IntegrationAccessVersion: 1, GrantVersion: 2, PolicyRevision: 1, PolicyDigest: strings.Repeat("a", 64)}
	return r
}

func TestLifecycleOAuthBindingAndOrdering(t *testing.T) {
	r := lifecycleOAuthRequest()
	d, err := DecideLifecycle(LifecycleState{}, r)
	if err != nil || ValidateLifecycleOAuth(d.State, *r.OAuth) != nil {
		t.Fatal("OAuth install rejected", err)
	}
	changed := *r.OAuth
	changed.ClientID = lifecycleTestFirst
	if ValidateLifecycleOAuth(d.State, changed) == nil {
		t.Fatal("modified settings accepted")
	}
	if dup, err := DecideLifecycle(d.State, r); err != nil || dup.Receipt.Outcome != LifecycleDuplicate {
		t.Fatal("duplicate", err)
	}
	u := lifecycleTestRequest(2, LifecycleUninstall, lifecycleTestFirst)
	closed, err := DecideLifecycle(d.State, u)
	if err != nil || ValidateLifecycleOAuth(closed.State, *r.OAuth) == nil {
		t.Fatal("uninstall retained OAuth", err)
	}
	if late, err := DecideLifecycle(closed.State, r); err != nil || late.Receipt.Outcome != LifecycleSuperseded {
		t.Fatal("late delivery reopened installation", err)
	}
	for _, change := range []func(*LifecycleRequest){
		func(r *LifecycleRequest) { r.Action = LifecycleUninstall },
		func(r *LifecycleRequest) { r.OAuth.ProjectID = lifecycleTestNext },
		func(r *LifecycleRequest) { r.OAuth.GrantVersion = 0 },
	} {
		bad := lifecycleOAuthRequest()
		change(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid OAuth message accepted")
		}
	}
}

func TestLifecycleOAuthSignedReceiverOptInAndStrictJSON(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(nil)
	jwks := platformJWKS("lifecycle", pub)
	t.Cleanup(jwks.Close)
	v, _ := NewVerifier(VerifierConfig{JWKSURL: jwks.URL, HTTPClient: jwks.Client()})
	calls := 0
	fn := func(_ context.Context, r LifecycleRequest) (LifecycleReceipt, error) {
		calls++
		d, err := DecideLifecycle(LifecycleState{}, r)
		return d.Receipt, err
	}
	h, _ := v.HandleOAuthLifecycle(lifecycleTestIntegration, fn)
	raw, _ := json.Marshal(lifecycleOAuthRequest())
	call := func(h http.Handler, raw []byte, want int) {
		t.Helper()
		r := httptest.NewRequest("POST", "/lifecycle", bytes.NewReader(raw))
		r.Header = signLifecycleInbound(t, key, raw)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("status %d, want %d", w.Code, want)
		}
	}
	call(h, raw, 200)
	withoutOAuth := lifecycleOAuthRequest()
	withoutOAuth.OAuth = nil
	withoutOAuthRaw, _ := json.Marshal(withoutOAuth)
	call(h, withoutOAuthRaw, 400)
	for _, invalid := range []string{
		strings.Replace(string(raw), `"oauth":{`, `"oauth":{"unknown":1,`, 1),
		strings.Replace(string(raw), `"keyId":"key-1"`, `"keyId":"key-1","keyId":"key-2"`, 1),
		strings.Replace(string(raw), `"clientId":`, `"ClientId":`, 1),
		strings.Replace(string(raw), `"grantVersion":2`, `"grantVersion":null`, 1),
		strings.Replace(string(raw), `"policyRevision":1,`, ``, 1),
	} {
		call(h, []byte(invalid), 400)
	}
	if calls != 1 {
		t.Fatal("malformed settings reached storage")
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	sender, _ := NewLifecycleSender(LifecycleSenderConfig{PrivateKey: key, KeyID: "lifecycle", AllowHTTP: true})
	if _, err := sender.Deliver(context.Background(), srv.URL, lifecycleOAuthRequest()); err != nil {
		t.Fatal(err)
	}
}
