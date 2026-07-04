package sign

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	body := []byte(`{"hello":"world"}`)
	ts := FormatTimestamp(time.Now())

	sig := Sign(priv, ts, body)
	if err := Verify(pub, ts, body, sig); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestVerifyRejectsTamperedBody(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	ts := FormatTimestamp(time.Now())
	sig := Sign(priv, ts, []byte("original"))

	if err := Verify(pub, ts, []byte("tampered"), sig); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("want ErrInvalidSignature, got %v", err)
	}
}

func TestSignerMatchesVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	s := NewSigner(priv)
	body := []byte("payload")
	now := time.Now()

	ts, sig := s.Sign(body, now)
	if ts != FormatTimestamp(now) {
		t.Fatalf("timestamp mismatch: %s vs %s", ts, FormatTimestamp(now))
	}
	if err := Verify(pub, ts, body, sig); err != nil {
		t.Fatalf("verify signer output: %v", err)
	}
}

func TestCheckTimestamp(t *testing.T) {
	now := time.Now()
	fresh := FormatTimestamp(now.Add(-time.Minute))
	if err := CheckTimestamp(fresh, DefaultTimestampWindow, now); err != nil {
		t.Fatalf("fresh timestamp rejected: %v", err)
	}
	stale := FormatTimestamp(now.Add(-10 * time.Minute))
	if err := CheckTimestamp(stale, DefaultTimestampWindow, now); !errors.Is(err, ErrStaleTimestamp) {
		t.Fatalf("want ErrStaleTimestamp, got %v", err)
	}
	if err := CheckTimestamp("not-a-number", DefaultTimestampWindow, now); !errors.Is(err, ErrMalformed) {
		t.Fatalf("want ErrMalformed, got %v", err)
	}
}

func TestParsePrivateKey(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)

	full := base64.StdEncoding.EncodeToString(priv)
	if got, err := ParsePrivateKey(full); err != nil || len(got) != ed25519.PrivateKeySize {
		t.Fatalf("full key: got len=%d err=%v", len(got), err)
	}

	seed := base64.StdEncoding.EncodeToString(priv.Seed())
	if got, err := ParsePrivateKey(seed); err != nil || len(got) != ed25519.PrivateKeySize {
		t.Fatalf("seed key: got len=%d err=%v", len(got), err)
	}

	if got, err := ParsePrivateKey(""); err != nil || got != nil {
		t.Fatalf("empty key: got=%v err=%v", got, err)
	}

	if _, err := ParsePrivateKey("!!!not base64"); !errors.Is(err, ErrMalformed) {
		t.Fatalf("want ErrMalformed for bad base64, got %v", err)
	}
}
