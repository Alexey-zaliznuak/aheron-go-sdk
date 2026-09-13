package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/sign"
)

func TestMigrationProofVerifiedReceiver(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	jwks := platformJWKS("migration", pub)
	defer jwks.Close()
	v, err := NewVerifier(VerifierConfig{JWKSURL: jwks.URL, HTTPClient: jwks.Client()})
	if err != nil {
		t.Fatal(err)
	}
	key := ed25519.NewKeyFromSeed(make([]byte, 32))
	h := sha256.Sum256(key.Public().(ed25519.PublicKey))
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Second)
	p := integrationoauth.MigrationProofRequest{ClientID: lifecycleTestFirst, ProofID: lifecycleTestNext, IntegrationID: lifecycleTestIntegration, JobID: lifecycleTestFirst, ProjectID: lifecycleTestNext, InstallationID: lifecycleTestFirst, LegacyKeyID: lifecycleTestNext, ClientVersion: 1, CredentialVersion: 1, KeyID: "migration-" + hex.EncodeToString(h[:]), Nonce: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), ExpectedKeyUpdatedAt: at, SourceScopes: []string{"crm.read"}, CreatedAt: at, ExpiresAt: at.Add(time.Minute)}
	ch := integrationoauth.MigrationChallenge{Protocol: integrationoauth.MigrationProofProtocol, MigrationProofRequest: p, RequestDigest: p.Digest()}
	var authCalls, reads atomic.Int32
	auth := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authCalls.Add(1)
		raw, _ := json.Marshal(p)
		var receipt integrationoauth.MigrationProofReceipt
		_ = json.Unmarshal(raw, &receipt)
		receipt.RequestDigest = p.Digest()
		receipt.State = "claimed"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(receipt)
	}))
	defer auth.Close()
	client, err := integrationoauth.NewMigrationProofClient(integrationoauth.MigrationProofConfig{IntegrationID: p.IntegrationID, PrivateKey: key, ProofEndpoint: auth.URL + "/proof", HTTPClient: auth.Client()})
	if err != nil {
		t.Fatal(err)
	}
	var deleted atomic.Bool
	handler, err := v.HandleMigrationProof(client, func(_ context.Context, in integrationoauth.MigrationChallenge) (string, error) {
		reads.Add(1)
		if deleted.Load() {
			return "", integrationoauth.ErrRequest
		}
		if in.InstallationID != p.InstallationID || in.ProjectID != p.ProjectID {
			t.Error("wrong lookup context")
		}
		return "legacy-key", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"valid", "duplicate", "wrong domain", "no signature", "wrong recipient", "altered nonce", "deleted", "query", "method"} {
		t.Run(mode, func(t *testing.T) {
			in := ch
			if mode == "wrong recipient" {
				in.IntegrationID = p.ProjectID
				in.RequestDigest = in.Digest()
			}
			if mode == "altered nonce" {
				in.Nonce = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
			}
			deleted.Store(mode == "deleted")
			raw, _ := json.Marshal(in)
			req := httptest.NewRequest("POST", "https://integration.example/oauth-migration", bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			ts := sign.FormatTimestamp(time.Now())
			domain := integrationoauth.MigrationProofProtocol
			if mode == "wrong domain" {
				domain = LifecycleProtocol
			}
			if mode != "no signature" {
				req.Header.Set(sign.HeaderPlatformTimestamp, ts)
				req.Header.Set(sign.HeaderPlatformKeyID, "migration")
				req.Header.Set(sign.HeaderPlatformSignature, sign.SignDomain(priv, domain, ts, raw))
			}
			if mode == "query" {
				req.URL.RawQuery = "issuer=https://evil.example"
			}
			if mode == "method" {
				req.Method = "GET"
			}
			before := authCalls.Load()
			readBefore := reads.Load()
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if mode == "valid" || mode == "duplicate" {
				if w.Code != 200 || authCalls.Load() != before+1 || reads.Load() != readBefore+1 {
					t.Fatal("proof not submitted", w.Code)
				}
			} else {
				if w.Code == 200 || authCalls.Load() != before {
					t.Fatal("invalid request reached auth", w.Code)
				}
				if mode != "deleted" && reads.Load() != readBefore {
					t.Fatal("credential read before validation")
				}
			}
			if w.Header().Get("Cache-Control") != "no-store" || bytes.Contains(w.Body.Bytes(), []byte("legacy-key")) || bytes.Contains(w.Body.Bytes(), []byte(ch.Nonce)) {
				t.Fatal("credential/challenge exposed")
			}
		})
	}
}
