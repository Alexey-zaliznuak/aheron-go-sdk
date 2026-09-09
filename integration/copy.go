package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/schemetransfer"
)

var (
	ErrCopyUnsupportedVersion = errors.New("unsupported copy integration version")
	ErrCopyUnknownSource      = errors.New("unknown copy resource source")
	ErrCopyUnknownBlock       = errors.New("unknown copy block")
	ErrCopyInvalidRequest     = errors.New("invalid copy callback request")
	ErrCopyUnavailable        = errors.New("copy callback unavailable")
)

// CopyVersionGuard must explicitly accept the pinned catalog version. It may
// consult the integration's registration result or a supported-version registry;
// silently interpreting old settings as the current version is not allowed.
// A nil guard fails closed. Return ErrCopyUnsupportedVersion for unknown versions.
type CopyVersionGuard func(context.Context, int) error

type ResourceValuesHandler func(context.Context, schemetransfer.LookupRequest) (schemetransfer.LookupResponse, error)
type IdentifyResourcesHandler func(context.Context, schemetransfer.LookupRequest) (schemetransfer.IdentifyResponse, error)
type PrepareCopyHandler func(context.Context, schemetransfer.PrepareCopyRequest) (schemetransfer.PrepareCopyResponse, error)
type ValidateCopySettingsHandler func(context.Context, schemetransfer.ValidateCopySettingsRequest) (schemetransfer.ValidationResult, error)

type ResourceValuesHandlers struct {
	// Lookup serves search and resolve; Identify serves only identify. All
	// callbacks must scope reads to ProjectID and enforce source constraints.
	Lookup   ResourceValuesHandler
	Identify IdentifyResourcesHandler
}

// CopyCallbackError is a sanitized transport error, distinct from successful
// domain validation results (blocked/needsReview are HTTP 200).
type CopyCallbackError struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

// HandleResourceValues serves the manifest's resourceValuesPath. Callbacks are
// read-only: they must not execute blocks or create integration resources.
func (v *Verifier) HandleResourceValues(guard CopyVersionGuard, handlers ResourceValuesHandlers) http.Handler {
	return v.handleCopy(schemetransfer.ResourceValues, guard, func(ctx context.Context, raw []byte) (any, error) {
		var req schemetransfer.LookupRequest
		_ = json.Unmarshal(raw, &req)
		if req.Mode == "identify" {
			if handlers.Identify == nil {
				return nil, ErrCopyUnavailable
			}
			return handlers.Identify(ctx, req)
		}
		if handlers.Lookup == nil {
			return nil, ErrCopyUnavailable
		}
		return handlers.Lookup(ctx, req)
	})
}

// HandlePrepareCopy makes implicit source settings explicit before export. It
// must not save settings, run an action or disclose credentials in its response.
func (v *Verifier) HandlePrepareCopy(guard CopyVersionGuard, fn PrepareCopyHandler) http.Handler {
	return v.handleCopy(schemetransfer.PrepareCopy, guard, func(ctx context.Context, raw []byte) (any, error) {
		if fn == nil {
			return nil, ErrCopyUnavailable
		}
		var req schemetransfer.PrepareCopyRequest
		_ = json.Unmarshal(raw, &req)
		return fn(ctx, req)
	})
}

// HandleValidateCopySettings checks restored target settings without executing
// the block. Incompatibility belongs in ValidationResult.Issues, not in an error.
func (v *Verifier) HandleValidateCopySettings(guard CopyVersionGuard, fn ValidateCopySettingsHandler) http.Handler {
	return v.handleCopy(schemetransfer.ValidateCopySettings, guard, func(ctx context.Context, raw []byte) (any, error) {
		if fn == nil {
			return nil, ErrCopyUnavailable
		}
		var req schemetransfer.ValidateCopySettingsRequest
		_ = json.Unmarshal(raw, &req)
		return fn(ctx, req)
	})
}

func (v *Verifier) handleCopy(operation schemetransfer.CopyOperation, guard CopyVersionGuard, fn func(context.Context, []byte) (any, error)) http.Handler {
	return v.Verify(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeCopyError(w, http.StatusMethodNotAllowed, "invalidRequest", "POST is required")
			return
		}
		raw, _, _ := VerifiedBody(r)
		if err := schemetransfer.ValidateCallbackRequest(operation, raw); err != nil {
			writeCopyError(w, http.StatusBadRequest, "invalidRequest", "invalid copy request")
			return
		}
		if guard == nil {
			v.copyFailure(w, ErrCopyUnavailable)
			return
		}
		var metadata struct {
			IntegrationVersion int `json:"integrationVersion"`
		}
		_ = json.Unmarshal(raw, &metadata)
		if err := guard(r.Context(), metadata.IntegrationVersion); err != nil {
			v.copyFailure(w, err)
			return
		}
		result, err := fn(r.Context(), raw)
		if err != nil {
			v.copyFailure(w, err)
			return
		}
		response, err := json.Marshal(result)
		if err == nil {
			err = schemetransfer.ValidateCallbackResponse(operation, raw, response)
		}
		if err != nil {
			// Never log a callback error or a marshal error verbatim: it may
			// contain settings, credentials or user-entered field values.
			v.log.Error("integration copy callback returned invalid response", LogF("operation", string(operation)))
			writeCopyError(w, http.StatusInternalServerError, "invalidResponse", "invalid copy response")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(response)
	}))
}

func (v *Verifier) copyFailure(w http.ResponseWriter, err error) {
	status, code, message := http.StatusInternalServerError, "callbackFailed", "copy callback failed"
	switch {
	case errors.Is(err, ErrCopyUnsupportedVersion):
		status, code, message = http.StatusConflict, "unsupportedVersion", "unsupported integration version"
	case errors.Is(err, ErrCopyUnknownSource):
		status, code, message = http.StatusNotFound, "unknownSource", "unknown resource source"
	case errors.Is(err, ErrCopyUnknownBlock):
		status, code, message = http.StatusNotFound, "unknownBlock", "unknown block"
	case errors.Is(err, ErrCopyInvalidRequest):
		status, code, message = http.StatusBadRequest, "invalidRequest", "invalid copy request"
	case errors.Is(err, ErrCopyUnavailable):
		status, code, message = http.StatusServiceUnavailable, "unavailable", "copy callback unavailable"
	}
	v.log.Error("integration copy callback failed", LogF("code", code))
	writeCopyError(w, status, code, message)
}

func writeCopyError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, CopyCallbackError{Code: code, Error: message})
}
