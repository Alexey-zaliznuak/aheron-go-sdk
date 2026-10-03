package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const DefaultBaseURL = "https://aheron.pro/api"

var (
	ErrConfig           = errors.New("platform: invalid client configuration")
	ErrInvalidInput     = errors.New("platform: invalid resource identifier")
	ErrUnauthorized     = errors.New("platform: unauthorized")
	ErrForbidden        = errors.New("platform: forbidden")
	ErrNotFound         = errors.New("platform: not found")
	ErrConflict         = errors.New("platform: conflict")
	ErrTransport        = errors.New("platform: API transport failed")
	ErrResponse         = errors.New("platform: invalid API response")
	ErrResponseTooLarge = errors.New("platform: API response exceeds the size limit")
)

// APIError exposes status, but never an upstream body or credential-bearing URL.
type APIError struct {
	Operation  string
	StatusCode int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("platform: %s returned HTTP %d", e.Operation, e.StatusCode)
}

func (e *APIError) Is(target error) bool {
	return (target == ErrUnauthorized && e.StatusCode == 401) ||
		(target == ErrForbidden && e.StatusCode == 403) ||
		(target == ErrNotFound && e.StatusCode == 404) ||
		(target == ErrConflict && e.StatusCode == 409)
}

type Config struct {
	// BaseURL includes the backend API prefix, e.g. https://aheron.pro/api.
	BaseURL          string
	TokenProvider    TokenProvider
	Timeout          time.Duration
	MaxResponseBytes int64
	// HTTPClient is copied. Redirects, cookies and an unbounded timeout are
	// disabled on the copy; the caller's original client is never changed.
	HTTPClient *http.Client
	// AllowLoopbackHTTP permits development against localhost or a loopback
	// IP only. It does not enable plaintext remote credential transmission.
	AllowLoopbackHTTP bool
}

// Client is immutable after construction and safe for concurrent use, provided
// its TokenProvider is also safe. Create separate copies for separate users.
type Client struct {
	base             *url.URL
	http             *http.Client
	provider         TokenProvider
	maxResponseBytes int64
	Projects         *ProjectsClient
	Schemes          *SchemesClient
}

func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Opaque != "" ||
		u.RawQuery != "" || u.ForceQuery || strings.Contains(cfg.BaseURL, "#") ||
		u.RawPath != "" || strings.Contains(u.Path, "\\") || cfg.TokenProvider == nil {
		return nil, ErrConfig
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && cfg.AllowLoopbackHTTP && loopback(u.Hostname())) {
		return nil, ErrConfig
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return nil, ErrConfig
		}
	}
	if cfg.Timeout < 0 || cfg.MaxResponseBytes < 0 {
		return nil, ErrConfig
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxResponseBytes == 0 {
		cfg.MaxResponseBytes = 4 << 20
	}
	if cfg.MaxResponseBytes > 64<<20 {
		return nil, ErrConfig
	}
	client := http.Client{}
	if cfg.HTTPClient != nil {
		client = *cfg.HTTPClient
	}
	client.Timeout = cfg.Timeout
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	u.Path = strings.TrimRight(u.Path, "/")
	c := &Client{base: u, http: &client, provider: cfg.TokenProvider, maxResponseBytes: cfg.MaxResponseBytes}
	c.bindResources()
	return c, nil
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// WithTokenProvider returns an independent credential binding while reusing
// the immutable transport. It never modifies the original client or provider.
func (c *Client) WithTokenProvider(provider TokenProvider) (*Client, error) {
	if c == nil || provider == nil {
		return nil, ErrConfig
	}
	copy := *c
	copy.provider = provider
	copy.bindResources()
	return &copy, nil
}

func (c *Client) bindResources() {
	c.Projects = &ProjectsClient{client: c}
	c.Schemes = &SchemesClient{client: c}
}

// get intentionally performs one request. Credential refresh is explicit in
// the provider; there is no hidden replay, auth fallback, or arbitrary URL API.
func (c *Client) get(ctx context.Context, operation, path string, out any) error {
	u := *c.base
	u.Path += path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return ErrConfig
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(ctx, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &APIError{Operation: operation, StatusCode: resp.StatusCode}
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return ErrResponse
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.maxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrTransport
	}
	if int64(len(body)) > c.maxResponseBytes {
		return ErrResponseTooLarge
	}
	if strings.TrimSpace(string(body)) == "null" || json.Unmarshal(body, out) != nil {
		return ErrResponse
	}
	return nil
}

// Do is the shared user transport for typed resource clients in this SDK.
// Requests must stay inside the configured API base; credentials are never
// forwarded across redirects. The caller owns the response body and its limit.
// The input request is cloned and no failed request is automatically replayed.
func (c *Client) Do(ctx context.Context, input *http.Request) (*http.Response, error) {
	if input == nil || input.URL == nil {
		return nil, ErrInvalidInput
	}
	u := input.URL
	if u.Scheme != c.base.Scheme || u.Host != c.base.Host || u.User != nil || u.Opaque != "" ||
		u.Fragment != "" || u.RawPath != "" || strings.Contains(u.Path, "\\") ||
		path.Clean(u.Path) != u.Path || !strings.HasPrefix(u.Path, c.base.Path+"/") ||
		(input.Host != "" && input.Host != c.base.Host) {
		return nil, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	token, err := c.provider.Token(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, ErrCredential // Providers can include secrets in their errors.
	}
	if _, err := NewAccessToken(token.value, token.expiresAt); err != nil {
		return nil, err
	}
	if !token.expiresAt.IsZero() && !time.Now().Before(token.expiresAt) {
		return nil, ErrTokenExpired
	}
	req := input.Clone(ctx)
	req.Header.Del("Cookie")
	req.Header.Set("Authorization", "Bearer "+token.value)
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrTransport // Do not expose URL/transport error details.
	}
	return resp, nil
}
