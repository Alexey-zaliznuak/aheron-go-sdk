package sign

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"testing"
)

func TestDomainSignatureIsolation(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	domain, timestamp := "aheron.installation-lifecycle.v1", "1800000000"
	body := []byte(`{"action":"uninstall"}`)
	signature := SignDomain(priv, domain, timestamp, body)
	want := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(`aheron.installation-lifecycle.v1.1800000000.{"action":"uninstall"}`)))
	if signature != want {
		t.Fatal("domain signature wire format changed")
	}
	if err := VerifyDomain(pub, domain, timestamp, body, signature); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		Verify(pub, timestamp, body, signature),
		VerifyDomain(pub, domain, timestamp, body, Sign(priv, timestamp, body)),
		VerifyDomain(pub, "other", timestamp, body, signature),
		VerifyDomain(pub, domain, timestamp, []byte(`{"action":"install"}`), signature),
		VerifyDomain(pub, domain, "1800000001", body, signature),
	} {
		if !errors.Is(err, ErrInvalidSignature) {
			t.Fatalf("signature crossed context: %v", err)
		}
	}
}
