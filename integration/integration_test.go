package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/sign"
)

// platformJWKS serves a single platform signing key so a Verifier can fetch it.
func platformJWKS(kid string, pub ed25519.PublicKey) *httptest.Server {
	body, _ := json.Marshal(map[string]any{
		"keys": []map[string]string{{
			"kty": "OKP", "crv": "Ed25519", "use": "sig", "alg": "EdDSA",
			"kid": kid, "x": base64.RawURLEncoding.EncodeToString(pub),
		}},
	})
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
}

// signInbound builds the platform-signed headers for an inbound request body.
func signInbound(t *testing.T, priv ed25519.PrivateKey, kid string, body []byte) http.Header {
	t.Helper()
	ts := sign.FormatTimestamp(time.Now())
	h := http.Header{}
	h.Set(sign.HeaderPlatformTimestamp, ts)
	h.Set(sign.HeaderPlatformSignature, sign.Sign(priv, ts, body))
	h.Set(sign.HeaderPlatformKeyID, kid)
	return h
}

func TestInboundVerifyAndDecodeAction(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	const kid = "aheron-int-202606-01"
	jwks := platformJWKS(kid, pub)
	defer jwks.Close()

	verifier, err := NewVerifier(VerifierConfig{JWKSURL: jwks.URL, HTTPClient: jwks.Client()})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}

	// An author-designed action body (matches the echo example's template).
	body := []byte(`{"context":{"id":"ctx-1","version":7,"inputKey":"in","schemeId":"scheme-1"},` +
		`"actionKey":"open_course","settings":{"outputs":["ok","fail"]},` +
		`"vars":{"project":{},"subject":{"name":"a"}},"integrationContext":{}}`)

	type actionBody struct {
		Context   ExecutionContext `json:"context"`
		ActionKey string           `json:"actionKey"`
		Settings  struct {
			Outputs []string `json:"outputs"`
		} `json:"settings"`
	}

	var captured actionBody
	handler := verifier.Handle(func(_ context.Context, r *http.Request) error {
		return DecodeBody(r, &captured)
	})

	req := httptest.NewRequest(http.MethodPost, "/blocks/action", bytes.NewReader(body))
	req.Header = signInbound(t, priv, kid, body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if captured.Context.ID != "ctx-1" || captured.Context.Version != 7 {
		t.Fatalf("decoded context mismatch: %+v", captured.Context)
	}
	if captured.Context.SchemeID != "scheme-1" {
		t.Fatalf("decoded schemeId mismatch: %q", captured.Context.SchemeID)
	}
	if captured.ActionKey != "open_course" {
		t.Fatalf("actionKey mismatch: %q", captured.ActionKey)
	}
	if len(captured.Settings.Outputs) != 2 || captured.Settings.Outputs[0] != "ok" {
		t.Fatalf("outputs mismatch: %v", captured.Settings.Outputs)
	}
}

func TestInboundRejectsTamperedSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	const kid = "k1"
	jwks := platformJWKS(kid, pub)
	defer jwks.Close()

	verifier, _ := NewVerifier(VerifierConfig{JWKSURL: jwks.URL, HTTPClient: jwks.Client()})
	handler := verifier.Handle(func(context.Context, *http.Request) error { return nil })

	body := []byte(`{"actionKey":"x"}`)
	header := signInbound(t, priv, kid, body)
	// Tamper: change the body after signing.
	tampered := []byte(`{"actionKey":"y"}`)

	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(tampered))
	req.Header = header
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 for tampered body, got %d", rec.Code)
	}
}

func TestListVariableDefinitions(t *testing.T) {
	var gotPath, gotQuery, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"id":"d1","projectId":"p1","name":"Ответ","key":"answer","type":"string","ownerType":"project","createdAt":"2026-01-01T00:00:00Z"},
			{"id":"d2","projectId":"p1","name":"VK id","key":"vk_user_id","type":"string","ownerType":"integration","integrationId":"int-9","createdAt":"2026-01-01T00:00:00Z"}
		]`))
	}))
	defer srv.Close()

	c, err := New(Config{IntegrationID: "int-1", APIKey: "key-1", CRMURL: srv.URL})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	defs, err := c.CRM.ListVariableDefinitions(context.Background(), "p1", ListVariableDefinitionsParams{OwnerType: "project"})
	if err != nil {
		t.Fatalf("list variable definitions: %v", err)
	}
	if gotPath != "/projects/p1/variable-definitions" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotQuery != "ownerType=project" {
		t.Fatalf("query = %q", gotQuery)
	}
	if gotAuth != "Bearer key-1" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if len(defs) != 2 || defs[0].Key != "answer" || defs[1].OwnerType != "integration" {
		t.Fatalf("defs = %+v", defs)
	}
	if defs[1].IntegrationID == nil || *defs[1].IntegrationID != "int-9" {
		t.Fatalf("integrationId = %v", defs[1].IntegrationID)
	}
}

func TestListVariableDefinitionsRequiresProject(t *testing.T) {
	c, err := New(Config{IntegrationID: "int-1", APIKey: "key-1"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := c.CRM.ListVariableDefinitions(context.Background(), "", ListVariableDefinitionsParams{}); err == nil {
		t.Fatal("expected error for empty projectID")
	}
}
