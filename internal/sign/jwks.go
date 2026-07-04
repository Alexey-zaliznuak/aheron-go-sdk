package sign

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// defaultJWKSTTL mirrors the Cache-Control max-age the backend sets on the
// well-known endpoint: cache keys for a few minutes, refresh on an unknown kid.
const defaultJWKSTTL = 5 * time.Minute

// maxJWKSBody caps how much of the JWKS response is read to avoid a hostile or
// misconfigured endpoint exhausting memory.
const maxJWKSBody = 1 << 20 // 1 MiB

// jwk is a single JSON Web Key describing an Ed25519 public key in OKP form
// (RFC 8037), matching what the backend publishes at
// GET /.well-known/aheron-integration-jwks.json.
type jwk struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Kid string `json:"kid"`
}

type jwks struct {
	Keys []jwk `json:"keys"`
}

// KeySet fetches and caches the platform's integration signing public keys from
// the well-known JWKS endpoint, selecting a key by its kid. It refreshes lazily:
// on a cache miss or when the cache is stale. It is safe for concurrent use.
type KeySet struct {
	url    string
	client *http.Client
	ttl    time.Duration

	mu        sync.RWMutex
	byKID     map[string]ed25519.PublicKey
	fetchedAt time.Time
}

// NewKeySet builds a KeySet for the given JWKS URL. A nil client falls back to
// http.DefaultClient; a non-positive ttl falls back to defaultJWKSTTL.
func NewKeySet(url string, client *http.Client, ttl time.Duration) *KeySet {
	if client == nil {
		client = http.DefaultClient
	}
	if ttl <= 0 {
		ttl = defaultJWKSTTL
	}
	return &KeySet{
		url:    url,
		client: client,
		ttl:    ttl,
		byKID:  map[string]ed25519.PublicKey{},
	}
}

// Key returns the public key for kid. It serves from cache when fresh, otherwise
// refreshes once. When kid is empty and exactly one key is known, that single
// key is returned (integrations running a single unlabeled key can verify
// without a kid). If a refresh fails but a matching key is already cached, the
// cached key is returned so a transient JWKS outage does not break verification.
func (k *KeySet) Key(ctx context.Context, kid string) (ed25519.PublicKey, error) {
	if key, ok := k.lookup(kid); ok && k.fresh() {
		return key, nil
	}
	if err := k.refresh(ctx); err != nil {
		if key, ok := k.lookup(kid); ok {
			return key, nil
		}
		return nil, err
	}
	if key, ok := k.lookup(kid); ok {
		return key, nil
	}
	return nil, fmt.Errorf("%w: no signing key for kid %q", ErrMalformed, kid)
}

func (k *KeySet) fresh() bool {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return !k.fetchedAt.IsZero() && time.Since(k.fetchedAt) < k.ttl
}

func (k *KeySet) lookup(kid string) (ed25519.PublicKey, bool) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	if kid == "" {
		if len(k.byKID) == 1 {
			for _, key := range k.byKID {
				return key, true
			}
		}
		return nil, false
	}
	key, ok := k.byKID[kid]
	return key, ok
}

func (k *KeySet) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.url, nil)
	if err != nil {
		return fmt.Errorf("build jwks request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := k.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch jwks: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch jwks: unexpected status %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSBody))
	if err != nil {
		return fmt.Errorf("read jwks: %w", err)
	}

	var set jwks
	if err := json.Unmarshal(raw, &set); err != nil {
		return fmt.Errorf("decode jwks: %w", err)
	}

	parsed := make(map[string]ed25519.PublicKey, len(set.Keys))
	for _, key := range set.Keys {
		if key.Kty != "OKP" || key.Crv != "Ed25519" {
			continue
		}
		decoded, err := base64.RawURLEncoding.DecodeString(key.X)
		if err != nil {
			return fmt.Errorf("decode jwks key %q: %w", key.Kid, err)
		}
		if len(decoded) != ed25519.PublicKeySize {
			return fmt.Errorf("jwks key %q is not a %d-byte Ed25519 key", key.Kid, ed25519.PublicKeySize)
		}
		parsed[key.Kid] = ed25519.PublicKey(decoded)
	}

	k.mu.Lock()
	k.byKID = parsed
	k.fetchedAt = time.Now()
	k.mu.Unlock()
	return nil
}
