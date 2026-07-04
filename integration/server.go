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

// VerifierConfig configures a Verifier. JWKSURL is required and must point at the
// platform's well-known integration JWKS (for aheron.pro that is
// https://aheron.pro/.well-known/aheron-integration-jwks.json).
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

// NewVerifier builds a Verifier. It returns an error when JWKSURL is empty.
func NewVerifier(cfg VerifierConfig) (*Verifier, error) {
	if cfg.JWKSURL == "" {
		return nil, fmt.Errorf("integration: VerifierConfig.JWKSURL is required")
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
// success, calls next with the verified body available to DecodeAction. On any
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

// HandleAction is a convenience http.Handler that verifies the request, decodes
// it as a "block.action" and calls fn. A nil error from fn yields 200; a non-nil
// error yields 500 (the platform leaves the step parked and logs the failure).
// It rejects a non-action type with 400.
func (v *Verifier) HandleAction(fn ActionHandler) http.Handler {
	return v.Verify(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, err := DecodeAction(r)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := fn(r.Context(), req); err != nil {
			v.log.Error("integration action handler failed", LogF("error", err.Error()))
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

// ActionHandler handles a verified inbound integrationAction request.
type ActionHandler func(ctx context.Context, req ActionRequest) error

// DecodeAction reads the verified body left by Verifier.Verify and decodes it as
// a "block.action" envelope. It errors if used outside a Verify chain or if the
// envelope type is not TypeAction.
func DecodeAction(r *http.Request) (ActionRequest, error) {
	vc, ok := r.Context().Value(verifiedKey).(verifiedContext)
	if !ok {
		return ActionRequest{}, fmt.Errorf("integration: request was not verified (wrap the handler with Verifier.Verify)")
	}
	var env Envelope
	if err := json.Unmarshal(vc.body, &env); err != nil {
		return ActionRequest{}, fmt.Errorf("integration: decode envelope: %w", err)
	}
	if env.Type != TypeAction {
		return ActionRequest{}, fmt.Errorf("integration: expected type %q, got %q", TypeAction, env.Type)
	}
	var payload ActionPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return ActionRequest{}, fmt.Errorf("integration: decode action payload: %w", err)
	}
	return ActionRequest{Type: env.Type, KeyID: vc.keyID, Payload: payload}, nil
}

// Mux dispatches verified inbound requests by envelope type onto registered
// handlers. Use it when a single endpoint serves several inbound types; wrap it
// with Verifier.Verify. For the common one-block-per-endpoint case, prefer
// Verifier.HandleAction.
type Mux struct {
	action ActionHandler
	log    Logger
}

// NewMux returns an empty Mux.
func NewMux() *Mux { return &Mux{log: NopLogger()} }

// OnAction registers the handler for "block.action" requests. The last
// registration wins.
func (m *Mux) OnAction(fn ActionHandler) *Mux {
	m.action = fn
	return m
}

// ServeHTTP dispatches a verified request. It must be wrapped with
// Verifier.Verify so the verified body is available.
func (m *Mux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	vc, ok := r.Context().Value(verifiedKey).(verifiedContext)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "request was not verified")
		return
	}
	var env Envelope
	if err := json.Unmarshal(vc.body, &env); err != nil {
		writeJSONError(w, http.StatusBadRequest, "decode envelope")
		return
	}
	switch env.Type {
	case TypeAction:
		if m.action == nil {
			writeJSONError(w, http.StatusNotImplemented, "no handler for block.action")
			return
		}
		req, err := DecodeAction(r)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := m.action(r.Context(), req); err != nil {
			m.log.Error("integration action handler failed", LogF("error", err.Error()))
			writeJSONError(w, http.StatusInternalServerError, "handler failed")
			return
		}
		w.WriteHeader(http.StatusOK)
	default:
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("unknown inbound type %q", env.Type))
	}
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
