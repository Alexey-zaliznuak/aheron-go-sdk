package integration

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/httpclient"
)

// FilesClient stores and retrieves a project's media files in the platform
// media-service, authorized by the project API key granted to the integration
// at install time. The project is inferred from the key, so no project id is
// passed. Metadata paths are relative to the configured MediaURL (which carries
// the "/api/media" gateway prefix); file bytes are uploaded directly to object
// storage via a presigned URL and never flow through media-service.
type FilesClient struct {
	http   *httpclient.Client
	apiKey string
	// putClient uploads bytes straight to the presigned object-storage URL. It is
	// separate from http (which targets media-service) because the presigned URL
	// is an absolute object-storage endpoint.
	putClient *http.Client
}

// WithAPIKey returns a copy of the client that authenticates with apiKey instead
// of the key configured on the parent Client. It shares the underlying HTTP
// transport, so it is cheap to derive per request or per project.
func (c *FilesClient) WithAPIKey(apiKey string) *FilesClient {
	clone := *c
	clone.apiKey = apiKey
	return &clone
}

// File is a stored media file. URL is the stable public GET URL to its bytes
// (the media bucket grants public read). Namespace partitions a project's files
// by owner/purpose: "library" is the user-facing media library; integrations
// keep machine-managed files under their own namespace so users cannot delete
// them from the library by accident.
type File struct {
	ID         string     `json:"id"`
	Namespace  string     `json:"namespace"`
	FileName   string     `json:"fileName"`
	MimeType   string     `json:"mimeType"`
	SizeBytes  int64      `json:"sizeBytes"`
	Kind       string     `json:"kind"`
	URL        string     `json:"url"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

// NamespaceUsage is the stored volume of one namespace of the project.
type NamespaceUsage struct {
	Namespace   string `json:"namespace"`
	StoredBytes int64  `json:"storedBytes"`
}

// Usage is a project's storage usage snapshot: the billable total plus a
// per-namespace breakdown.
type Usage struct {
	ProjectID   string           `json:"projectId"`
	StoredBytes int64            `json:"storedBytes"`
	Namespaces  []NamespaceUsage `json:"namespaces"`
}

type filesListResponse struct {
	Files []File `json:"files"`
}

type uploadURLResponse struct {
	UploadKey string `json:"uploadKey"`
	URL       string `json:"url"`
	Method    string `json:"method"`
}

type finalizeUploadBody struct {
	UploadKey string `json:"uploadKey"`
	FileName  string `json:"fileName"`
	MimeType  string `json:"mimeType"`
	Namespace string `json:"namespace,omitempty"`
}

type purgeNamespaceBody struct {
	Namespace      string     `json:"namespace"`
	LastUsedBefore *time.Time `json:"lastUsedBefore,omitempty"`
}

type purgeNamespaceResponse struct {
	Deleted int `json:"deleted"`
}

type replaceContentBody struct {
	UploadKey string `json:"uploadKey"`
	MimeType  string `json:"mimeType"`
}

// Upload stores content as a new file in the project's media library (the
// "library" namespace, deduplicated by content) and returns its metadata. The
// bytes are PUT directly to object storage via a presigned URL — they do not
// transit media-service — then the upload is finalized. The content hash is
// derived server-side from the object's ETag, so no hash is sent; the PUT
// carries a Content-MD5 so storage rejects a corrupted transfer. mimeType may
// be empty; the service infers it from the file name.
func (c *FilesClient) Upload(ctx context.Context, fileName, mimeType string, content []byte) (File, error) {
	return c.UploadToNamespace(ctx, "", fileName, mimeType, content)
}

// UploadToNamespace is Upload targeting an explicit namespace. Integrations
// should store machine-managed files (e.g. dialog attachments) under their own
// namespace slug so the files stay out of the user's media library and cannot
// be deleted from it. Deduplication is namespace-scoped. An empty namespace
// means the media library.
func (c *FilesClient) UploadToNamespace(ctx context.Context, namespace, fileName, mimeType string, content []byte) (File, error) {
	if len(content) == 0 {
		return File{}, fmt.Errorf("integration: Upload requires content")
	}
	if c.apiKey == "" {
		return File{}, errNoAPIKey
	}

	target, err := c.createUploadURL(ctx)
	if err != nil {
		return File{}, err
	}
	if err := c.putObject(ctx, target, content, mimeType); err != nil {
		return File{}, err
	}

	body, err := json.Marshal(finalizeUploadBody{
		UploadKey: target.UploadKey,
		FileName:  fileName,
		MimeType:  mimeType,
		Namespace: namespace,
	})
	if err != nil {
		return File{}, fmt.Errorf("integration: marshal finalize: %w", err)
	}
	req, err := c.bearerRequest(http.MethodPost, "/files", nil, body, false)
	if err != nil {
		return File{}, err
	}
	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return File{}, err
	}
	return decodeFile(resp.Body)
}

// Replace repoints an existing file at new content, keeping its id. Like Upload,
// the bytes go directly to object storage.
func (c *FilesClient) Replace(ctx context.Context, fileID, mimeType string, content []byte) (File, error) {
	if fileID == "" || len(content) == 0 {
		return File{}, fmt.Errorf("integration: Replace requires fileID and content")
	}
	if c.apiKey == "" {
		return File{}, errNoAPIKey
	}

	target, err := c.createUploadURL(ctx)
	if err != nil {
		return File{}, err
	}
	if err := c.putObject(ctx, target, content, mimeType); err != nil {
		return File{}, err
	}

	body, err := json.Marshal(replaceContentBody{
		UploadKey: target.UploadKey,
		MimeType:  mimeType,
	})
	if err != nil {
		return File{}, fmt.Errorf("integration: marshal replace: %w", err)
	}
	req, err := c.bearerRequest(http.MethodPost, "/files/"+fileID+"/content", nil, body, false)
	if err != nil {
		return File{}, err
	}
	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return File{}, err
	}
	return decodeFile(resp.Body)
}

// ListParams filters and paginates a file listing. Before is a keyset cursor
// (return files created strictly before it); Limit caps the page size.
// Namespace filters to one namespace; empty lists every namespace.
type ListParams struct {
	Namespace string
	Before    *time.Time
	Limit     int
}

// List returns the project's live files, newest first.
func (c *FilesClient) List(ctx context.Context, p ListParams) ([]File, error) {
	query := map[string]string{}
	if p.Namespace != "" {
		query["namespace"] = p.Namespace
	}
	if p.Before != nil {
		query["before"] = p.Before.UTC().Format(time.RFC3339)
	}
	if p.Limit > 0 {
		query["limit"] = strconv.Itoa(p.Limit)
	}
	req, err := c.bearerRequest(http.MethodGet, "/files", query, nil, true)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	var out filesListResponse
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, fmt.Errorf("integration: decode files list: %w", err)
	}
	return out.Files, nil
}

// Get returns a single file's metadata (with its public URL).
func (c *FilesClient) Get(ctx context.Context, fileID string) (File, error) {
	if fileID == "" {
		return File{}, fmt.Errorf("integration: Get requires fileID")
	}
	req, err := c.bearerRequest(http.MethodGet, "/files/"+fileID, nil, nil, true)
	if err != nil {
		return File{}, err
	}
	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return File{}, err
	}
	return decodeFile(resp.Body)
}

// Rename updates a file's display name.
func (c *FilesClient) Rename(ctx context.Context, fileID, name string) (File, error) {
	if fileID == "" || name == "" {
		return File{}, fmt.Errorf("integration: Rename requires fileID and name")
	}
	body, err := json.Marshal(map[string]string{"fileName": name})
	if err != nil {
		return File{}, fmt.Errorf("integration: marshal rename: %w", err)
	}
	req, err := c.bearerRequest(http.MethodPatch, "/files/"+fileID, nil, body, false)
	if err != nil {
		return File{}, err
	}
	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return File{}, err
	}
	return decodeFile(resp.Body)
}

// Delete soft-deletes a file.
func (c *FilesClient) Delete(ctx context.Context, fileID string) error {
	if fileID == "" {
		return fmt.Errorf("integration: Delete requires fileID")
	}
	req, err := c.bearerRequest(http.MethodDelete, "/files/"+fileID, nil, nil, false)
	if err != nil {
		return err
	}
	_, err = c.http.Do(ctx, req)
	return err
}

// PurgeNamespace bulk soft-deletes the project's files in one namespace,
// optionally only those not used since lastUsedBefore (nil purges the whole
// namespace). It is how an integration bounds the growth of its own namespace.
// Returns the number of files deleted.
func (c *FilesClient) PurgeNamespace(ctx context.Context, namespace string, lastUsedBefore *time.Time) (int, error) {
	if namespace == "" {
		return 0, fmt.Errorf("integration: PurgeNamespace requires namespace")
	}
	body, err := json.Marshal(purgeNamespaceBody{Namespace: namespace, LastUsedBefore: lastUsedBefore})
	if err != nil {
		return 0, fmt.Errorf("integration: marshal purge: %w", err)
	}
	req, err := c.bearerRequest(http.MethodPost, "/files/purge", nil, body, false)
	if err != nil {
		return 0, err
	}
	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return 0, err
	}
	var out purgeNamespaceResponse
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return 0, fmt.Errorf("integration: decode purge response: %w", err)
	}
	return out.Deleted, nil
}

// Usage returns the project's storage usage snapshot.
func (c *FilesClient) Usage(ctx context.Context) (Usage, error) {
	req, err := c.bearerRequest(http.MethodGet, "/usage", nil, nil, true)
	if err != nil {
		return Usage{}, err
	}
	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return Usage{}, err
	}
	var out Usage
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return Usage{}, fmt.Errorf("integration: decode usage: %w", err)
	}
	return out, nil
}

// createUploadURL asks media-service for a presigned direct-upload target.
func (c *FilesClient) createUploadURL(ctx context.Context) (uploadURLResponse, error) {
	req, err := c.bearerRequest(http.MethodPost, "/files/upload-url", nil, nil, true)
	if err != nil {
		return uploadURLResponse{}, err
	}
	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return uploadURLResponse{}, err
	}
	var out uploadURLResponse
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return uploadURLResponse{}, fmt.Errorf("integration: decode upload url: %w", err)
	}
	if out.URL == "" || out.UploadKey == "" {
		return uploadURLResponse{}, fmt.Errorf("integration: empty upload target")
	}
	return out, nil
}

// putObject uploads content directly to the presigned object-storage URL. It
// sends Content-MD5 so storage verifies integrity and rejects a corrupted
// transfer; the same MD5 becomes the object's ETag, which the service later uses
// as the content hash.
func (c *FilesClient) putObject(ctx context.Context, target uploadURLResponse, content []byte, contentType string) error {
	method := target.Method
	if method == "" {
		method = http.MethodPut
	}
	req, err := http.NewRequestWithContext(ctx, method, target.URL, bytes.NewReader(content))
	if err != nil {
		return fmt.Errorf("integration: build upload request: %w", err)
	}
	req.ContentLength = int64(len(content))
	req.Header.Set("Content-MD5", md5Base64(content))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.putHTTP().Do(req)
	if err != nil {
		return fmt.Errorf("integration: upload to storage: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return fmt.Errorf("integration: upload to storage: status %d: %s", resp.StatusCode, string(snippet))
	}
	return nil
}

func (c *FilesClient) putHTTP() *http.Client {
	if c.putClient != nil {
		return c.putClient
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

func (c *FilesClient) bearerRequest(method, path string, query map[string]string, body []byte, idempotent bool) (httpclient.Request, error) {
	if c.apiKey == "" {
		return httpclient.Request{}, errNoAPIKey
	}
	return httpclient.Request{
		Method:     method,
		Path:       path,
		Query:      query,
		Headers:    map[string]string{"Authorization": "Bearer " + c.apiKey},
		Body:       body,
		Idempotent: idempotent,
	}, nil
}

func md5Base64(content []byte) string {
	sum := md5.Sum(content)
	return base64.StdEncoding.EncodeToString(sum[:])
}

func decodeFile(body []byte) (File, error) {
	var out File
	if err := json.Unmarshal(body, &out); err != nil {
		return File{}, fmt.Errorf("integration: decode file: %w", err)
	}
	return out, nil
}
