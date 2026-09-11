// Package transfermedia calls the platform-only template asset API. Integrations
// use integration.FilesClient with a project key; they must not receive this
// client's internal credential. Authorization of source/target projects and the
// owning template revision belongs to the platform caller.
package transfermedia

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integration"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/schemetransfer"
)

var (
	ErrUnavailable    = errors.New("template media unavailable")
	ErrNotFound       = errors.New("template media not found")
	ErrConflict       = errors.New("template media conflict or operation incomplete")
	ErrInvalidRequest = errors.New("invalid template media request")
)

const maxResponseBytes = 16 << 10

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var resourcePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var namespacePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,62}[a-z0-9])?$`)
var hashPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type Client struct {
	baseURL, token string
	http           *http.Client
}

// New copies the provided client, forbids redirects and defaults a missing
// timeout to 30 seconds. Caller cancellation and shorter deadlines are honored.
// Methods make one HTTP attempt; retries must reuse the same operation address.
func New(baseURL, token string, client *http.Client) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") || client == nil {
		return nil, ErrInvalidRequest
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if copy.Timeout <= 0 {
		copy.Timeout = 30 * time.Second
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: &copy}, nil
}

type CaptureRequest struct {
	SourceProjectID string `json:"sourceProjectId"`
	SourceFileID    string `json:"sourceFileId"`
}

// Asset metadata contains no source identity, URL, object key or credentials.
type Asset struct {
	SnapshotID  string `json:"snapshotId"`
	AssetID     string `json:"assetId"`
	FileName    string `json:"fileName"`
	MimeType    string `json:"mimeType"`
	ContentHash string `json:"contentHash"`
	Kind        string `json:"kind"`
	SizeBytes   int64  `json:"sizeBytes"`
}

type ImportRequest struct {
	SnapshotID string `json:"snapshotId"`
	AssetID    string `json:"assetId"`
	Namespace  string `json:"namespace,omitempty"`
}

func validUUID(id string) bool {
	return uuidPattern.MatchString(id) && id != "00000000-0000-0000-0000-000000000000"
}

func snapshotPath(snapshotID, assetID string) (string, error) {
	if !validUUID(snapshotID) || !validUUID(assetID) {
		return "", ErrInvalidRequest
	}
	return "/internal/template-file-snapshots/" + snapshotID + "/assets/" + assetID, nil
}

func importPath(projectID, importID, resourceKey string) (string, error) {
	if !validUUID(projectID) || !validUUID(importID) || !resourcePattern.MatchString(resourceKey) {
		return "", ErrInvalidRequest
	}
	return "/internal/projects/" + projectID + "/template-file-imports/" + importID + "/resources/" + resourceKey, nil
}

// Capture freezes one source file. Snapshot/asset IDs belong to a revision and
// must be persisted by the caller before the first attempt, not regenerated on retry.
func (c *Client) Capture(ctx context.Context, snapshotID, assetID string, input CaptureRequest) (Asset, error) {
	path, err := snapshotPath(snapshotID, assetID)
	if err != nil || !validUUID(input.SourceProjectID) || !validUUID(input.SourceFileID) {
		return Asset{}, ErrInvalidRequest
	}
	return c.asset(ctx, http.MethodPut, path, snapshotID, assetID, input)
}

func (c *Client) GetAsset(ctx context.Context, snapshotID, assetID string) (Asset, error) {
	path, err := snapshotPath(snapshotID, assetID)
	if err != nil {
		return Asset{}, err
	}
	return c.asset(ctx, http.MethodGet, path, snapshotID, assetID, nil)
}

func (c *Client) asset(ctx context.Context, method, path, snapshotID, assetID string, input any) (Asset, error) {
	var out Asset
	if err := c.do(ctx, method, path, input, &out); err != nil {
		return Asset{}, err
	}
	if out.SnapshotID != snapshotID || out.AssetID != assetID || strings.TrimSpace(out.FileName) == "" || strings.TrimSpace(out.MimeType) == "" || out.SizeBytes <= 0 || !hashPattern.MatchString(out.ContentHash) || !validKind(out.Kind) {
		return Asset{}, ErrUnavailable
	}
	return out, nil
}

// Import returns the current target file on a receipt replay, preserving manual
// edits. A deleted result returns ErrNotFound and must not be recreated under a
// fresh operation ID. Namespace is chosen from the pinned integration manifest.
func (c *Client) Import(ctx context.Context, projectID, importID, resourceKey string, input ImportRequest) (integration.File, error) {
	path, err := importPath(projectID, importID, resourceKey)
	if err != nil || !validUUID(input.SnapshotID) || !validUUID(input.AssetID) || (input.Namespace != "" && !namespacePattern.MatchString(input.Namespace)) {
		return integration.File{}, ErrInvalidRequest
	}
	out, err := c.file(ctx, http.MethodPut, path, input)
	if err != nil {
		return integration.File{}, err
	}
	namespace := input.Namespace
	if namespace == "" {
		namespace = "library"
	}
	if out.Namespace != namespace {
		return integration.File{}, ErrUnavailable
	}
	return out, nil
}

func (c *Client) GetImport(ctx context.Context, projectID, importID, resourceKey string) (integration.File, error) {
	path, err := importPath(projectID, importID, resourceKey)
	if err != nil {
		return integration.File{}, err
	}
	return c.file(ctx, http.MethodGet, path, nil)
}

func (c *Client) file(ctx context.Context, method, path string, input any) (integration.File, error) {
	var out integration.File
	if err := c.do(ctx, method, path, input, &out); err != nil {
		return integration.File{}, err
	}
	u, err := url.Parse(out.URL)
	if !validUUID(out.ID) || !namespacePattern.MatchString(out.Namespace) || strings.TrimSpace(out.FileName) == "" || strings.TrimSpace(out.MimeType) == "" || out.SizeBytes <= 0 || !validKind(out.Kind) || out.CreatedAt.IsZero() || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return integration.File{}, ErrUnavailable
	}
	return out, nil
}

func validKind(kind string) bool {
	switch kind {
	case "photo", "video", "audio", "file":
		return true
	default:
		return false
	}
}

func (c *Client) do(ctx context.Context, method, path string, input, out any) error {
	var body []byte
	if input != nil {
		var err error
		body, err = json.Marshal(input)
		if err != nil {
			return ErrInvalidRequest
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return ErrInvalidRequest
	}
	req.Header.Set("X-Internal-Token", c.token)
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case 400, 413, 422:
		return ErrInvalidRequest
	case 404:
		return ErrNotFound
	case 409:
		return ErrConflict
	default:
		return ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes {
		return ErrUnavailable
	}
	if _, err = schemetransfer.DecodeJSON(raw); err != nil {
		return ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil {
		return ErrUnavailable
	}
	return nil
}
