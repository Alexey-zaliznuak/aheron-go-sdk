package sign

import (
	"crypto/ed25519"
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
