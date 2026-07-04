package sign

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func jwksServer(t *testing.T, kid string, pub ed25519.PublicKey) *httptest.Server {
	t.Helper()
	body := fmt.Sprintf(
		`{"keys":[{"kty":"OKP","crv":"Ed25519","use":"sig","alg":"EdDSA","kid":%q,"x":%q}]}`,
		kid, base64.RawURLEncoding.EncodeToString(pub),
	)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

func TestKeySetSelectsByKID(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	srv := jwksServer(t, "aheron-int-202606-01", pub)
	defer srv.Close()

	ks := NewKeySet(srv.URL, srv.Client(), time.Minute)
	got, err := ks.Key(context.Background(), "aheron-int-202606-01")
	if err != nil {
		t.Fatalf("Key by kid: %v", err)
	}
	if !got.Equal(pub) {
		t.Fatal("returned key does not match published key")
	}
}

func TestKeySetEmptyKIDSingleKey(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	srv := jwksServer(t, "only", pub)
	defer srv.Close()

	ks := NewKeySet(srv.URL, srv.Client(), time.Minute)
	got, err := ks.Key(context.Background(), "")
	if err != nil {
		t.Fatalf("Key empty kid: %v", err)
	}
	if !got.Equal(pub) {
		t.Fatal("returned key does not match published key")
	}
}

func TestKeySetUnknownKID(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	srv := jwksServer(t, "known", pub)
	defer srv.Close()

	ks := NewKeySet(srv.URL, srv.Client(), time.Minute)
	if _, err := ks.Key(context.Background(), "missing"); err == nil {
		t.Fatal("expected error for unknown kid")
	}
}
