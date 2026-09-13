package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
)

// ApplicationOAuthConfig enables integration-wide management calls. The same
// provider may serve installation clients, but grants and token caches stay separate.
type ApplicationOAuthConfig struct {
	Provider   *integrationoauth.Provider
	HTTPClient *http.Client
}

type applicationOAuth struct {
	client            *integrationoauth.Client
	baseURL, audience string
}

var errApplicationOAuthResponse = errors.New("integration: invalid application OAuth response")
var applicationEndpointKey = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func newApplicationOAuth(baseURL, audience string, cfg *ApplicationOAuthConfig) (*applicationOAuth, error) {
	if cfg == nil {
		return nil, nil
	}
	scope := "catalog.write"
	if audience == "links" {
		scope = "links.callbacks.write"
	}
	c, err := integrationoauth.NewApplicationClient(integrationoauth.ApplicationClientConfig{Provider: cfg.Provider, BaseURL: baseURL, HTTPClient: cfg.HTTPClient, TokenRequest: integrationoauth.ApplicationRequest{Audience: audience, Scopes: []string{scope}}})
	if err != nil {
		return nil, err
	}
	return &applicationOAuth{client: c, baseURL: strings.TrimRight(baseURL, "/"), audience: audience}, nil
}

func (c *applicationOAuth) call(ctx context.Context, method, path string, body []byte, out any) error {
	callbacksPrefix := "/integrations/self/link-callbacks/"
	if !(c.audience == "catalog" && method == http.MethodPost && path == selfSyncPath || c.audience == "links" && method == http.MethodPut && strings.HasPrefix(path, callbacksPrefix) && applicationEndpointKey.MatchString(strings.TrimPrefix(path, callbacksPrefix))) {
		return integrationoauth.ErrRequest
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return integrationoauth.ErrRequest
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// Both operations declare desired state; auth rejects a 401 before writes.
	resp, err := c.client.DoIdempotent(ctx, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Method: method, URL: "/integrations/self", Status: resp.StatusCode, Message: "application OAuth request rejected"}
	}
	if resp.StatusCode != http.StatusOK {
		return errApplicationOAuthResponse
	}
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return errApplicationOAuthResponse
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || out == nil || json.Unmarshal(raw, out) != nil {
		return errApplicationOAuthResponse
	}
	return nil
}
