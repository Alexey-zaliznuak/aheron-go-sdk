package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/httpclient"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/sign"
)

// Paths are relative to the configured ExecutionURL, which already carries the
// gateway's "/api/execution" prefix.
const (
	activatePath = "/integrations/triggers/activate"
	triggersPath = "/integrations/triggers"
)

// TriggersClient activates integration triggers and lists trigger instances. All
// calls are signed with the integration's private key.
type TriggersClient struct {
	http   *httpclient.Client
	id     string
	signer *sign.Signer
}

// ActivateParams starts a trigger for a subject in a project. The subject is
// addressed in exactly one of two mutually exclusive modes:
//
//   - by the platform's internal SubjectID (a uuid you already know); or
//   - by the integration's own external identity: IntegrationSubjectID (plus an
//     optional IntegrationSubjectType, defaulting server-side to "user"), which
//     the platform resolves to a subject through the CRM.
//
// ActivationKey selects which declared trigger(s) fire.
type ActivateParams struct {
	ProjectID              string
	ActivationKey          string
	SubjectID              string
	IntegrationSubjectType string
	IntegrationSubjectID   string
}

type activateBody struct {
	ProjectID              string `json:"projectId"`
	ActivationKey          string `json:"activationKey"`
	SubjectID              string `json:"subjectId,omitempty"`
	IntegrationSubjectType string `json:"integrationSubjectType,omitempty"`
	IntegrationSubjectID   string `json:"integrationSubjectId,omitempty"`
}

type activateResponse struct {
	ExecutionContextIDs []string `json:"executionContextIds"`
}

// Activate fires the matching trigger(s) and returns the ids of the trigger
// contexts that were created or refreshed (one per matching trigger step). The
// platform verifies the signature and that the integration is installed in the
// project. Activation is deduplicated server-side, so this call is retried on
// transient failures.
func (c *TriggersClient) Activate(ctx context.Context, p ActivateParams) ([]string, error) {
	if p.ProjectID == "" {
		return nil, fmt.Errorf("integration: Activate requires ProjectID")
	}
	if p.ActivationKey == "" {
		return nil, fmt.Errorf("integration: Activate requires ActivationKey")
	}
	bySubjectID := p.SubjectID != ""
	byExternalID := p.IntegrationSubjectID != ""
	if bySubjectID == byExternalID {
		return nil, fmt.Errorf("integration: Activate requires exactly one of SubjectID or IntegrationSubjectID")
	}

	body, err := json.Marshal(activateBody{
		ProjectID:              p.ProjectID,
		ActivationKey:          p.ActivationKey,
		SubjectID:              p.SubjectID,
		IntegrationSubjectType: p.IntegrationSubjectType,
		IntegrationSubjectID:   p.IntegrationSubjectID,
	})
	if err != nil {
		return nil, fmt.Errorf("integration: marshal activate: %w", err)
	}

	req, err := buildSignedRequest(c.signer, c.id, http.MethodPost, activatePath, nil, body, true)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	var out activateResponse
	if len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, &out); err != nil {
			return nil, fmt.Errorf("integration: decode activate response: %w", err)
		}
	}
	return out.ExecutionContextIDs, nil
}

// TriggerActivation is one declared activationKey -> outputKey mapping of a
// trigger instance.
type TriggerActivation struct {
	ActivationKey string `json:"activationKey"`
	OutputKey     string `json:"outputKey"`
}

// TriggerInstance is a single integration trigger step placed on a scheme, with
// its declared activation keys and their outputs. Settings is the step's raw
// settings document as saved by the block's settings editor, so an integration
// can rebuild its own rule registry (e.g. message-matching patterns) from the
// listing alone.
type TriggerInstance struct {
	SchemeID    string              `json:"schemeId"`
	StepID      string              `json:"stepId"`
	BlockKey    string              `json:"blockKey"`
	Settings    json.RawMessage     `json:"settings,omitempty"`
	Activations []TriggerActivation `json:"activations"`
}

type listTriggersResponse struct {
	Triggers []TriggerInstance `json:"triggers"`
}

// List returns the trigger instances of a block type in a project, scoped to
// this integration. It is a signed GET (the signature covers an empty body).
func (c *TriggersClient) List(ctx context.Context, projectID, blockKey string) ([]TriggerInstance, error) {
	if projectID == "" {
		return nil, fmt.Errorf("integration: List requires projectID")
	}
	if blockKey == "" {
		return nil, fmt.Errorf("integration: List requires blockKey")
	}

	query := map[string]string{"projectId": projectID, "blockKey": blockKey}
	req, err := buildSignedRequest(c.signer, c.id, http.MethodGet, triggersPath, query, nil, true)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	var out listTriggersResponse
	if len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, &out); err != nil {
			return nil, fmt.Errorf("integration: decode triggers response: %w", err)
		}
	}
	return out.Triggers, nil
}
