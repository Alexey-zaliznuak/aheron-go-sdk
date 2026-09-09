// Package transfercrm implements the platform-to-CRM provisioning protocol.
// It is separate from pure transfer contracts and integration-authenticated SDK APIs.
package transfercrm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/schemetransfer"
)

var (
	ErrUnavailable    = errors.New("transfer CRM unavailable")
	ErrConflict       = errors.New("transfer resource conflict")
	ErrNotFound       = errors.New("transfer project or resource not found")
	ErrInvalidRequest = errors.New("invalid transfer resource request")
)

type Client struct {
	baseURL, token string
	http           *http.Client
}

// New uses the caller's timeouts and transport but never follows redirects with
// the internal credential. The caller must supply a bounded request context.
func New(baseURL, token string, client *http.Client) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimSpace(token) == "" || client == nil {
		return nil, ErrInvalidRequest
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: &copy}, nil
}
func (c *Client) ProvisionResource(ctx context.Context, projectID, importID, ref string, input schemetransfer.ProvisionRequest) (schemetransfer.ProvisionResult, error) {
	var empty schemetransfer.ProvisionResult
	if !schemetransfer.ValidProvisionAddress(projectID, importID, ref) || input.Validate() != nil {
		return empty, ErrInvalidRequest
	}
	body, err := json.Marshal(input)
	if err != nil {
		return empty, ErrInvalidRequest
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+"/internal/projects/"+projectID+"/scheme-imports/"+importID+"/resources/"+ref, bytes.NewReader(body))
	if err != nil {
		return empty, ErrInvalidRequest
	}
	req.Header.Set("X-Internal-Token", c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return empty, ErrUnavailable
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case 200:
	case 400, 422:
		return empty, ErrInvalidRequest
	case 404:
		return empty, ErrNotFound
	case 409:
		return empty, ErrConflict
	default:
		return empty, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 8193))
	if err != nil || len(raw) > 8192 {
		return empty, ErrUnavailable
	}
	if _, err := schemetransfer.DecodeJSON(raw); err != nil {
		return empty, ErrUnavailable
	}
	var result schemetransfer.ProvisionResult
	if json.Unmarshal(raw, &result) != nil || result.Validate(input) != nil {
		return empty, ErrUnavailable
	}
	return result, nil
}
