package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
)

// Paths are relative to the configured ExecutionURL, which already carries the
// gateway's "/api/execution" prefix.
const (
	activatePath = "/integrations/triggers/activate"
	triggersPath = "/integrations/triggers"
)

// TriggersClient activates and lists integration triggers using ExecutionOAuth.
type TriggersClient struct {
	oauth *executionOAuth
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
	Event                  *ActivationEvent
	// Metadata is this integration's namespace object. The platform places it
	// under context.metadata.<integrationSlug> on the activated branch.
	Metadata       json.RawMessage
	IdempotencyKey string
}

// ActivationEvent is an immutable, bounded event snapshot attached to the
// activated execution context. The platform owns its envelope, while Data is
// interpreted only by the integration that sent it.
type ActivationEvent struct {
	EventID string          `json:"eventId"`
	Kind    string          `json:"kind"`
	Version int             `json:"version"`
	Data    json.RawMessage `json:"data"`
}

type ActivationResult struct {
	ExecutionContextIDs []string `json:"executionContextIds"`
	MatchedTriggers     int      `json:"matchedTriggers"`
	SkippedOnce         int      `json:"skippedOnce"`
	SkippedDuplicate    int      `json:"skippedDuplicate"`
}

type activateBody struct {
	ProjectID              string           `json:"projectId"`
	ActivationKey          string           `json:"activationKey"`
	SubjectID              string           `json:"subjectId,omitempty"`
	IntegrationSubjectType string           `json:"integrationSubjectType,omitempty"`
	IntegrationSubjectID   string           `json:"integrationSubjectId,omitempty"`
	Event                  *ActivationEvent `json:"event,omitempty"`
	Metadata               json.RawMessage  `json:"metadata,omitempty"`
	IdempotencyKey         string           `json:"idempotencyKey,omitempty"`
}

// Activate fires the matching trigger(s) and returns the ids of the trigger
// contexts that were created or refreshed (one per matching trigger step). The
// platform verifies authorization and the installation in the project. OAuth
// additionally checks subject ownership and does not automatically retry this
// write.
func (c *TriggersClient) Activate(ctx context.Context, p ActivateParams) ([]string, error) {
	result, err := c.ActivateDetailed(ctx, p)
	return result.ExecutionContextIDs, err
}

// ActivateDetailed also reports whether a matching trigger was suppressed by
// its once-per-subject policy. A zero-ID result is not necessarily an unknown
// activation key.
func (c *TriggersClient) ActivateDetailed(ctx context.Context, p ActivateParams) (ActivationResult, error) {
	if p.ProjectID == "" {
		return ActivationResult{}, fmt.Errorf("integration: Activate requires ProjectID")
	}
	if p.ActivationKey == "" {
		return ActivationResult{}, fmt.Errorf("integration: Activate requires ActivationKey")
	}
	bySubjectID := p.SubjectID != ""
	byExternalID := p.IntegrationSubjectID != ""
	if bySubjectID == byExternalID {
		return ActivationResult{}, fmt.Errorf("integration: Activate requires exactly one of SubjectID or IntegrationSubjectID")
	}

	body, err := json.Marshal(activateBody{
		ProjectID:              p.ProjectID,
		ActivationKey:          p.ActivationKey,
		SubjectID:              p.SubjectID,
		IntegrationSubjectType: p.IntegrationSubjectType,
		IntegrationSubjectID:   p.IntegrationSubjectID,
		Event:                  p.Event,
		Metadata:               p.Metadata,
		IdempotencyKey:         p.IdempotencyKey,
	})
	if err != nil {
		return ActivationResult{}, fmt.Errorf("integration: marshal activate: %w", err)
	}

	if c.oauth == nil {
		return ActivationResult{}, fmt.Errorf("integration: execution OAuth is required for Activate")
	}
	if p.ProjectID != c.oauth.projectID {
		return ActivationResult{}, integrationoauth.ErrRequest
	}
	var out ActivationResult
	err = c.oauth.call(ctx, c.oauth.triggers, http.MethodPost, activatePath, nil, body, false, http.StatusAccepted, &out)
	if err == nil && out.ExecutionContextIDs == nil {
		return ActivationResult{}, errExecutionOAuthResponse
	}
	return out, err
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
	if c.oauth == nil {
		return TriggerListing{}, fmt.Errorf("integration: execution OAuth is required for ListTriggers")
	}
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
