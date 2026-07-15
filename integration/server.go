package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/sign"
)

// defaultMaxInboundBody caps how much of an inbound request body is read before
// verification, so a hostile caller cannot exhaust memory.
const defaultMaxInboundBody = 1 << 20 // 1 MiB

// VerifierConfig configures a Verifier. JWKSURL points at the platform's
// well-known integration JWKS; an empty value falls back to DefaultJWKSURL
// (aheron.pro). Set it only for a non-default platform deployment.
type VerifierConfig struct {
	JWKSURL string
	// HTTPClient fetches the JWKS. Defaults to http.DefaultClient.
	HTTPClient *http.Client
	// Window is the timestamp freshness tolerance. Defaults to 5 minutes.
	Window time.Duration
	// CacheTTL is how long fetched keys are cached. Defaults to 5 minutes.
	CacheTTL time.Duration
	// MaxBodyBytes caps the inbound body read. Defaults to 1 MiB.
	MaxBodyBytes int64
	// Logger receives verification logs. Defaults to a no-op.
	Logger Logger
}

// Verifier authenticates inbound platform requests: it checks the Ed25519
// signature (against the platform JWKS, selected by kid) and the timestamp
// freshness before handing off to a handler. It is safe for concurrent use.
type Verifier struct {
	keys    *sign.KeySet
	window  time.Duration
	maxBody int64
	log     Logger
}

// NewVerifier builds a Verifier. An empty JWKSURL falls back to DefaultJWKSURL,
// so the minimal setup needs no configuration at all on aheron.pro.
func NewVerifier(cfg VerifierConfig) (*Verifier, error) {
	if cfg.JWKSURL == "" {
		cfg.JWKSURL = DefaultJWKSURL
	}
	window := cfg.Window
	if window <= 0 {
		window = sign.DefaultTimestampWindow
	}
	maxBody := cfg.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = defaultMaxInboundBody
	}
	log := cfg.Logger
	if log == nil {
		log = NopLogger()
	}
	return &Verifier{
		keys:    sign.NewKeySet(cfg.JWKSURL, cfg.HTTPClient, cfg.CacheTTL),
		window:  window,
		maxBody: maxBody,
		log:     log,
	}, nil
}

// verifiedContext is stashed on the request context by Verify so downstream
// decoders reuse the already-read, already-verified body.
type verifiedContext struct {
	body  []byte
	keyID string
}

type ctxKey int

const verifiedKey ctxKey = iota

// Verify is net/http middleware that authenticates the inbound request and, on
// success, calls next with the verified body available to DecodeBody. On any
// failure it writes 401 with a JSON error and does not call next.
func (v *Verifier) Verify(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, v.maxBody))
		if err != nil {
			v.reject(w, "read body", err)
			return
		}

		timestamp := r.Header.Get(sign.HeaderPlatformTimestamp)
		signature := r.Header.Get(sign.HeaderPlatformSignature)
		kid := r.Header.Get(sign.HeaderPlatformKeyID)
		if timestamp == "" || signature == "" {
			v.reject(w, "missing signature headers", nil)
			return
		}

		if err := sign.CheckTimestamp(timestamp, v.window, time.Now()); err != nil {
			v.reject(w, "timestamp", err)
			return
		}

		key, err := v.keys.Key(r.Context(), kid)
		if err != nil {
			v.reject(w, "select key", err)
			return
		}
		if err := sign.Verify(key, timestamp, body, signature); err != nil {
			v.reject(w, "verify signature", err)
			return
		}

		ctx := context.WithValue(r.Context(), verifiedKey, verifiedContext{body: body, keyID: kid})
		// Restore the body so a handler that reads r.Body directly still works.
		r2 := r.WithContext(ctx)
		r2.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r2)
	})
}

// Handler handles a verified inbound request. Returning nil yields 200; a
// non-nil error yields 500 (the platform leaves any parked step waiting and logs
// the failure).
type Handler func(ctx context.Context, r *http.Request) error

// InstallHandler handles a verified, decoded install request.
type InstallHandler func(ctx context.Context, req InstallRequest) error

// UninstallHandler handles a verified, decoded uninstall request.
type UninstallHandler func(ctx context.Context, req UninstallRequest) error

// TriggerSyncHandler handles a verified, decoded trigger-sync request.
type TriggerSyncHandler func(ctx context.Context, req TriggerSyncRequest) error

// Handle wraps fn with signature verification. The action request body is
// author-designed (see action_request_template), so fn reads it with DecodeBody
// into its own struct. Use it for a version's action_url endpoint.
func (v *Verifier) Handle(fn Handler) http.Handler {
	return v.Verify(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := fn(r.Context(), r); err != nil {
			v.log.Error("integration handler failed", LogF("error", err.Error()))
			writeJSONError(w, http.StatusInternalServerError, "handler failed")
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
}

// HandleInstall verifies the request, decodes the fixed install body and calls
// fn. Use it for the version's install_url endpoint. A nil error yields 200; a
// non-nil error yields 500.
func (v *Verifier) HandleInstall(fn InstallHandler) http.Handler {
	return v.Verify(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req InstallRequest
		if err := DecodeBody(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := fn(r.Context(), req); err != nil {
			v.log.Error("integration install handler failed", LogF("error", err.Error()))
			writeJSONError(w, http.StatusInternalServerError, "handler failed")
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
}

// HandleUninstall verifies the request, decodes the fixed uninstall body and
// calls fn. Use it for the integration's uninstall_url endpoint. A nil error
// yields 200; a non-nil error yields 500 so the platform redelivers.
func (v *Verifier) HandleUninstall(fn UninstallHandler) http.Handler {
	return v.Verify(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req UninstallRequest
		if err := DecodeBody(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := fn(r.Context(), req); err != nil {
			v.log.Error("integration uninstall handler failed", LogF("error", err.Error()))
			writeJSONError(w, http.StatusInternalServerError, "handler failed")
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
}

// HandleTriggerSync verifies the request, decodes the fixed trigger-sync body
// and calls fn. Use it for the integration's trigger_sync_url endpoint. A nil
// error yields 200; a non-nil error yields 500 so the platform redelivers.
func (v *Verifier) HandleTriggerSync(fn TriggerSyncHandler) http.Handler {
	return v.Verify(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req TriggerSyncRequest
		if err := DecodeBody(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := fn(r.Context(), req); err != nil {
			v.log.Error("integration trigger-sync handler failed", LogF("error", err.Error()))
			writeJSONError(w, http.StatusInternalServerError, "handler failed")
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
}

func (v *Verifier) reject(w http.ResponseWriter, stage string, err error) {
	if err != nil {
		v.log.Warn("integration inbound rejected", LogF("stage", stage), LogF("error", err.Error()))
	} else {
		v.log.Warn("integration inbound rejected", LogF("stage", stage))
	}
	writeJSONError(w, http.StatusUnauthorized, "signature verification failed")
}

// DecodeBody unmarshals the verified request body left by Verifier.Verify into
// dst. It errors if used outside a Verify chain. Embed integration.ExecutionContext
// in dst wherever the action_request_template references {{context}} so the
// decoded value can be passed to StepsClient.Resolve.
func DecodeBody(r *http.Request, dst any) error {
	vc, ok := r.Context().Value(verifiedKey).(verifiedContext)
	if !ok {
		return fmt.Errorf("integration: request was not verified (wrap the handler with Verifier.Verify)")
	}
	if err := json.Unmarshal(vc.body, dst); err != nil {
		return fmt.Errorf("integration: decode body: %w", err)
	}
	return nil
}

// VerifiedBody returns the raw verified request body left by Verifier.Verify and
// the platform signing key id that authenticated it. ok is false if used outside
// a Verify chain.
func VerifiedBody(r *http.Request) (body []byte, keyID string, ok bool) {
	vc, ok := r.Context().Value(verifiedKey).(verifiedContext)
	if !ok {
		return nil, "", false
	}
	return vc.body, vc.keyID, true
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
