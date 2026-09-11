package sign

import (
	"crypto/ed25519"
	"encoding/base64"
)

// SignDomain signs "<domain>.<timestamp>.<body>". Domain is a fixed protocol
// constant chosen by the caller, never inferred from untrusted message fields.
// Placing it BEFORE the numeric timestamp distinguishes this signature from
// every legacy signature, including legacy signatures over arbitrary bodies.
func SignDomain(priv ed25519.PrivateKey, domain, timestamp string, body []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, domainInput(domain, timestamp, body)))
}

func VerifyDomain(pub ed25519.PublicKey, domain, timestamp string, body []byte, signatureB64 string) error {
	return verifyInput(pub, domainInput(domain, timestamp, body), signatureB64)
}

func domainInput(domain, timestamp string, body []byte) []byte {
	input := make([]byte, 0, len(domain)+len(timestamp)+len(body)+2)
	input = append(input, domain...)
	input = append(input, '.')
	input = append(input, timestamp...)
	input = append(input, '.')
	return append(input, body...)
}
