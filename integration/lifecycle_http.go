package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"unicode/utf8"
)

const maxLifecycleBody = 16 << 10

// LifecycleHandler must atomically persist the decision from DecideLifecycle
// with the credential effect and finish required cleanup before returning its
// receipt. Do not call legacy project-wide install/uninstall handlers here.
type LifecycleHandler func(context.Context, LifecycleRequest) (LifecycleReceipt, error)

// HandleLifecycle serves a NEW dedicated POST endpoint. Binding the expected
// integration ID is mandatory: platform signatures alone identify the sender,
// not the intended recipient. Never register this on legacy install/uninstall
// URLs or advertise it before durable watermark/credential handling is ready.
func (v *Verifier) HandleLifecycle(integrationID string, fn LifecycleHandler) (http.Handler, error) {
	return v.handleLifecycle(integrationID, fn, false)
}

// HandleOAuthLifecycle opts a durable receiver into OAuth installation settings.
// Use only after its repository atomically stores/clears OAuth and legacy state
// with the watermark and its runtime reads OAuth without falling back to a key.
// The original HandleLifecycle deliberately rejects OAuth after an SDK upgrade.
func (v *Verifier) HandleOAuthLifecycle(integrationID string, fn LifecycleHandler) (http.Handler, error) {
	return v.handleLifecycle(integrationID, fn, true)
}

func (v *Verifier) handleLifecycle(integrationID string, fn LifecycleHandler, oauth bool) (http.Handler, error) {
	if !lifecycleValidID(integrationID) || fn == nil {
		return nil, ErrLifecycleInvalid
	}
	verified := v.verify(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _, _ := VerifiedBody(r)
		var req LifecycleRequest
		if err := decodeLifecycleObject(body, &req, lifecycleRequestFields); err != nil || req.Validate() != nil || req.OAuth != nil && !oauth {
			writeJSONError(w, http.StatusBadRequest, "invalid lifecycle request")
			return
		}
		if req.IntegrationID != integrationID {
			writeJSONError(w, http.StatusForbidden, "wrong lifecycle recipient")
			return
		}
		receipt, err := fn(r.Context(), req)
		if err != nil {
			status, code := http.StatusServiceUnavailable, "lifecycle processing unavailable"
			if errors.Is(err, ErrLifecycleConflict) {
				status, code = http.StatusConflict, "lifecycle conflict"
			}
			// Storage errors can contain credentials. Log only classification and
			// stable identifiers; storage instrumentation owns sanitized details.
			v.log.Error("integration lifecycle processing failed", LogF("eventId", req.EventID), LogF("installationId", req.InstallationID), LogF("errorClass", code))
			writeJSONError(w, status, code)
			return
		}
		if receipt.ValidateFor(req) != nil {
			v.log.Error("integration lifecycle receipt invalid", LogF("eventId", req.EventID))
			writeJSONError(w, http.StatusServiceUnavailable, "lifecycle receipt unavailable")
			return
		}
		writeJSON(w, http.StatusOK, receipt)
	}), LifecycleProtocol)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSONError(w, http.StatusMethodNotAllowed, "POST required")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxLifecycleBody)
		verified.ServeHTTP(w, r)
	}), nil
}

// Token-by-token parsing rejects duplicate,
// unknown, mis-cased, null and missing fields before struct unmarshalling can
// silently discard them. OAuth settings have their own strict nested decoder.
// The optional credential must be absent when unused.
var lifecycleRequestFields = map[string]bool{"protocol": true, "eventId": true, "integrationId": true, "projectId": true, "installationId": true, "sequence": true, "action": true, "projectApiKey": false, "oauth": false}
var lifecycleReceiptFields = map[string]bool{"protocol": true, "eventId": true, "integrationId": true, "projectId": true, "installationId": true, "sequence": true, "requestDigest": true, "outcome": true, "observedSequence": true}

func decodeLifecycleObject(body []byte, dst any, fields map[string]bool) error {
	if len(body) > maxLifecycleBody || !utf8.Valid(body) {
		return ErrLifecycleInvalid
	}
	d := json.NewDecoder(bytes.NewReader(body))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return ErrLifecycleInvalid
	}
	seen := make(map[string]bool, len(fields))
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return ErrLifecycleInvalid
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return ErrLifecycleInvalid
		}
		if _, ok := fields[key]; !ok {
			return ErrLifecycleInvalid
		}
		seen[key] = true
		var raw json.RawMessage
		if d.Decode(&raw) != nil || string(raw) == "null" {
			return ErrLifecycleInvalid
		}
		if key == "projectApiKey" && string(raw) == `""` {
			return ErrLifecycleInvalid
		}
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return ErrLifecycleInvalid
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return ErrLifecycleInvalid
	}
	for field, required := range fields {
		if required && !seen[field] {
			return ErrLifecycleInvalid
		}
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return ErrLifecycleInvalid
	}
	return nil
}
