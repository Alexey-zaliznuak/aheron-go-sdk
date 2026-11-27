// Package projectkey verifies project API keys through the internal auth API.
package projectkey

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalidKey  = errors.New("invalid project API key")
	ErrUnavailable = errors.New("project API key verification unavailable")
)

type Verifier struct {
	endpoint string
	token    string
	client   *http.Client
}

// NewVerifier uses a lookup-only service credential on the internal auth listener.
func NewVerifier(baseURL, lookupToken string, timeout time.Duration) (*Verifier, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(baseURL, "#") ||
		len(lookupToken) < 32 || strings.ContainsAny(lookupToken, " \t\r\n") || timeout <= 0 {
		return nil, errors.New("invalid project API key verifier configuration")
	}
	return &Verifier{
		endpoint: strings.TrimRight(baseURL, "/") + "/internal/api-keys/lookup",
		token:    lookupToken,
		client: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// Verify returns the project UUID of an active key. It checks the authority
// on each request, so a revoked key cannot be accepted from a local cache.
func (v *Verifier) Verify(ctx context.Context, raw string) (uuid.UUID, error) {
	if !strings.HasPrefix(raw, "ahr_proj_") || len(raw) > 256 || strings.ContainsAny(raw, " \t\r\n,") {
		return uuid.Nil, ErrInvalidKey
	}
	if v == nil || v.client == nil {
		return uuid.Nil, ErrUnavailable
	}
	body, err := json.Marshal(struct {
		Token string `json:"token"`
	}{Token: raw})
	if err != nil {
		return uuid.Nil, ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.endpoint, strings.NewReader(string(body)))
	if err != nil {
		return uuid.Nil, ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Project-Key-Lookup-Token", v.token)
	resp, err := v.client.Do(req)
	if err != nil {
		return uuid.Nil, ErrUnavailable
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound, http.StatusBadRequest:
		return uuid.Nil, ErrInvalidKey
	case http.StatusOK:
	default:
		return uuid.Nil, ErrUnavailable
	}
	const maxResponse = 16 << 10
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil || len(data) > maxResponse {
		return uuid.Nil, ErrUnavailable
	}
	var result struct {
		ProjectID uuid.UUID `json:"projectId"`
		Valid     *bool     `json:"valid"`
	}
	if err := json.Unmarshal(data, &result); err != nil || result.Valid == nil || result.ProjectID == uuid.Nil {
		return uuid.Nil, ErrUnavailable
	}
	if !*result.Valid {
		return uuid.Nil, ErrInvalidKey
	}
	return result.ProjectID, nil
}
