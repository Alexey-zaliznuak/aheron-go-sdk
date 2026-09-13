package integrationoauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
)

var ErrTransport = errors.New("integration OAuth: resource transport failed")

// ClientConfig binds a client to one approved resource URL prefix and token
// request. BaseURL must come from trusted deployment config, not user input.
type ClientConfig struct {
	Provider     *Provider
	BaseURL      string
	TokenRequest Request
	HTTPClient   *http.Client
}

// Client authenticates resource calls. It never follows redirects or sends a
// credential outside the configured HTTPS origin/path. It does not check object
// ownership: resource APIs must enforce project/installation/scopes themselves.
type Client struct {
	application *ApplicationRequest
	provider    *Provider
	base        *url.URL
	request     Request
	http        *http.Client
}

func NewClient(cfg ClientConfig) (*Client, error) {
	u, err := url.Parse(cfg.BaseURL)
	if cfg.Provider == nil || err != nil || !httpsURL(u) || u.RawQuery != "" || u.ForceQuery || strings.Contains(cfg.BaseURL, "#") || !cleanResourcePath(u) {
		return nil, ErrConfig
	}
	if _, err := requestKey(cfg.TokenRequest); err != nil {
		return nil, ErrConfig
	}
	req := cfg.TokenRequest
	req.Scopes = slices.Clone(req.Scopes)
	return &Client{provider: cfg.Provider, base: u, request: req, http: boundedClient(cfg.HTTPClient)}, nil
}

// Do retries a 401 once for GET/HEAD only, using a fresh token. Other methods
// return the original 401 after invalidating its token. No retry on 403, 5xx or
// an ambiguous transport failure; there is never a legacy fallback.
func (c *Client) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	retry := req != nil && (req.Method == http.MethodGet || req.Method == http.MethodHead || req.Method == "")
	return c.do(ctx, req, retry)
}

// DoIdempotent permits one retry after 401 for a write operation whose server
// contract guarantees safe replay (e.g. durable deduplication). An idempotency
// header alone is not proof. A nonempty body also requires Request.GetBody.
// It does not retry transport errors or 5xx responses.
func (c *Client) DoIdempotent(ctx context.Context, req *http.Request) (*http.Response, error) {
	return c.do(ctx, req, true)
}

func (c *Client) do(ctx context.Context, req *http.Request, retry bool) (*http.Response, error) {
	if c == nil || !c.accepts(req) {
		if req != nil && req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, ErrRequest
	}
	token, err := c.acquireToken(ctx)
	if err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	first := req.Clone(ctx)
	if first.Header == nil {
		first.Header = make(http.Header)
	}
	first.Header.Set("Authorization", "Bearer "+token.Bearer())
	response, err := c.http.Do(first)
	if err != nil {
		return nil, resourceTransportError(ctx)
	}
	if response.StatusCode != http.StatusUnauthorized {
		return response, nil
	}
	c.invalidateToken(token)
	if !retry || req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
		return response, nil
	}
	// Preserve the original response when it cannot be replayed. Once replay
	// starts, close the rejected body without reading unbounded attacker data.
	second := req.Clone(ctx)
	if req.Body != nil && req.Body != http.NoBody {
		second.Body, err = req.GetBody()
		if err != nil || second.Body == nil {
			if second.Body != nil {
				_ = second.Body.Close()
			}
			return response, nil
		}
	}
	_ = response.Body.Close()
	token, err = c.acquireToken(ctx)
	if err != nil {
		if second.Body != nil {
			_ = second.Body.Close()
		}
		return nil, err
	}
	if second.Header == nil {
		second.Header = make(http.Header)
	}
	second.Header.Set("Authorization", "Bearer "+token.Bearer())
	response, err = c.http.Do(second)
	if err != nil {
		return nil, resourceTransportError(ctx)
	}
	if response.StatusCode == http.StatusUnauthorized {
		c.invalidateToken(token)
	}
	return response, nil
}

func (c *Client) accepts(req *http.Request) bool {
	if req == nil || !httpsURL(req.URL) || !strings.EqualFold(req.URL.Host, c.base.Host) || !cleanResourcePath(req.URL) || req.Host != "" && !strings.EqualFold(req.Host, c.base.Host) {
		return false
	}
	basePath := strings.TrimRight(c.base.Path, "/")
	if req.URL.Path != basePath && !strings.HasPrefix(req.URL.Path, basePath+"/") {
		return false
	}
	for key := range req.Header {
		switch strings.ToLower(key) {
		case "authorization", "proxy-authorization", "cookie", "x-api-key", "x-integration-id", "x-integration-timestamp", "x-integration-signature":
			return false
		}
	}
	return true
}

func cleanResourcePath(u *url.URL) bool {
	if u.Path == "" {
		return true
	}
	// Reject dot segments, backslashes and alternate escaped separators so a
	// reverse proxy cannot interpret the configured path boundary differently.
	if !strings.HasPrefix(u.Path, "/") || strings.ContainsAny(u.Path, "\\%") {
		return false
	}
	if u.RawPath != "" && u.RawPath != u.Path {
		return false
	}
	return path.Clean(u.Path) == strings.TrimSuffix(u.Path, "/") || u.Path == "/"
}

func resourceTransportError(ctx context.Context) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%w: %w", ErrTransport, ctx.Err())
	}
	return ErrTransport
}
