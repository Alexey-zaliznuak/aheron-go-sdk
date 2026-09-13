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

// LinksOAuthConfig binds project link operations to one installation.
// RegisterCallback is application-level management and is not authorized by
// this project grant. Use a separately configured management client for it.
type LinksOAuthConfig struct {
	Provider       *integrationoauth.Provider
	ProjectID      string
	InstallationID string
	HTTPClient     *http.Client
}

type linksOAuth struct {
	projectID, baseURL string
	read, write        *integrationoauth.Client
}

var errLinksOAuthResponse = errors.New("integration: invalid Links OAuth response")

func newLinksOAuth(baseURL string, cfg *LinksOAuthConfig) (*linksOAuth, error) {
	if cfg == nil {
		return nil, nil
	}
	c := &linksOAuth{projectID: cfg.ProjectID, baseURL: strings.TrimRight(baseURL, "/")}
	for _, op := range []struct {
		target **integrationoauth.Client
		scope  string
	}{{&c.read, "links.read"}, {&c.write, "links.write"}} {
		client, err := integrationoauth.NewClient(integrationoauth.ClientConfig{Provider: cfg.Provider, BaseURL: baseURL, HTTPClient: cfg.HTTPClient, TokenRequest: integrationoauth.Request{ProjectID: cfg.ProjectID, InstallationID: cfg.InstallationID, Audience: "links", Scopes: []string{op.scope}}})
		if err != nil {
			return nil, err
		}
		*op.target = client
	}
	return c, nil
}

func (c *linksOAuth) operation(method string, u *url.URL) (*integrationoauth.Client, int, bool, error) {
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "projects" || parts[1] != c.projectID || parts[2] != "links" || u.IsAbs() || u.Host != "" || u.Fragment != "" || u.RawPath != "" {
		return nil, 0, false, integrationoauth.ErrRequest
	}
	if len(parts) > 3 && !linkIDPattern.MatchString(parts[3]) {
		return nil, 0, false, integrationoauth.ErrRequest
	}
	if method == "GET" && (len(parts) == 3 || len(parts) == 4 || len(parts) == 5 && parts[4] == "deliveries") {
		return c.read, 200, false, nil
	}
	if u.RawQuery != "" || u.ForceQuery {
		return nil, 0, false, integrationoauth.ErrRequest
	}
	if method == "POST" && len(parts) == 3 {
		return c.write, 201, true, nil
	}
	if method == "DELETE" && len(parts) == 4 {
		return c.write, 204, false, nil
	}
	if method == "POST" && len(parts) == 7 && parts[4] == "deliveries" && integrationOAuthUUID(parts[5]) && parts[6] == "replay" {
		return c.write, 204, false, nil
	}
	return nil, 0, false, integrationoauth.ErrRequest
}

func (c *linksOAuth) call(ctx context.Context, method, path, key string, body []byte, out any) error {
	u, err := url.Parse(path)
	if err != nil {
		return integrationoauth.ErrRequest
	}
	client, status, retry, err := c.operation(method, u)
	if err != nil {
		return err
	}
	if retry && (key == "" || len(key) > 128 || strings.ContainsAny(key, "\r\n")) {
		return integrationoauth.ErrRequest
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return integrationoauth.ErrRequest
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	var resp *http.Response
	// Create has durable project/principal/key deduplication. Replay and Disable
	// use Do, so only GET and this explicit Create contract can retry a 401.
	if retry {
		resp, err = client.DoIdempotent(ctx, req)
	} else {
		resp, err = client.Do(ctx, req)
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Method: method, URL: "/projects/{projectId}/links", Status: resp.StatusCode, Message: "Links OAuth request rejected"}
	}
	if resp.StatusCode != status {
		return errLinksOAuthResponse
	}
	if status == 204 {
		return nil
	}
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return errLinksOAuthResponse
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || out == nil || json.Unmarshal(raw, out) != nil {
		return errLinksOAuthResponse
	}
	return nil
}
