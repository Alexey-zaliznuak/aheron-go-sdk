package integration

import (
	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/httpclient"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/sign"
)

// APIError is returned when the platform responds with a non-2xx status. It
// carries the HTTP status, the extracted error message and the raw body.
type APIError = httpclient.APIError

// Signature/verification error sentinels, re-exported so callers can branch with
// errors.Is without importing an internal package.
var (
	// ErrInvalidSignature means an inbound platform signature did not verify.
	ErrInvalidSignature = sign.ErrInvalidSignature
	// ErrStaleTimestamp means an inbound request's timestamp is outside the
	// freshness window (likely a replay or a badly skewed clock).
	ErrStaleTimestamp = sign.ErrStaleTimestamp
	// ErrMalformed means signature material (headers, key, timestamp) could not
	// be parsed.
	ErrMalformed = sign.ErrMalformed
)
