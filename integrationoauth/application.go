package integrationoauth

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// ApplicationRequest identifies an integration-wide OAuth profile. Access is
// controlled by the registered client and audience, not by application scopes.
type ApplicationRequest struct {
	Audience string
}

func applicationRequestKey(req ApplicationRequest) (cacheKey, error) {
	if req.Audience != "catalog" && req.Audience != "links" {
		return cacheKey{}, ErrRequest
	}
	return cacheKey{audience: req.Audience, kind: "application"}, nil
}

func (p *Provider) ApplicationToken(ctx context.Context, req ApplicationRequest) (Token, error) {
	key, err := applicationRequestKey(req)
	if err != nil {
		return Token{}, err
	}
	return p.token(ctx, key)
}

func (p *Provider) InvalidateApplication(req ApplicationRequest, rejected Token) {
	key, err := applicationRequestKey(req)
	if err == nil {
		p.invalidate(key, rejected)
	}
}

type ApplicationClientConfig struct {
	Provider     *Provider
	BaseURL      string
	TokenRequest ApplicationRequest
	HTTPClient   *http.Client
}

// NewApplicationClient pins the same transport boundary as NewClient, with a
// distinct token request and cache namespace. Neither profile can fall back.
func NewApplicationClient(cfg ApplicationClientConfig) (*Client, error) {
	u, err := url.Parse(cfg.BaseURL)
	if cfg.Provider == nil || err != nil || !httpsURL(u) || u.RawQuery != "" || u.ForceQuery || strings.Contains(cfg.BaseURL, "#") || !cleanResourcePath(u) {
		return nil, ErrConfig
	}
	if _, err := applicationRequestKey(cfg.TokenRequest); err != nil {
		return nil, ErrConfig
	}
	req := cfg.TokenRequest
	return &Client{provider: cfg.Provider, base: u, application: &req, http: boundedClient(cfg.HTTPClient)}, nil
}

func (c *Client) acquireToken(ctx context.Context) (Token, error) {
	if c.application != nil {
		return c.provider.ApplicationToken(ctx, *c.application)
	}
	return c.provider.Token(ctx, c.request)
}

func (c *Client) invalidateToken(token Token) {
	if c.application != nil {
		c.provider.InvalidateApplication(*c.application, token)
		return
	}
	c.provider.Invalidate(c.request, token)
}
