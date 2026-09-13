// Package integrationoauth implements Aheron's installation and application OAuth profiles.
// A Provider owns one registered client/key in one environment; it does not
// register clients, grant permissions, or fall back to legacy credentials.
package integrationoauth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrConfig   = errors.New("integration OAuth: invalid configuration")
	ErrRequest  = errors.New("integration OAuth: invalid request")
	ErrCapacity = errors.New("integration OAuth: too many concurrent token requests")
)

// Config is immutable after NewProvider. PrivateKey must be a full Ed25519 key.
// Load it through the application's secret configuration, never from an API call.
type Config struct {
	ClientID   string
	KeyID      string
	PrivateKey ed25519.PrivateKey
	// TokenEndpoint is the exact external HTTPS URL used both for POST and JWT aud.
	TokenEndpoint string
	// HTTPClient is copied; cookies and redirects are disabled. Its transport
	// remains caller-owned and must be safe for concurrent use and credentials.
	HTTPClient *http.Client
	// MaxEntries bounds each of the token cache and simultaneous distinct
	// acquisitions. Default 1024, maximum 10000. Idle tokens are evicted by LRU.
	MaxEntries int
}

func (Config) String() string   { return "[redacted OAuth configuration]" }
func (Config) GoString() string { return "[redacted OAuth configuration]" }

// Request identifies exactly one installation and one resource permission set.
// IDs come from the trusted installation binding; never infer them from a token.
type Request struct {
	ProjectID      string
	InstallationID string
	Audience       string
	Scopes         []string
}

// Token keeps the credential private to avoid accidental JSON/fmt disclosure.
// Bearer returns the secret explicitly for an Authorization header.
type Token struct {
	value     string
	expiresAt time.Time
	scope     string
}

func (t Token) Bearer() string       { return t.value }
func (t Token) ExpiresAt() time.Time { return t.expiresAt }
func (t Token) Scope() string        { return t.scope }
func (Token) String() string         { return "[redacted OAuth token]" }
func (Token) GoString() string       { return "[redacted OAuth token]" }

type cacheKey struct{ project, installation, audience, scopes, kind string }
type cacheEntry struct {
	token     Token
	refreshAt time.Time
	used      uint64
}
type flight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	token   Token
	err     error
}

// Provider is safe for concurrent use. Share it across installation clients.
// Refresh is lazy, with early expiry and jitter, not a background renewal loop.
// Failed refreshes never return a cached token or a legacy credential.
type Provider struct {
	clientID, keyID, endpoint string
	privateKey                ed25519.PrivateKey
	http                      *http.Client
	maxEntries                int
	now                       func() time.Time
	mu                        sync.Mutex
	cache                     map[cacheKey]cacheEntry
	flights                   map[cacheKey]*flight
	sequence                  uint64
}

func (*Provider) String() string   { return "[redacted OAuth provider]" }
func (*Provider) GoString() string { return "[redacted OAuth provider]" }

func NewProvider(cfg Config) (*Provider, error) {
	if !canonicalUUID(cfg.ClientID) || !asciiIdentifier(cfg.KeyID, 128) ||
		len(cfg.PrivateKey) != ed25519.PrivateKeySize ||
		!bytes.Equal(cfg.PrivateKey, ed25519.NewKeyFromSeed(cfg.PrivateKey.Seed())) {
		return nil, ErrConfig
	}
	u, err := url.Parse(cfg.TokenEndpoint)
	if err != nil || !httpsURL(u) || u.Path == "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(cfg.TokenEndpoint, "#") || len(cfg.TokenEndpoint) > 2048 {
		return nil, ErrConfig
	}
	if cfg.MaxEntries == 0 {
		cfg.MaxEntries = 1024
	}
	if cfg.MaxEntries < 1 || cfg.MaxEntries > 10000 {
		return nil, ErrConfig
	}
	client := boundedClient(cfg.HTTPClient)
	if client.Timeout > 30*time.Second {
		return nil, ErrConfig
	}
	return &Provider{clientID: cfg.ClientID, keyID: cfg.KeyID, endpoint: cfg.TokenEndpoint,
		privateKey: slices.Clone(cfg.PrivateKey), http: client, maxEntries: cfg.MaxEntries,
		now: time.Now, cache: make(map[cacheKey]cacheEntry), flights: make(map[cacheKey]*flight)}, nil
}

func (p *Provider) Token(ctx context.Context, req Request) (Token, error) {
	if p == nil {
		return Token{}, ErrConfig
	}
	key, err := requestKey(req)
	if err != nil {
		return Token{}, err
	}
	return p.token(ctx, key)
}

func (p *Provider) token(ctx context.Context, key cacheKey) (Token, error) {
	if p == nil {
		return Token{}, ErrConfig
	}
	if err := ctx.Err(); err != nil {
		return Token{}, err
	}
	p.mu.Lock()
	if cached, ok := p.cache[key]; ok && p.now().Before(cached.refreshAt) {
		p.sequence++
		cached.used = p.sequence
		p.cache[key] = cached
		p.mu.Unlock()
		return cached.token, nil
	}
	delete(p.cache, key)
	f := p.flights[key]
	if f == nil {
		if len(p.flights) >= p.maxEntries {
			p.mu.Unlock()
			return Token{}, ErrCapacity
		}
		// One caller's cancellation must not abort other waiters. The acquisition
		// has its own deadline and is cancelled when its last waiter leaves.
		acquireCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.http.Timeout)
		f = &flight{done: make(chan struct{}), cancel: cancel}
		p.flights[key] = f
		go p.acquire(acquireCtx, key, f)
	}
	f.waiters++
	p.mu.Unlock()
	select {
	case <-ctx.Done():
		p.mu.Lock()
		f.waiters--
		if f.waiters == 0 && p.flights[key] == f {
			delete(p.flights, key)
			f.cancel()
		}
		p.mu.Unlock()
		return Token{}, ctx.Err()
	case <-f.done:
		if err := ctx.Err(); err != nil {
			return Token{}, err
		}
		if f.err == nil && !p.now().Before(f.token.expiresAt) {
			return Token{}, &TokenError{Code: "expired_response"}
		}
		return f.token, f.err
	}
}

// Invalidate removes only the rejected token. A late 401 cannot evict a newer
// token issued concurrently. It does not revoke tokens in auth-service.
func (p *Provider) Invalidate(req Request, rejected Token) {
	if p == nil || rejected.value == "" {
		return
	}
	key, err := requestKey(req)
	if err != nil {
		return
	}
	p.invalidate(key, rejected)
}

func (p *Provider) invalidate(key cacheKey, rejected Token) {
	if p == nil || rejected.value == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if cached, ok := p.cache[key]; ok && cached.token.value == rejected.value {
		delete(p.cache, key)
	}
}

func (p *Provider) acquire(ctx context.Context, key cacheKey, f *flight) {
	defer f.cancel()
	token, refreshAt, err := p.fetch(ctx, key)
	if ctx.Err() != nil {
		token, err = Token{}, tokenTransportError(ctx, "timeout")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	// A cancelled flight must not overwrite a replacement or repopulate cache.
	if p.flights[key] == f {
		delete(p.flights, key)
		if err == nil && ctx.Err() == nil {
			if len(p.cache) >= p.maxEntries {
				var oldest cacheKey
				var used uint64 = ^uint64(0)
				for k, cached := range p.cache {
					if cached.used < used {
						oldest, used = k, cached.used
					}
				}
				delete(p.cache, oldest)
			}
			p.sequence++
			p.cache[key] = cacheEntry{token: token, refreshAt: refreshAt, used: p.sequence}
		}
	}
	f.token, f.err = token, err
	close(f.done)
}

func requestKey(req Request) (cacheKey, error) {
	if !canonicalUUID(req.ProjectID) || !canonicalUUID(req.InstallationID) || req.Audience == "" || len(req.Audience) > 256 || !utf8.ValidString(req.Audience) || strings.IndexFunc(req.Audience, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return cacheKey{}, ErrRequest
	}
	joined, err := normalizeScopes(req.Scopes)
	if err != nil {
		return cacheKey{}, err
	}
	return cacheKey{project: req.ProjectID, installation: req.InstallationID, audience: req.Audience, scopes: joined}, nil
}

func normalizeScopes(input []string) (string, error) {
	if len(input) == 0 || len(input) > 128 {
		return "", ErrRequest
	}
	scopes := slices.Clone(input)
	slices.Sort(scopes)
	for i, scope := range scopes {
		if !asciiIdentifier(scope, 128) || strings.ContainsAny(scope, "\"\\") || i > 0 && scopes[i-1] == scope {
			return "", ErrRequest
		}
	}
	joined := strings.Join(scopes, " ")
	if len(joined) > 4096 {
		return "", ErrRequest
	}
	return joined, nil
}

func canonicalUUID(value string) bool {
	if len(value) != 36 || value == "00000000-0000-0000-0000-000000000000" {
		return false
	}
	for i, c := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func asciiIdentifier(value string, max int) bool {
	if len(value) == 0 || len(value) > max {
		return false
	}
	for _, c := range value {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

func httpsURL(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Opaque == "" && u.Fragment == ""
}

func boundedClient(source *http.Client) *http.Client {
	client := http.Client{Timeout: 30 * time.Second}
	if source != nil {
		client = *source
	}
	if client.Timeout <= 0 {
		client.Timeout = 30 * time.Second
	}
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}
