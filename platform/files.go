package platform

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// FilesClient targets the backend's project-authorized media library API. The
// integration FilesClient remains responsible for its installation namespaces.
type FilesClient struct{ client *Client }
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
type FileUsage struct {
	ProjectID   string               `json:"projectId"`
	StoredBytes int64                `json:"storedBytes"`
	Namespaces  []FileNamespaceUsage `json:"namespaces"`
}
type FileNamespaceUsage struct {
	Namespace   string `json:"namespace"`
	StoredBytes int64  `json:"storedBytes"`
}
type ListFilesParams struct {
	Before *time.Time
	Limit  int
}

func (f *FilesClient) List(ctx context.Context, projectID string, p ListFilesParams) ([]File, error) {
	if !resourceID.MatchString(projectID) || p.Limit < 0 || p.Limit > 200 {
		return nil, ErrInvalidInput
	}
	q := url.Values{}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	if p.Before != nil {
		q.Set("before", p.Before.UTC().Format(time.RFC3339Nano))
	}
	var out struct {
		Files []File `json:"files"`
	}
	err := f.client.requestJSON(ctx, "list files", http.MethodGet, "/projects/"+projectID+"/files", q, nil, &out)
	return out.Files, err
}
func (f *FilesClient) Get(ctx context.Context, projectID, fileID string) (File, error) {
	return f.fileRequest(ctx, http.MethodGet, "get file", projectID, fileID, nil)
}
func (f *FilesClient) Rename(ctx context.Context, projectID, fileID, fileName string) (File, error) {
	if strings.TrimSpace(fileName) == "" || len(fileName) > 255 {
		return File{}, ErrInvalidInput
	}
	return f.fileRequest(ctx, http.MethodPatch, "rename file", projectID, fileID, map[string]string{"fileName": fileName})
}
func (f *FilesClient) Delete(ctx context.Context, projectID, fileID string) error {
	if !resourceID.MatchString(projectID) || !resourceID.MatchString(fileID) {
		return ErrInvalidInput
	}
	return f.client.requestJSON(ctx, "delete file", http.MethodDelete, "/projects/"+projectID+"/files/"+fileID, nil, nil, nil)
}
func (f *FilesClient) Usage(ctx context.Context, projectID string) (FileUsage, error) {
	if !resourceID.MatchString(projectID) {
		return FileUsage{}, ErrInvalidInput
	}
	var out FileUsage
	err := f.client.requestJSON(ctx, "file usage", http.MethodGet, "/projects/"+projectID+"/files/usage", nil, nil, &out)
	if err == nil && !strings.EqualFold(out.ProjectID, projectID) {
		err = ErrResponse
	}
	return out, err
}
func (f *FilesClient) fileRequest(ctx context.Context, method, op, projectID, fileID string, input any) (File, error) {
	if !resourceID.MatchString(projectID) || !resourceID.MatchString(fileID) {
		return File{}, ErrInvalidInput
	}
	var out File
	err := f.client.requestJSON(ctx, op, method, "/projects/"+projectID+"/files/"+fileID, nil, input, &out)
	if err == nil && !strings.EqualFold(out.ID, fileID) {
		err = ErrResponse
	}
	return out, err
}
