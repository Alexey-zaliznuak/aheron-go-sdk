package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
)

// ExecutionOAuthConfig binds one SDK client to a project installation. Reuse
// Provider across installations to share its bounded token cache. Resource
// transport settings are independent of legacy Resty retries.
type ExecutionOAuthConfig struct {
	Provider       *integrationoauth.Provider
	ProjectID      string
	InstallationID string
	HTTPClient     *http.Client
}

type executionOAuth struct {
	projectID, baseURL                  string
	resolve, resolveVariables, triggers *integrationoauth.Client
}

func newExecutionOAuth(baseURL string, cfg *ExecutionOAuthConfig) (*executionOAuth, error) {
	if cfg == nil {
		return nil, nil
	}
	c := &executionOAuth{projectID: cfg.ProjectID, baseURL: strings.TrimRight(baseURL, "/")}
	for _, operation := range []struct {
		target **integrationoauth.Client
		scopes []string
	}{
		{&c.resolve, []string{"integration.resolve"}},
		{&c.resolveVariables, []string{"integration.resolve", "variables.write"}},
		{&c.triggers, []string{"triggers.activate"}},
	} {
		client, err := integrationoauth.NewClient(integrationoauth.ClientConfig{Provider: cfg.Provider, BaseURL: baseURL, HTTPClient: cfg.HTTPClient,
			TokenRequest: integrationoauth.Request{ProjectID: cfg.ProjectID, InstallationID: cfg.InstallationID, Audience: "execution", Scopes: operation.scopes}})
		if err != nil {
			return nil, err
		}
		*operation.target = client
	}
	return c, nil
}

var errExecutionOAuthResponse = errors.New("integration: invalid execution OAuth response")

func (c *executionOAuth) call(ctx context.Context, client *integrationoauth.Client, method, path string, query map[string]string, body []byte, idempotent bool, expectedStatus int, out any) error {
	u, err := url.Parse(c.baseURL + path)
	if err != nil {
		return integrationoauth.ErrRequest
	}
	q := u.Query()
	for key, value := range query {
		q.Set(key, value)
	}
	u.RawQuery = q.Encode()
	var req *http.Request
	if body == nil {
		req, err = http.NewRequestWithContext(ctx, method, u.String(), nil)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	}
	if err != nil {
		return integrationoauth.ErrRequest
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	var resp *http.Response
	if idempotent {
		resp, err = client.DoIdempotent(ctx, req)
	} else {
		resp, err = client.Do(ctx, req)
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Do not preserve upstream bodies: they may reflect credentials.
		return &APIError{Method: method, URL: path, Status: resp.StatusCode, Message: "execution OAuth request rejected"}
	}
	if resp.StatusCode != expectedStatus {
		return errExecutionOAuthResponse
	}
	if out == nil {
		return nil
	}
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return errExecutionOAuthResponse
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return errExecutionOAuthResponse
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, out) != nil {
		return errExecutionOAuthResponse
	}
	return nil
}

func (c *executionOAuth) resolveStep(ctx context.Context, ec ExecutionContext, body []byte, variables bool, options ResolveOptions) error {
	// Older payloads can omit ProjectID; execution-service checks the context's
	// authoritative project in either case.
	if ec.ProjectID != "" && ec.ProjectID != c.projectID {
		return integrationoauth.ErrRequest
	}
	client := c.resolve
	if variables {
		client = c.resolveVariables
	}
	return c.call(ctx, client, http.MethodPost, resolvePath, nil, body, options.IdempotencyKey != "", http.StatusAccepted, nil)
}
