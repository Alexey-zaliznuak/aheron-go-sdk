package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/httpclient"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/platform"
)

type crmUser struct {
	client  *platform.Client
	baseURL string
}

func (c *crmUser) do(ctx context.Context, input httpclient.Request) (*httpclient.Response, error) {
	parts := strings.Split(strings.TrimPrefix(input.Path, "/"), "/")
	if len(parts) < 3 || !integrationOAuthUUID(parts[1]) {
		return nil, platform.ErrInvalidInput
	}
	// Reuse the existing typed operation inventory, without installation scopes.
	inventory := crmOAuth{projectID: parts[1]}
	_, statuses, err := inventory.operation(input)
	if err != nil {
		return nil, platform.ErrInvalidInput
	}
	u, err := url.Parse(strings.TrimRight(c.baseURL, "/") + input.Path)
	if err != nil {
		return nil, platform.ErrInvalidInput
	}
	q := u.Query()
	for k, v := range input.Query {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, input.Method, u.String(), bytes.NewReader(input.Body))
	if err != nil {
		return nil, platform.ErrInvalidInput
	}
	req.Header.Set("Accept", "application/json")
	if input.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{Method: input.Method, URL: "/projects/{projectId}", Status: resp.StatusCode, Message: "CRM user request rejected"}
	}
	accepted := false
	for _, status := range statuses {
		if status == resp.StatusCode {
			accepted = true
		}
	}
	if !accepted {
		return nil, platform.ErrResponse
	}
	if resp.StatusCode == 204 {
		return &httpclient.Response{Status: 204}, nil
	}
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return nil, platform.ErrResponse
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 || !json.Valid(body) || bytes.Equal(bytes.TrimSpace(body), []byte("null")) {
		return nil, platform.ErrResponse
	}
	return &httpclient.Response{Status: resp.StatusCode, Body: body}, nil
}
