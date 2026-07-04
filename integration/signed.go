package integration

import (
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/httpclient"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/sign"
)

// buildSignedRequest assembles an httpclient.Request authenticated with the
// integration's own key: it signs "<timestamp>.<body>" (body is the empty slice
// for bodiless GETs) and sets the X-Integration-Id / X-Integration-Timestamp /
// X-Integration-Signature headers. The signature is computed here, once, over
// the exact bytes that are sent; retries resend the same bytes and finish well
// within the freshness window, so no per-attempt re-signing is needed.
func buildSignedRequest(signer *sign.Signer, id, method, path string, query map[string]string, body []byte, idempotent bool) (httpclient.Request, error) {
	if signer == nil {
		return httpclient.Request{}, errNoSigner
	}
	signBody := body
	if signBody == nil {
		signBody = []byte{}
	}
	ts, sig := signer.Sign(signBody, time.Now())
	headers := map[string]string{
		sign.HeaderIntegrationID:        id,
		sign.HeaderIntegrationTimestamp: ts,
		sign.HeaderIntegrationSignature: sig,
	}
	return httpclient.Request{
		Method:     method,
		Path:       path,
		Query:      query,
		Headers:    headers,
		Body:       body,
		Idempotent: idempotent,
	}, nil
}
