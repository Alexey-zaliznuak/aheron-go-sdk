// Package sign implements the Ed25519 signing and verification used by the
// Aheron integrations trust model. It is the SDK-side mirror of the platform's
// execution-service/internal/integrationsig package and MUST stay byte-for-byte
// compatible with it.
//
// The trust model is fully asymmetric (there is no shared secret):
//
//   - Inbound (platform -> integration): the platform signs its outgoing request
//     with the platform private key and sends X-Aheron-Timestamp /
//     X-Aheron-Signature (+ optional X-Aheron-Key-Id). The integration verifies
//     it with the platform public key published as a JWKS (see KeySet).
//   - Outbound (integration -> platform): the integration signs its callback
//     (resolve / trigger activation / listing) with its OWN private key and sends
//     X-Integration-Id / X-Integration-Timestamp / X-Integration-Signature. The
//     platform verifies it with the integration's registered public key.
//
// The signature is always Ed25519 over the canonical bytes "<timestamp>.<body>",
// where timestamp is the unix-seconds string carried in the matching header and
// body is the exact raw request body. Both sides also check timestamp freshness
// against a narrow window to blunt replay.
package sign

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Header names for both directions of the trust model. They match the constants
// in execution-service/internal/integrationsig.
const (
	// HeaderPlatformTimestamp / HeaderPlatformSignature carry the platform's
	// signature on an inbound request to an integration.
	HeaderPlatformTimestamp = "X-Aheron-Timestamp"
	HeaderPlatformSignature = "X-Aheron-Signature"
	// HeaderPlatformKeyID carries the kid of the platform signing key so the
	// integration can select the matching public key from the platform JWKS.
	HeaderPlatformKeyID = "X-Aheron-Key-Id"

	// HeaderIntegrationID / HeaderIntegrationTimestamp / HeaderIntegrationSignature
	// carry an integration's signature on an outbound callback to the platform.
	HeaderIntegrationID        = "X-Integration-Id"
	HeaderIntegrationTimestamp = "X-Integration-Timestamp"
	HeaderIntegrationSignature = "X-Integration-Signature"
)

// DefaultTimestampWindow is the replay-protection window applied to a timestamp:
// a request whose timestamp is more than this far from now (in either direction)
// is rejected.
const DefaultTimestampWindow = 5 * time.Minute

var (
	// ErrInvalidSignature means the signature did not verify against the key.
	ErrInvalidSignature = errors.New("invalid signature")
	// ErrStaleTimestamp means the timestamp is outside the freshness window.
	ErrStaleTimestamp = errors.New("stale timestamp")
	// ErrMalformed means the signature material (base64, key length, timestamp
	// format) could not be parsed.
	ErrMalformed = errors.New("malformed signature material")
)

// signingInput builds the canonical bytes that are signed and verified:
// "<timestamp>.<body>". Keeping it in one place guarantees signer and verifier
// agree byte-for-byte.
func signingInput(timestamp string, body []byte) []byte {
	out := make([]byte, 0, len(timestamp)+1+len(body))
	out = append(out, timestamp...)
	out = append(out, '.')
	out = append(out, body...)
	return out
}

// Sign returns the base64 (std encoding) Ed25519 signature of
// "<timestamp>.<body>" under priv.
func Sign(priv ed25519.PrivateKey, timestamp string, body []byte) string {
	sig := ed25519.Sign(priv, signingInput(timestamp, body))
	return base64.StdEncoding.EncodeToString(sig)
}

// Verify checks that signatureB64 is a valid Ed25519 signature of
// "<timestamp>.<body>" under pub. It returns ErrMalformed for unparseable
// material and ErrInvalidSignature when the signature does not verify.
func Verify(pub ed25519.PublicKey, timestamp string, body []byte, signatureB64 string) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: public key must be %d bytes, got %d", ErrMalformed, ed25519.PublicKeySize, len(pub))
	}
	sig, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil {
		return fmt.Errorf("%w: signature base64: %v", ErrMalformed, err)
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("%w: signature must be %d bytes, got %d", ErrMalformed, ed25519.SignatureSize, len(sig))
	}
	if !ed25519.Verify(pub, signingInput(timestamp, body), sig) {
		return ErrInvalidSignature
	}
	return nil
}

// FormatTimestamp renders t as the unix-seconds decimal string used in the
// signature headers.
func FormatTimestamp(t time.Time) string {
	return strconv.FormatInt(t.Unix(), 10)
}

// CheckTimestamp verifies that the unix-seconds timestamp string is within
// ±window of now. An unparseable timestamp is ErrMalformed; a timestamp outside
// the window is ErrStaleTimestamp.
func CheckTimestamp(timestamp string, window time.Duration, now time.Time) error {
	secs, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: timestamp %q is not unix seconds: %v", ErrMalformed, timestamp, err)
	}
	delta := now.Sub(time.Unix(secs, 0))
	if delta < 0 {
		delta = -delta
	}
	if delta > window {
		return fmt.Errorf("%w: timestamp is %s away from now", ErrStaleTimestamp, delta)
	}
	return nil
}

// ParsePrivateKey decodes a base64 (std encoding) Ed25519 private key. It accepts
// either a full 64-byte private key or a 32-byte seed (from which the full key is
// derived). An empty string yields a nil key and no error so callers can treat
// "key not configured" as a soft state.
func ParsePrivateKey(b64 string) (ed25519.PrivateKey, error) {
	if b64 == "" {
		return nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("%w: private key base64: %v", ErrMalformed, err)
	}
	switch len(raw) {
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(raw), nil
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	default:
		return nil, fmt.Errorf("%w: private key must be %d or %d bytes, got %d", ErrMalformed, ed25519.PrivateKeySize, ed25519.SeedSize, len(raw))
	}
}

// Signer timestamps and signs an outgoing request body with an Ed25519 private
// key in one call. It is safe for concurrent use.
type Signer struct {
	priv ed25519.PrivateKey
}

// NewSigner wraps a private key. priv must be a valid Ed25519 private key.
func NewSigner(priv ed25519.PrivateKey) *Signer {
	return &Signer{priv: priv}
}

// Sign produces the timestamp/signature pair for body at time now. The returned
// timestamp must be sent in the timestamp header and signature in the signature
// header so the verifier reconstructs the same canonical input.
func (s *Signer) Sign(body []byte, now time.Time) (timestamp string, signature string) {
	timestamp = FormatTimestamp(now)
	signature = Sign(s.priv, timestamp, body)
	return timestamp, signature
}
