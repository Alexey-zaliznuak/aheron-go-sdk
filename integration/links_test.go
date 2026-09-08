package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestLinksClientSignedPaths(t *testing.T) {
	key := ed25519.NewKeyFromSeed(make([]byte, 32))
	pub := key.Public().(ed25519.PublicKey)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		canonical := append([]byte(fmt.Sprintf("%s.links-request-v1\n%s\n%s\n%s\n", r.Header.Get("X-Integration-Timestamp"), r.Method, r.URL.RequestURI(), r.Header.Get("Idempotency-Key"))), body...)
		sig, _ := base64.StdEncoding.DecodeString(r.Header.Get("X-Integration-Signature"))
		if !ed25519.Verify(pub, canonical, sig) {
			t.Errorf("signature mismatch for %s", r.URL.RequestURI())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"0123456789AbCdEf","items":[]}`))
	}))
	defer srv.Close()
	c, e := New(Config{IntegrationID: "11111111-1111-4111-8111-111111111111", PrivateKey: base64.StdEncoding.EncodeToString(key), LinksURL: srv.URL + "/api"})
	if e != nil {
		t.Fatal(e)
	}
	project := "22222222-2222-4222-8222-222222222222"
	if _, e := c.Links.Create(context.Background(), project, CreateLinkRequest{TargetURL: "https://example.com"}, "message:1"); e != nil {
		t.Fatal(e)
	}
	if _, e := c.Links.List(context.Background(), project, "a+/=?", 10); e != nil {
		t.Fatal(e)
	}
	if _, e := c.Links.RegisterCallback(context.Background(), "opened", LinkEndpointRequest{URL: "https://example.com/callback", Enabled: true}); e != nil {
		t.Fatal(e)
	}
	if e := c.Links.Disable(context.Background(), project, "0123456789AbCdEf"); e != nil {
		t.Fatal(e)
	}
	if calls != 4 {
		t.Fatal(calls)
	}
}
func TestLinkVerifierMetadataTampering(t *testing.T) {
	key := ed25519.NewKeyFromSeed(make([]byte, 32))
	pub := key.Public().(ed25519.PublicKey)
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kid": "links-1", "kty": "OKP", "crv": "Ed25519", "x": base64.RawURLEncoding.EncodeToString(pub)}}})
	}))
	defer jwks.Close()
	v := NewLinkVerifier(VerifierConfig{JWKSURL: jwks.URL})
	body := []byte(`{"id":"0123456789AbCdEf","data":{"subjectId":"s"}}`)
	event := "11111111-1111-4111-8111-111111111111"
	at := time.Now().UTC().Format(time.RFC3339Nano)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	canonical := append([]byte(fmt.Sprintf("%s.links-callback-v1\n%s\n%s\n", ts, event, at)), body...)
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(key, canonical))
	request := func() *http.Request {
		r := httptest.NewRequest("POST", "/callback", bytes.NewReader(body))
		r.Header.Set("X-Aheron-Timestamp", ts)
		r.Header.Set("X-Aheron-Key-Id", "links-1")
		r.Header.Set("X-Aheron-Signature", signature)
		r.Header.Set("Idempotency-Key", event)
		r.Header.Set("X-Aheron-Link-Occurred-At", at)
		return r
	}
	e, err := v.Verify(context.Background(), request())
	if err != nil || e.EventID != event || len(e.Data) == 0 {
		t.Fatalf("%+v %v", e, err)
	}
	for _, header := range []string{"Idempotency-Key", "X-Aheron-Link-Occurred-At", "X-Aheron-Timestamp"} {
		r := request()
		r.Header.Set(header, "22222222-2222-4222-8222-222222222222")
		if _, err := v.Verify(context.Background(), r); err == nil {
			t.Fatal("accepted tampered " + header)
		}
	}
}
