package campaign

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrSourceProofConfig         = errors.New("invalid campaign source proof configuration")
	ErrSourceProofInvalidRequest = errors.New("invalid campaign source proof identity")
	ErrSourceProofUnavailable    = errors.New("campaign source proof unavailable")
	ErrSourceProofNotFound       = errors.New("campaign source proof not found")
)

// SourceProof identifies the permanent integration owner within a project.
// Installation liveness is verified separately through OAuth introspection.
type SourceProof struct {
	ProjectID          string    `json:"projectId"`
	CampaignID         string    `json:"campaignId"`
	Name               string    `json:"name"`
	OwnerIntegrationID string    `json:"ownerIntegrationId"`
	PreparationMode    string    `json:"preparationMode"`
	Status             string    `json:"status"`
	CreatedAt          time.Time `json:"createdAt"`
}

type SourceProofClient struct {
	baseURL, token string
	client         *http.Client
}

// NewSourceProofClient creates an internal, uncached client without redirects or retries.
func NewSourceProofClient(baseURL, token string, timeout time.Duration) (*SourceProofClient, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(baseURL, "#") || strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") || timeout <= 0 || timeout > 10*time.Second {
		return nil, ErrSourceProofConfig
	}
	return &SourceProofClient{baseURL: strings.TrimRight(baseURL, "/"), token: token, client: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *SourceProofClient) Get(ctx context.Context, projectID, campaignID string) (SourceProof, error) {
	if !validProofID(projectID) || !validProofID(campaignID) {
		return SourceProof{}, ErrSourceProofInvalidRequest
	}
	if c == nil || c.client == nil {
		return SourceProof{}, ErrSourceProofUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/internal/projects/"+projectID+"/campaigns/"+campaignID+"/source-proof", nil)
	if err != nil {
		return SourceProof{}, ErrSourceProofUnavailable
	}
	req.Header.Set("X-Internal-Token", c.token)
	req.Header.Set("Accept", "application/json")
	response, err := c.client.Do(req)
	if err != nil {
		return SourceProof{}, ErrSourceProofUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return SourceProof{}, ErrSourceProofNotFound
	}
	if response.StatusCode != http.StatusOK {
		return SourceProof{}, ErrSourceProofUnavailable
	}
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return SourceProof{}, ErrSourceProofUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (16<<10)+1))
	if err != nil || len(raw) > 16<<10 {
		return SourceProof{}, ErrSourceProofUnavailable
	}
	return decodeSourceProof(raw, projectID, campaignID)
}

func decodeSourceProof(raw []byte, projectID, campaignID string) (SourceProof, error) {
	invalid := ErrSourceProofUnavailable
	d := json.NewDecoder(bytes.NewReader(raw))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return SourceProof{}, invalid
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		name, err := d.Token()
		key, ok := name.(string)
		if err != nil || !ok {
			return SourceProof{}, invalid
		}
		if _, exists := fields[key]; exists {
			return SourceProof{}, invalid
		}
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return SourceProof{}, invalid
		}
		fields[key] = value
	}
	last, err := d.Token()
	if err != nil || last != json.Delim('}') {
		return SourceProof{}, invalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return SourceProof{}, invalid
	}
	required := []string{"projectId", "campaignId", "name", "ownerIntegrationId", "preparationMode", "status", "createdAt"}
	if len(fields) != len(required) {
		return SourceProof{}, invalid
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return SourceProof{}, invalid
		}
	}
	var out SourceProof
	if json.Unmarshal(raw, &out) != nil || out.ProjectID != projectID || out.CampaignID != campaignID || !validProofID(out.OwnerIntegrationID) || strings.TrimSpace(out.Name) == "" || out.CreatedAt.IsZero() {
		return SourceProof{}, invalid
	}
	switch out.PreparationMode {
	case "legacyProjection", "executionSnapshot":
	default:
		return SourceProof{}, invalid
	}
	switch out.Status {
	case "draft", "snapshotting", "preparing", "awaiting_audience", "running", "done", "failed", "cancelled":
	default:
		return SourceProof{}, invalid
	}
	return out, nil
}
func validProofID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}
