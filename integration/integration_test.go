package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
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

func TestInboundVerifyAndDecode(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	const kid = "aheron-int-202606-01"
	jwks := platformJWKS(kid, pub)
	defer jwks.Close()

	verifier, err := NewVerifier(VerifierConfig{JWKSURL: jwks.URL, HTTPClient: jwks.Client()})
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}

	envelope := `{"type":"block.action","payload":{` +
		`"settings":{"outputs":["ok","fail"]},` +
		`"subject":{"id":"subj-1"},"project":{"id":"proj-1"},` +
		`"vars":{"greeting":"hi"},"tags":["a"],` +
		`"resolve":{"url":"https://example.test/resolve","executionContextId":"ctx-1","contextVersion":7,"outputs":["ok","fail"]}}}`
	body := []byte(envelope)

	var captured ActionRequest
	handler := verifier.HandleAction(func(_ context.Context, req ActionRequest) error {
		captured = req
		return nil
	})

	req := httptest.NewRequest(http.MethodPost, "/blocks/echo", bytes.NewReader(body))
	req.Header = signInbound(t, priv, kid, body)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if captured.Payload.Subject.ID != "subj-1" || captured.Payload.Resolve.ExecutionContextID != "ctx-1" {
		t.Fatalf("decoded payload mismatch: %+v", captured.Payload)
	}
	if captured.Payload.Resolve.ContextVersion != 7 {
		t.Fatalf("context version mismatch: %d", captured.Payload.Resolve.ContextVersion)
	}
	if captured.KeyID != kid {
		t.Fatalf("kid mismatch: %q", captured.KeyID)
	}
	rp := captured.Resolve("ok", nil)
	if rp.URL != "https://example.test/resolve" {
		t.Fatalf("resolve url not carried: %q", rp.URL)
	}
}

func TestInboundRejectsTamperedSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	const kid = "k1"
	jwks := platformJWKS(kid, pub)
	defer jwks.Close()

	verifier, _ := NewVerifier(VerifierConfig{JWKSURL: jwks.URL, HTTPClient: jwks.Client()})
	handler := verifier.HandleAction(func(context.Context, ActionRequest) error { return nil })

	body := []byte(`{"type":"block.action","payload":{}}`)
	header := signInbound(t, priv, kid, body)
	// Tamper: change the body after signing.
	tampered := []byte(`{"type":"block.action","payload":{"subject":{"id":"x"}}}`)

	req := httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(tampered))
	req.Header = header
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 for tampered body, got %d", rec.Code)
	}
}

func TestOutboundResolveIsSigned(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	seed := base64.StdEncoding.EncodeToString(priv.Seed())

	var gotBody []byte
	var gotID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotID = r.Header.Get(sign.HeaderIntegrationID)
		ts := r.Header.Get(sign.HeaderIntegrationTimestamp)
		sig := r.Header.Get(sign.HeaderIntegrationSignature)
		if err := sign.Verify(pub, ts, gotBody, sig); err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c, err := New(Config{IntegrationID: "int-1", PrivateKey: seed, ExecutionURL: srv.URL})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	err = c.Steps.Resolve(context.Background(), ResolveParams{
		ExecutionContextID: "ctx-9",
		ContextVersion:     3,
		Output:             "ok",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if gotID != "int-1" {
		t.Fatalf("integration id header mismatch: %q", gotID)
	}
	var decoded map[string]any
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("decode captured body: %v", err)
	}
	if decoded["executionContextId"] != "ctx-9" || decoded["output"] != "ok" {
		t.Fatalf("resolve body mismatch: %v", decoded)
	}
}

func TestResolveRequiresSigner(t *testing.T) {
	c, err := New(Config{IntegrationID: "int-1"}) // no private key
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if err := c.Steps.Resolve(context.Background(), ResolveParams{ExecutionContextID: "x", Output: "ok"}); err == nil {
		t.Fatal("expected error when no signer configured")
	}
}
