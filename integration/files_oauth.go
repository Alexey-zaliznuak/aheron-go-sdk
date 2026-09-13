package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/httpclient"
)

// FilesOAuthConfig binds media metadata methods to one project installation.
// HTTPClient is for media API calls only. UploadHTTPClient is a separate trusted
// transport for presigned storage PUTs; no OAuth token is attached to uploads.
type FilesOAuthConfig struct {
	Provider         *integrationoauth.Provider
	ProjectID        string
	InstallationID   string
	HTTPClient       *http.Client
	UploadHTTPClient *http.Client
}

type filesOAuth struct {
	projectID, baseURL string
	read, write        *integrationoauth.Client
	upload             *http.Client
}

func newFilesOAuth(baseURL string, cfg *FilesOAuthConfig) (*filesOAuth, error) {
	if cfg == nil {
		return nil, nil
	}
	c := &filesOAuth{projectID: cfg.ProjectID, baseURL: strings.TrimRight(baseURL, "/")}
	for _, op := range []struct {
		target **integrationoauth.Client
		scope  string
	}{{&c.read, "files.read"}, {&c.write, "files.write"}} {
		client, err := integrationoauth.NewClient(integrationoauth.ClientConfig{Provider: cfg.Provider, BaseURL: baseURL, HTTPClient: cfg.HTTPClient, TokenRequest: integrationoauth.Request{ProjectID: cfg.ProjectID, InstallationID: cfg.InstallationID, Audience: "media", Scopes: []string{op.scope}}})
		if err != nil {
			return nil, err
		}
		*op.target = client
	}
	upload := http.Client{}
	if cfg.UploadHTTPClient != nil {
		upload = *cfg.UploadHTTPClient
	}
	upload.Jar = nil
	upload.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if upload.Timeout <= 0 || upload.Timeout > 5*time.Minute {
		upload.Timeout = 5 * time.Minute
	}
	c.upload = &upload
	return c, nil
}

var errFilesOAuthResponse = errors.New("integration: invalid Files OAuth response")
var errFilesOAuthUpload = errors.New("integration: storage upload failed")

// Explicit inventory: future typed methods remain unavailable until classified.
func (c *filesOAuth) operation(req httpclient.Request) (*integrationoauth.Client, []int, error) {
	shape := req.Path
	parts := strings.Split(strings.TrimPrefix(shape, "/"), "/")
	if len(parts) >= 2 && parts[0] == "files" && parts[1] != "purge" && parts[1] != "upload-url" {
		if !integrationOAuthUUID(parts[1]) {
			return nil, nil, integrationoauth.ErrRequest
		}
		parts[1] = "{id}"
		shape = "/" + strings.Join(parts, "/")
	}
	switch req.Method + " " + shape {
	case "GET /files", "GET /files/{id}", "GET /usage":
		return c.read, []int{200}, nil
	case "POST /files":
		return c.write, []int{201}, nil
	case "POST /files/upload-url", "POST /files/purge", "POST /files/{id}/content", "PATCH /files/{id}", "DELETE /files/{id}":
		return c.write, []int{200}, nil
	default:
		return nil, nil, integrationoauth.ErrRequest
	}
}

func (c *filesOAuth) validateUploadTarget(target uploadURLResponse) error {
	u, err := url.Parse(target.URL)
	prefix := "tmp/" + c.projectID + "/"
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || strings.Contains(target.URL, "#") || target.Method != http.MethodPut || !strings.HasPrefix(target.UploadKey, prefix) || !integrationOAuthUUID(strings.TrimPrefix(target.UploadKey, prefix)) {
		return errFilesOAuthResponse
	}
	return nil
}

func (c *filesOAuth) putObject(ctx context.Context, target uploadURLResponse, content []byte, contentType string) error {
	if err := c.validateUploadTarget(target); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target.URL, bytes.NewReader(content))
	if err != nil {
		return errFilesOAuthUpload
	}
	req.ContentLength = int64(len(content))
	req.Header.Set("Content-MD5", md5Base64(content))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.upload.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errFilesOAuthUpload
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return errFilesOAuthUpload
	}
	return nil
}

func (c *FilesClient) do(ctx context.Context, req httpclient.Request) (*httpclient.Response, error) {
	if c.oauth != nil {
		return c.oauth.do(ctx, req)
	}
	return c.http.Do(ctx, req)
}

func (c *FilesClient) decodeError(operation string, err error) error {
	if c.oauth != nil {
		return errFilesOAuthResponse
	}
	return fmt.Errorf("integration: decode %s: %w", operation, err)
}

func (c *FilesClient) decodeFile(body []byte) (File, error) {
	var out File
	if err := json.Unmarshal(body, &out); err != nil {
		return File{}, c.decodeError("file", err)
	}
	return out, nil
}

func (c *filesOAuth) do(ctx context.Context, input httpclient.Request) (*httpclient.Response, error) {
	client, statuses, err := c.operation(input)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(c.baseURL + input.Path)
	if err != nil {
		return nil, integrationoauth.ErrRequest
	}
	query := u.Query()
	for k, v := range input.Query {
		query.Set(k, v)
	}
	u.RawQuery = query.Encode()
	var req *http.Request
	if input.Body == nil {
		req, err = http.NewRequestWithContext(ctx, input.Method, u.String(), nil)
	} else {
		req, err = http.NewRequestWithContext(ctx, input.Method, u.String(), bytes.NewReader(input.Body))
	}
	if err != nil {
		return nil, integrationoauth.ErrRequest
	}
	req.Header.Set("Accept", "application/json")
	if input.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// No automatic write retry: Files writes have no durable idempotency receipts.
	response, err := client.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &APIError{Method: input.Method, URL: "/files", Status: response.StatusCode, Message: "Files OAuth request rejected"}
	}
	accepted := false
	for _, status := range statuses {
		if response.StatusCode == status {
			accepted = true
		}
	}
	if !accepted {
		return nil, errFilesOAuthResponse
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return nil, errFilesOAuthResponse
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 || !json.Valid(raw) || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, errFilesOAuthResponse
	}
	return &httpclient.Response{Status: response.StatusCode, Body: raw}, nil
}
