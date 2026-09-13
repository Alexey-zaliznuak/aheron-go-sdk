package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/httpclient"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/sign"
)

// Paths are relative to the configured ExecutionURL, which already carries the
// gateway's "/api/execution" prefix.
const (
	activatePath = "/integrations/triggers/activate"
	triggersPath = "/integrations/triggers"
)

// TriggersClient activates and lists integration triggers using ExecutionOAuth
// when configured, or the integration's legacy signature otherwise.
type TriggersClient struct {
	oauth  *executionOAuth
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
// platform verifies authorization and the installation in the project. OAuth
// additionally checks subject ownership and does not automatically retry this
// write. The legacy transport retains its existing retry policy.
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

	if c.oauth != nil {
		if p.ProjectID != c.oauth.projectID {
			return nil, integrationoauth.ErrRequest
		}
		var out activateResponse
		err := c.oauth.call(ctx, c.oauth.triggers, http.MethodPost, activatePath, nil, body, false, http.StatusAccepted, &out)
		if err == nil && out.ExecutionContextIDs == nil {
			return nil, errExecutionOAuthResponse
		}
		return out.ExecutionContextIDs, err
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

// TriggerListing is the versioned result of ListTriggers: the trigger instances
// of a block type in a project plus the ConfigVersion they were listed at.
//
// ConfigVersion is the per-(project, integration) counter the platform bumps on
// every trigger-configuration change (the same value carried by
// TriggerSyncRequest). Use it to guard the local rule snapshot: only replace the
// snapshot when a freshly listed ConfigVersion is greater than the one already
// applied, so out-of-order syncs and TTL-based refreshes converge on the same
// state. It is 0 when the platform is too old to report a version.
type TriggerListing struct {
	ConfigVersion int64             `json:"configVersion"`
	Triggers      []TriggerInstance `json:"triggers"`
}

// List returns the trigger instances of a block type in a project, scoped to
// this integration. Authentication follows the client configuration. It
// delegates to ListTriggers and drops the version; use ListTriggers when you
// need the ConfigVersion to guard a local snapshot.
func (c *TriggersClient) List(ctx context.Context, projectID, blockKey string) ([]TriggerInstance, error) {
	listing, err := c.ListTriggers(ctx, projectID, blockKey)
	if err != nil {
		return nil, err
	}
	return listing.Triggers, nil
}

// ListTriggers returns the trigger instances of a block type in a project,
// scoped to this integration, together with the ConfigVersion they were listed
// at. It uses OAuth or a signed GET according to configuration, to the same
// endpoint as List.
//
// Pair it with HandleTriggerSync: when a sync ping's ConfigVersion is newer than
// the stored one, call ListTriggers and atomically replace the local rule
// snapshot guarded by the returned ConfigVersion. Because the listing itself
// carries the version, a TTL-based fallback resync uses the very same guard.
func (c *TriggersClient) ListTriggers(ctx context.Context, projectID, blockKey string) (TriggerListing, error) {
	if projectID == "" {
		return TriggerListing{}, fmt.Errorf("integration: ListTriggers requires projectID")
	}
	if blockKey == "" {
		return TriggerListing{}, fmt.Errorf("integration: ListTriggers requires blockKey")
	}

	query := map[string]string{"projectId": projectID, "blockKey": blockKey}
	if c.oauth != nil {
		if projectID != c.oauth.projectID {
			return TriggerListing{}, integrationoauth.ErrRequest
		}
		var out TriggerListing
		err := c.oauth.call(ctx, c.oauth.triggers, http.MethodGet, triggersPath, query, nil, false, http.StatusOK, &out)
		if err == nil && (out.Triggers == nil || out.ConfigVersion < 0) {
			return TriggerListing{}, errExecutionOAuthResponse
		}
		return out, err
	}
	req, err := buildSignedRequest(c.signer, c.id, http.MethodGet, triggersPath, query, nil, true)
	if err != nil {
		return TriggerListing{}, err
	}
	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return TriggerListing{}, err
	}
	var out TriggerListing
	if len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, &out); err != nil {
			return TriggerListing{}, fmt.Errorf("integration: decode triggers response: %w", err)
		}
	}
	return out, nil
}
