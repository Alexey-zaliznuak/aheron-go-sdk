package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/sign"
	"io"
	"net/http"
	"strconv"
	"time"
)

const DefaultLinkJWKSURL = "https://link.aheron.pro/.well-known/link-jwks.json"

// LinkEvent identifies a request to a short URL, not verified human identity or destination page load.
type LinkEvent struct {
	ID         string          `json:"id"`
	Data       json.RawMessage `json:"data,omitempty"`
	EventID    string          `json:"-"`
	OccurredAt time.Time       `json:"-"`
}
type LinkVerifier struct {
	keys   *sign.KeySet
	window time.Duration
}

// NewLinkVerifier uses a separate link-service JWKS and signature domain. Existing platform callbacks are unaffected.
func NewLinkVerifier(cfg VerifierConfig) *LinkVerifier {
	if cfg.JWKSURL == "" {
		cfg.JWKSURL = DefaultLinkJWKSURL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
	if cfg.Window <= 0 {
		cfg.Window = 5 * time.Minute
	}
	return &LinkVerifier{sign.NewKeySet(cfg.JWKSURL, cfg.HTTPClient, cfg.CacheTTL), cfg.Window}
}
func (v *LinkVerifier) Verify(ctx context.Context, r *http.Request) (LinkEvent, error) {
	var out LinkEvent
	if r.Method != "POST" {
		return out, fmt.Errorf("links: callback must be POST")
	}
	body, e := io.ReadAll(io.LimitReader(r.Body, (32<<10)+1))
	if e != nil || len(body) > 32<<10 {
		return out, fmt.Errorf("links: invalid callback size")
	}
	ts, event, at := r.Header.Get(sign.HeaderPlatformTimestamp), r.Header.Get("Idempotency-Key"), r.Header.Get("X-Aheron-Link-Occurred-At")
	secs, e := strconv.ParseInt(ts, 10, 64)
	now := time.Now()
	if e != nil || time.Unix(secs, 0).Before(now.Add(-v.window)) || time.Unix(secs, 0).After(now.Add(v.window)) {
		return out, fmt.Errorf("links: stale callback")
	}
	if !linkUUIDPattern.MatchString(event) {
		return out, fmt.Errorf("links: invalid eventId")
	}
	occurred, e := time.Parse(time.RFC3339Nano, at)
	if e != nil {
		return out, fmt.Errorf("links: invalid occurredAt")
	}
	key, e := v.keys.Key(ctx, r.Header.Get(sign.HeaderPlatformKeyID))
	if e != nil {
		return out, e
	}
	canonical := append([]byte(fmt.Sprintf("links-callback-v1\n%s\n%s\n", event, at)), body...)
	if e = sign.Verify(key, ts, canonical, r.Header.Get(sign.HeaderPlatformSignature)); e != nil {
		return out, e
	}
	if e = json.Unmarshal(body, &out); e != nil || !linkIDPattern.MatchString(out.ID) {
		return out, fmt.Errorf("links: invalid callback JSON")
	}
	out.EventID = event
	out.OccurredAt = occurred
	return out, nil
}

// Handle verifies and decodes the callback. fn must durably deduplicate EventID together with its business effect.
// Returning nil acknowledges with 204; an error returns 503 so link-service retries.
func (v *LinkVerifier) Handle(fn func(context.Context, LinkEvent) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		event, e := v.Verify(r.Context(), r)
		if e != nil {
			http.Error(w, "invalid link callback", 401)
			return
		}
		if e = fn(r.Context(), event); e != nil {
			http.Error(w, "callback processing failed", 503)
			return
		}
		w.WriteHeader(204)
	})
}
