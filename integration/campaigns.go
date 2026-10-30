package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
)

// CampaignOAuthConfig binds campaign operations to an active project installation.
type CampaignOAuthConfig struct {
	Provider       *integrationoauth.Provider
	ProjectID      string
	InstallationID string
	HTTPClient     *http.Client
}

type CampaignClient struct {
	projectID, baseURL, backendURL string
	read, write, triggers          *integrationoauth.Client
}

type Campaign struct {
	PreparationMode    string          `json:"preparationMode,omitempty"`
	OwnerIntegrationID *string         `json:"ownerIntegrationId,omitempty"`
	ID                 string          `json:"id"`
	ProjectID          string          `json:"projectId"`
	Name               string          `json:"name"`
	Status             string          `json:"status"`
	Filter             json.RawMessage `json:"filter"`
	RequiredFilter     json.RawMessage `json:"requiredFilter"`
	Metadata           json.RawMessage `json:"metadata"`
	Revision           uint64          `json:"revision"`
	ActivationKey      string          `json:"activationKey"`
	TotalRecipients    int             `json:"totalRecipients"`
	ProcessedCount     int             `json:"processedCount"`
	Error              *string         `json:"error"`
	CreatedAt          time.Time       `json:"createdAt"`
	StartedAt          *time.Time      `json:"startedAt"`
	SnapshotFinishedAt *time.Time      `json:"snapshotFinishedAt"`
	FinishedAt         *time.Time      `json:"finishedAt"`
}

// CommandID is a stable UUID per create intent. Reuse it after an ambiguous
// response with the same body; a changed body needs a new command ID.
type CreateCampaignParams struct {
	PreparationMode string          `json:"preparationMode,omitempty"`
	CommandID       string          `json:"commandId"`
	Name            string          `json:"name"`
	ActivationKey   string          `json:"activationKey"`
	Filter          json.RawMessage `json:"filter,omitempty"`
	RequiredFilter  json.RawMessage `json:"requiredFilter,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
}

type UpdateCampaignParams struct {
	ExpectedRevision uint64           `json:"expectedRevision"`
	Name             *string          `json:"name,omitempty"`
	ActivationKey    *string          `json:"activationKey,omitempty"`
	Filter           *json.RawMessage `json:"filter,omitempty"`
	RequiredFilter   *json.RawMessage `json:"requiredFilter,omitempty"`
	Metadata         *json.RawMessage `json:"metadata,omitempty"`
}

// PreparedAudience identifies the immutable audience whose initial state has
// been durably recorded by the preparing integration.
type PreparedAudience struct {
	CampaignCreatedAt  time.Time `json:"campaignCreatedAt"`
	ExpectedRecipients uint64    `json:"expectedRecipients"`
}

type NamedTrigger struct {
	ActivationKey string `json:"activationKey"`
	Name          string `json:"name"`
	SchemeID      string `json:"schemeId"`
	SchemeName    string `json:"schemeName"`
	StepID        string `json:"stepId"`
}

func newCampaignClient(baseURL, backendURL string, cfg *CampaignOAuthConfig) (*CampaignClient, error) {
	c := &CampaignClient{baseURL: strings.TrimRight(baseURL, "/"), backendURL: strings.TrimRight(backendURL, "/")}
	if cfg == nil {
		return c, nil
	}
	c.projectID = cfg.ProjectID
	for _, op := range []struct {
		target                   **integrationoauth.Client
		baseURL, audience, scope string
	}{
		{&c.read, baseURL, "campaign", "campaigns.read"},
		{&c.write, baseURL, "campaign", "campaigns.write"},
		{&c.triggers, backendURL, "backend", "campaigns.read"},
	} {
		client, err := integrationoauth.NewClient(integrationoauth.ClientConfig{Provider: cfg.Provider, BaseURL: op.baseURL, HTTPClient: cfg.HTTPClient, TokenRequest: integrationoauth.Request{ProjectID: cfg.ProjectID, InstallationID: cfg.InstallationID, Audience: op.audience, Scopes: []string{op.scope}}})
		if err != nil {
			return nil, err
		}
		*op.target = client
	}
	return c, nil
}

func (c *CampaignClient) projectPath(projectID string) (string, error) {
	if c == nil || c.read == nil || projectID != c.projectID || !integrationOAuthUUID(projectID) {
		return "", integrationoauth.ErrRequest
	}
	return "/projects/" + projectID + "/campaigns", nil
}

func (c *CampaignClient) campaignPath(projectID, id string) (string, error) {
	path, err := c.projectPath(projectID)
	if err != nil {
		return "", err
	}
	if !integrationOAuthUUID(id) {
		return "", integrationoauth.ErrRequest
	}
	return path + "/" + id, nil
}

var errCampaignResponse = errors.New("integration: invalid campaign response")

func callResourceJSON(ctx context.Context, client *integrationoauth.Client, baseURL, method, path string, input, out any, idempotent bool, statuses ...int) error {
	var raw []byte
	var err error
	if input != nil {
		raw, err = json.Marshal(input)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return integrationoauth.ErrRequest
	}
	req.Header.Set("Accept", "application/json")
	if input != nil {
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
		return &APIError{Method: method, URL: path, Status: resp.StatusCode, Message: "resource request rejected"}
	}
	valid := false
	for _, status := range statuses {
		if status == resp.StatusCode {
			valid = true
		}
	}
	if !valid {
		return errCampaignResponse
	}
	if out == nil {
		return nil
	}
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return errCampaignResponse
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(body) > 2<<20 || bytes.Equal(bytes.TrimSpace(body), []byte("null")) || json.Unmarshal(body, out) != nil {
		return errCampaignResponse
	}
	return nil
}

func (c *CampaignClient) List(ctx context.Context, projectID string) ([]Campaign, error) {
	path, err := c.projectPath(projectID)
	if err != nil {
		return nil, err
	}
	var out struct {
		Campaigns []Campaign `json:"campaigns"`
	}
	err = callResourceJSON(ctx, c.read, c.baseURL, http.MethodGet, path, nil, &out, true, 200)
	if err == nil && out.Campaigns == nil {
		return nil, errCampaignResponse
	}
	return out.Campaigns, err
}

func (c *CampaignClient) Get(ctx context.Context, projectID, id string) (Campaign, error) {
	path, err := c.campaignPath(projectID, id)
	if err != nil {
		return Campaign{}, err
	}
	var out Campaign
	err = callResourceJSON(ctx, c.read, c.baseURL, http.MethodGet, path, nil, &out, true, 200)
	return out, err
}

func (c *CampaignClient) Create(ctx context.Context, projectID string, input CreateCampaignParams) (Campaign, error) {
	path, err := c.projectPath(projectID)
	if err != nil {
		return Campaign{}, err
	}
	if !integrationOAuthUUID(input.CommandID) {
		return Campaign{}, integrationoauth.ErrRequest
	}
	var out Campaign
	err = callResourceJSON(ctx, c.write, c.baseURL, http.MethodPost, path, input, &out, true, 200, 201)
	return out, err
}

func (c *CampaignClient) Update(ctx context.Context, projectID, id string, input UpdateCampaignParams) (Campaign, error) {
	path, err := c.campaignPath(projectID, id)
	if err != nil {
		return Campaign{}, err
	}
	var out Campaign
	err = callResourceJSON(ctx, c.write, c.baseURL, http.MethodPatch, path, input, &out, false, 200)
	return out, err
}

func (c *CampaignClient) Launch(ctx context.Context, projectID, id string, expectedRevision uint64) (Campaign, error) {
	path, err := c.campaignPath(projectID, id)
	if err != nil {
		return Campaign{}, err
	}
	input := struct {
		ExpectedRevision uint64 `json:"expectedRevision"`
	}{expectedRevision}
	var out Campaign
	err = callResourceJSON(ctx, c.write, c.baseURL, http.MethodPost, path+"/launch", input, &out, false, 200, 202)
	return out, err
}

// Prepare starts a full audience snapshot; the current server releases execution
// once that snapshot is durable, without waiting for analytics.
// It requires a server supporting this endpoint; there is no fallback to Launch.
func (c *CampaignClient) Prepare(ctx context.Context, projectID, id string, expectedRevision uint64) (Campaign, error) {
	path, err := c.campaignPath(projectID, id)
	if err != nil {
		return Campaign{}, err
	}
	input := struct {
		ExpectedRevision uint64 `json:"expectedRevision"`
	}{expectedRevision}
	var out Campaign
	err = callResourceJSON(ctx, c.write, c.baseURL, http.MethodPost, path+"/prepare", input, &out, false, 200, 202)
	return out, err
}

// StartPrepared acknowledges a legacy prepared campaign for compatibility.
// Current campaign execution does not wait for this call.
// Repeating the same acknowledgement is safe and never revives cancellation.
func (c *CampaignClient) StartPrepared(ctx context.Context, projectID, id string, audience PreparedAudience) (Campaign, error) {
	path, err := c.campaignPath(projectID, id)
	if err != nil {
		return Campaign{}, err
	}
	if audience.CampaignCreatedAt.IsZero() {
		return Campaign{}, integrationoauth.ErrRequest
	}
	var out Campaign
	err = callResourceJSON(ctx, c.write, c.baseURL, http.MethodPost, path+"/start-prepared", audience, &out, true, 200)
	return out, err
}

func (c *CampaignClient) Cancel(ctx context.Context, projectID, id string) (Campaign, error) {
	path, err := c.campaignPath(projectID, id)
	if err != nil {
		return Campaign{}, err
	}
	var out Campaign
	err = callResourceJSON(ctx, c.write, c.baseURL, http.MethodPost, path+"/cancel", nil, &out, false, 200, 202)
	return out, err
}

func (c *CampaignClient) ListNamedTriggers(ctx context.Context, projectID string) ([]NamedTrigger, error) {
	if _, err := c.projectPath(projectID); err != nil {
		return nil, err
	}
	var out struct {
		Triggers []NamedTrigger `json:"triggers"`
	}
	err := callResourceJSON(ctx, c.triggers, c.backendURL, http.MethodGet, "/integrations/projects/"+projectID+"/named-triggers", nil, &out, true, 200)
	if err == nil && out.Triggers == nil {
		return nil, errCampaignResponse
	}
	return out.Triggers, err
}
