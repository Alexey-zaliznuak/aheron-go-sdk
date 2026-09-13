package integrationoauth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func proofFixture() (ed25519.PrivateKey, MigrationChallenge) {
	key := ed25519.NewKeyFromSeed(make([]byte, 32))
	h := sha256.Sum256(key.Public().(ed25519.PublicKey))
	id := func(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }
	at := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Second)
	p := MigrationProofRequest{ClientID: id(1), ProofID: id(2), IntegrationID: id(3), JobID: id(4), ProjectID: id(5), InstallationID: id(6), LegacyKeyID: id(7), ClientVersion: 2, CredentialVersion: 4, KeyID: "migration-" + hex.EncodeToString(h[:]), Nonce: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), ExpectedKeyUpdatedAt: at, SourceScopes: []string{"crm.read"}, CreatedAt: at, ExpiresAt: at.Add(15 * time.Minute)}
	return key, MigrationChallenge{Protocol: MigrationProofProtocol, MigrationProofRequest: p, RequestDigest: p.Digest()}
}
func proofResponse(ch MigrationChallenge) MigrationProofReceipt {
	raw, _ := json.Marshal(ch.MigrationProofRequest)
	var r MigrationProofReceipt
	_ = json.Unmarshal(raw, &r)
	r.RequestDigest, r.State = ch.RequestDigest, "claimed"
	return r
}

func TestMigrationProofSubmit(t *testing.T) {
	key, ch := proofFixture()
	var mu sync.Mutex
	seen := map[string]bool{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != "POST" || r.URL.Path != "/oauth/migration/proof" || r.Header.Get("Authorization") != "" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("submission boundary")
		}
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 5 || body["clientId"] != ch.ClientID || body["proofId"] != ch.ProofID || body["nonce"] != ch.Nonce || body["legacyApiKey"] != "legacy-fixture-key" {
			t.Error("submission contract")
		}
		parts := strings.Split(body["clientAssertion"], ".")
		if len(parts) != 3 {
			t.Error("missing assertion")
			w.WriteHeader(400)
			return
		}
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		if !ed25519.Verify(key.Public().(ed25519.PublicKey), []byte(parts[0]+"."+parts[1]), sig) {
			t.Error("invalid EdDSA signature")
		}
		headerRaw, _ := base64.RawURLEncoding.DecodeString(parts[0])
		var header map[string]string
		_ = json.Unmarshal(headerRaw, &header)
		if header["alg"] != "EdDSA" || header["typ"] != "JWT" || header["kid"] != ch.KeyID {
			t.Error("wrong header")
		}
		payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var claims map[string]any
		_ = json.Unmarshal(payload, &claims)
		for name, want := range map[string]string{"iss": ch.ClientID, "sub": ch.ClientID, "aud": "https://" + r.Host + r.URL.Path, "proofId": ch.ProofID, "jobId": ch.JobID, "projectId": ch.ProjectID, "installationId": ch.InstallationID, "legacyKeyId": ch.LegacyKeyID, "requestDigest": ch.RequestDigest, "nonce": ch.Nonce} {
			if claims[name] != want {
				t.Errorf("claim %s mismatch", name)
			}
		}
		if claims["clientVersion"] != float64(ch.ClientVersion) || claims["credentialVersion"] != float64(ch.CredentialVersion) || claims["exp"].(float64)-claims["iat"].(float64) > 60 {
			t.Error("versions/TTL")
		}
		jti, _ := claims["jti"].(string)
		if len(jti) != 43 || seen[jti] {
			t.Error("reused jti")
		}
		seen[jti] = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(proofResponse(ch))
	}))
	defer server.Close()
	c, err := NewMigrationProofClient(MigrationProofConfig{IntegrationID: ch.IntegrationID, PrivateKey: key, ProofEndpoint: server.URL + "/oauth/migration/proof", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if got, err := c.Submit(context.Background(), ch, "legacy-fixture-key"); err != nil || !got.Matches(ch.MigrationProofRequest) {
			t.Fatal("submit", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatal("expected fresh assertions")
	}
}

func TestMigrationProofFailClosed(t *testing.T) {
	key, ch := proofFixture()
	response, _ := json.Marshal(proofResponse(ch))
	for _, mode := range []string{"redirect", "server error", "wrong receipt", "pending", "null expiry omitted", "duplicate", "case", "extra", "oversize", "content type", "trailing"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				raw := response
				switch mode {
				case "redirect":
					w.Header().Set("Location", "https://"+r.Host+"/leak")
					w.WriteHeader(307)
					return
				case "server error":
					w.WriteHeader(503)
					_, _ = io.WriteString(w, "private body detail")
					return
				case "wrong receipt":
					raw = bytes.Replace(raw, []byte(ch.InstallationID), []byte(ch.ProjectID), 1)
				case "pending":
					raw = bytes.Replace(raw, []byte(`"claimed"`), []byte(`"pending"`), 1)
				case "null expiry omitted":
					raw = bytes.Replace(raw, []byte(`"sourceExpiresAt":null,`), nil, 1)
				case "duplicate":
					raw = append([]byte(`{"state":"claimed",`), raw[1:]...)
				case "case":
					raw = bytes.Replace(raw, []byte(`"clientId"`), []byte(`"ClientId"`), 1)
				case "extra":
					raw = append([]byte(`{"accessToken":"should-not-exist",`), raw[1:]...)
				case "oversize":
					raw = bytes.Repeat([]byte(" "), 21<<10)
				case "content type":
					w.Header().Set("Content-Type", "text/plain")
				case "trailing":
					raw = append(raw, []byte(` {}`)...)
				}
				_, _ = w.Write(raw)
			}))
			defer server.Close()
			c, err := NewMigrationProofClient(MigrationProofConfig{IntegrationID: ch.IntegrationID, PrivateKey: key, ProofEndpoint: server.URL + "/proof", HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Submit(context.Background(), ch, "private-key"); err == nil || strings.Contains(fmt.Sprint(err), "private") {
				t.Fatal("unsafe response", err)
			}
			if calls.Load() != 1 {
				t.Fatal("request retried/redirected")
			}
		})
	}
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	c, _ := NewMigrationProofClient(MigrationProofConfig{IntegrationID: ch.IntegrationID, PrivateKey: key, ProofEndpoint: server.URL + "/proof", HTTPClient: server.Client()})
	for _, mode := range []string{"recipient", "kid", "digest", "expired", "future", "empty key", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			in := ch
			legacy := "legacy"
			ctx := context.Background()
			switch mode {
			case "recipient":
				in.IntegrationID = in.ProjectID
			case "kid":
				in.KeyID = "other"
			case "digest":
				in.RequestDigest = strings.Repeat("0", 64)
			case "expired":
				in.CreatedAt = in.CreatedAt.Add(-time.Hour)
				in.ExpiresAt = in.ExpiresAt.Add(-time.Hour)
			case "future":
				in.CreatedAt = in.CreatedAt.Add(time.Hour)
				in.ExpiresAt = in.ExpiresAt.Add(time.Hour)
			case "empty key":
				legacy = ""
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if mode != "digest" {
				in.RequestDigest = in.Digest()
			}
			if _, err := c.Submit(ctx, in, legacy); err == nil {
				t.Fatal("invalid request sent")
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("invalid context reached auth")
	}
	for _, endpoint := range []string{"http://auth/proof", "https://auth/proof?secret=x", "https://auth/proof#fragment", "https://user:pass@auth/proof"} {
		if _, err := NewMigrationProofClient(MigrationProofConfig{IntegrationID: ch.IntegrationID, PrivateKey: key, ProofEndpoint: endpoint}); err == nil {
			t.Fatal("unsafe issuer accepted")
		}
	}
}

func TestMigrationProofChallenge(t *testing.T) {
	_, ch := proofFixture()
	raw, _ := json.Marshal(ch)
	if got, err := DecodeMigrationChallenge(raw); err != nil || !got.Valid() {
		t.Fatal(err)
	}
	for _, invalid := range [][]byte{append([]byte(`{"protocol":"duplicate",`), raw[1:]...), bytes.Replace(raw, []byte(`"nonce"`), []byte(`"Nonce"`), 1), bytes.Replace(raw, []byte(`"sourceExpiresAt":null,`), nil, 1), append(raw, []byte(` {}`)...), []byte(`null`)} {
		if _, err := DecodeMigrationChallenge(invalid); err == nil {
			t.Fatal("ambiguous challenge accepted")
		}
	}
	if strings.Contains(fmt.Sprintf("%v %#v", ch, ch), ch.Nonce) {
		t.Fatal("challenge logged")
	}
}

func TestMigrationProofAuthContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/migration-proof.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Request MigrationProofRequest `json:"request"`
		Digest  string                `json:"digest"`
	}
	if json.Unmarshal(raw, &fixture) != nil || !fixture.Request.Valid() || fixture.Request.Digest() != fixture.Digest {
		t.Fatal("canonical auth contract changed")
	}
}
