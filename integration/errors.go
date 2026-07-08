package integration

import (
	"errors"
	"net/http"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/httpclient"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/sign"
)

// APIError is returned when the platform responds with a non-2xx status. It
// carries the HTTP status, the extracted error message and the raw body.
type APIError = httpclient.APIError

// IsUnauthorized reports whether err is an *APIError carrying a 401 or 403
// status, i.e. the platform rejected the caller's credentials (a project API key
// or a request signature). It lets callers branch without inspecting APIError
// directly.
func IsUnauthorized(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden
	}
	return false
}

// StatusCode returns the HTTP status carried by an *APIError, or 0 when err is
// not an *APIError. It is a convenience for callers that need to branch on the
// exact status (for example treating 409 as "already exists").
func StatusCode(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	return 0
}

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
