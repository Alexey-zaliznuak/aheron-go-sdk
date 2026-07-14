package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/httpclient"
)

// CRMClient reads and writes subject (lead) data in the platform CRM, authorized
// by the project API key granted to the integration at install time. Paths are
// relative to the configured CRMURL, which already carries the "/api/crm" gateway
// prefix.
type CRMClient struct {
	http   *httpclient.Client
	apiKey string
}

// WithAPIKey returns a copy of the client that authenticates with apiKey instead
// of the key configured on the parent Client. It shares the underlying HTTP
// transport, so it is cheap to derive per request or per project.
//
// Use it when a single integration process acts on behalf of many projects, each
// with its own project API key (for example one delivered per project on
// install): keep one keyless base client and derive c.WithAPIKey(projectKey) at
// call time, rather than constructing a full Client per project.
func (c *CRMClient) WithAPIKey(apiKey string) *CRMClient {
	clone := *c
	clone.apiKey = apiKey
	return &clone
}

// Field is a single (field, value) pair used in subject upserts. Field is a
// variable key, a built-in field ("displayName", "description") or a variable
// definition UUID. Value is any JSON-encodable value (for built-in text fields
// pass a string).
type Field struct {
	Field string
	Value any
}

// Subject is the CRM representation of a subject (lead).
type Subject struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"projectId"`
	DisplayName *string   `json:"displayName,omitempty"`
	Description *string   `json:"description,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

// SubjectVariableValue is a stored (or default-derived) variable value on a
// subject.
type SubjectVariableValue struct {
	ID                   string          `json:"id"`
	SubjectID            string          `json:"subjectId"`
	ProjectID            string          `json:"projectId"`
	VariableDefinitionID string          `json:"variableDefinitionId"`
	Value                json.RawMessage `json:"value"`
	IsDefault            bool            `json:"isDefault,omitempty"`
	CreatedAt            time.Time       `json:"createdAt"`
}

// UpsertSubjectParams locates a subject by the AND of all Find criteria: when
// none match it is created from Create; when exactly one matches (or
// OnMultiple="first") it is updated from Update. Find must be non-empty.
type UpsertSubjectParams struct {
	Find   []Field
	Create []Field
	Update []Field
	// OnMultiple controls behavior when Find matches more than one subject:
	// "conflict" (default) fails; "first" updates the oldest match.
	OnMultiple string
	// IntegrationID scopes bare field-key resolution to an integration: a key in
	// Find/Create/Update first matches that integration's own variable before
	// falling back to a project variable. Set it to this integration's platform
	// id to target an integration-owned variable (created via
	// CreateIntegrationVariableDefinition) unambiguously even when a project
	// variable shares the key. Empty keeps the default project-first resolution.
	IntegrationID string
}

// UpsertSubjectResult reports whether a new subject was created and returns the
// subject with its variable values.
type UpsertSubjectResult struct {
	Created bool                   `json:"created"`
	Subject Subject                `json:"subject"`
	Values  []SubjectVariableValue `json:"values"`
}

type fieldWire struct {
	Field string          `json:"field"`
	Value json.RawMessage `json:"value"`
}

type upsertSubjectBody struct {
	Find          []fieldWire `json:"find"`
	Create        []fieldWire `json:"create,omitempty"`
	Update        []fieldWire `json:"update,omitempty"`
	OnMultiple    string      `json:"onMultiple,omitempty"`
	IntegrationID string      `json:"integrationId,omitempty"`
}

// UpsertSubject creates or updates a subject. It is the primary way an
// integration maps its external identity to a platform subject.
func (c *CRMClient) UpsertSubject(ctx context.Context, projectID string, p UpsertSubjectParams) (UpsertSubjectResult, error) {
	if projectID == "" {
		return UpsertSubjectResult{}, fmt.Errorf("integration: UpsertSubject requires projectID")
	}
	find, err := fieldsToWire(p.Find)
	if err != nil {
		return UpsertSubjectResult{}, err
	}
	create, err := fieldsToWire(p.Create)
	if err != nil {
		return UpsertSubjectResult{}, err
	}
	update, err := fieldsToWire(p.Update)
	if err != nil {
		return UpsertSubjectResult{}, err
	}

	body, err := json.Marshal(upsertSubjectBody{Find: find, Create: create, Update: update, OnMultiple: p.OnMultiple, IntegrationID: p.IntegrationID})
	if err != nil {
		return UpsertSubjectResult{}, fmt.Errorf("integration: marshal upsert: %w", err)
	}

	req, err := c.bearerRequest(http.MethodPost, "/projects/"+projectID+"/subjects/upsert", nil, body, false)
	if err != nil {
		return UpsertSubjectResult{}, err
	}
	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return UpsertSubjectResult{}, err
	}
	var out UpsertSubjectResult
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return UpsertSubjectResult{}, fmt.Errorf("integration: decode upsert response: %w", err)
	}
	return out, nil
}

// GetSubject fetches a subject by id.
func (c *CRMClient) GetSubject(ctx context.Context, projectID, subjectID string) (Subject, error) {
	if projectID == "" || subjectID == "" {
		return Subject{}, fmt.Errorf("integration: GetSubject requires projectID and subjectID")
	}
	req, err := c.bearerRequest(http.MethodGet, "/projects/"+projectID+"/subjects/"+subjectID, nil, nil, true)
	if err != nil {
		return Subject{}, err
	}
	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return Subject{}, err
	}
	var out Subject
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return Subject{}, fmt.Errorf("integration: decode subject: %w", err)
	}
	return out, nil
}

// ListSubjectVariables returns a subject's variable values (including synthetic
// defaults).
func (c *CRMClient) ListSubjectVariables(ctx context.Context, projectID, subjectID string) ([]SubjectVariableValue, error) {
	if projectID == "" || subjectID == "" {
		return nil, fmt.Errorf("integration: ListSubjectVariables requires projectID and subjectID")
	}
	req, err := c.bearerRequest(http.MethodGet, "/projects/"+projectID+"/subjects/"+subjectID+"/variable-values", nil, nil, true)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	var out []SubjectVariableValue
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, fmt.Errorf("integration: decode variable values: %w", err)
	}
	return out, nil
}

// VariableValue is one (variable definition, value) pair for a batch update.
type VariableValue struct {
	VariableDefinitionID string
	Value                any
}

type variableValueWire struct {
	VariableDefinitionID string          `json:"variableDefinitionId"`
	Value                json.RawMessage `json:"value"`
}

// SetSubjectVariables upserts a batch of variable values on a subject (PUT). It
// is idempotent, so it is retried on transient failures.
func (c *CRMClient) SetSubjectVariables(ctx context.Context, projectID, subjectID string, values []VariableValue) ([]SubjectVariableValue, error) {
	if projectID == "" || subjectID == "" {
		return nil, fmt.Errorf("integration: SetSubjectVariables requires projectID and subjectID")
	}
	wire := make([]variableValueWire, 0, len(values))
	for i, v := range values {
		raw, err := json.Marshal(v.Value)
		if err != nil {
			return nil, fmt.Errorf("integration: marshal variable value[%d]: %w", i, err)
		}
		wire = append(wire, variableValueWire{VariableDefinitionID: v.VariableDefinitionID, Value: raw})
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("integration: marshal variable values: %w", err)
	}
	req, err := c.bearerRequest(http.MethodPut, "/projects/"+projectID+"/subjects/"+subjectID+"/variable-values", nil, body, true)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	var out []SubjectVariableValue
	if len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, &out); err != nil {
			return nil, fmt.Errorf("integration: decode variable values: %w", err)
		}
	}
	return out, nil
}

// VariableDefinition is a subject variable definition in a project: a typed,
// named slot (identified by Key) that subjects can carry values for.
type VariableDefinition struct {
	ID           string          `json:"id"`
	ProjectID    string          `json:"projectId"`
	Name         string          `json:"name"`
	Key          string          `json:"key"`
	Type         string          `json:"type"`
	Description  *string         `json:"description,omitempty"`
	DefaultValue json.RawMessage `json:"defaultValue,omitempty"`
	// OwnerType is "project" or "integration". IntegrationID is set only for
	// integration-owned definitions.
	OwnerType     string    `json:"ownerType"`
	IntegrationID *string   `json:"integrationId,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
}

// ListVariableDefinitionsParams narrows ListVariableDefinitions. The zero value
// lists every definition of the project.
type ListVariableDefinitionsParams struct {
	// OwnerType filters by ownership: "project" or "integration". Empty lists both.
	OwnerType string
	// IntegrationID keeps only definitions owned by that integration.
	IntegrationID string
}

// ListVariableDefinitions returns the project's subject variable definitions,
// optionally filtered by owner. Integrations use it to offer the author a
// picker of existing variables (e.g. "save the reply into variable X") instead
// of a free-form key that may silently not exist.
func (c *CRMClient) ListVariableDefinitions(ctx context.Context, projectID string, p ListVariableDefinitionsParams) ([]VariableDefinition, error) {
	if projectID == "" {
		return nil, fmt.Errorf("integration: ListVariableDefinitions requires projectID")
	}
	var query map[string]string
	if p.OwnerType != "" || p.IntegrationID != "" {
		query = make(map[string]string, 2)
		if p.OwnerType != "" {
			query["ownerType"] = p.OwnerType
		}
		if p.IntegrationID != "" {
			query["integrationId"] = p.IntegrationID
		}
	}
	req, err := c.bearerRequest(http.MethodGet, "/projects/"+projectID+"/variable-definitions", query, nil, true)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	var out []VariableDefinition
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return nil, fmt.Errorf("integration: decode variable definitions: %w", err)
	}
	return out, nil
}

// CreateVariableDefinitionParams is the payload to create a subject variable
// definition. Name and Key are required. Type defaults to "string" server-side
// when empty. DefaultValue, when non-nil, is JSON-encoded.
type CreateVariableDefinitionParams struct {
	Name         string
	Key          string
	Type         string
	Description  *string
	DefaultValue any
}

type createVariableDefinitionBody struct {
	Name         string          `json:"name"`
	Key          string          `json:"key"`
	Type         string          `json:"type,omitempty"`
	Description  *string         `json:"description,omitempty"`
	DefaultValue json.RawMessage `json:"defaultValue,omitempty"`
}

// CreateVariableDefinition creates a subject variable definition in a project. It
// returns *APIError with status 409 when a definition with the same key already
// exists; use EnsureVariableDefinition when you want that treated as success.
func (c *CRMClient) CreateVariableDefinition(ctx context.Context, projectID string, p CreateVariableDefinitionParams) (VariableDefinition, error) {
	if projectID == "" {
		return VariableDefinition{}, fmt.Errorf("integration: CreateVariableDefinition requires projectID")
	}
	if p.Name == "" || p.Key == "" {
		return VariableDefinition{}, fmt.Errorf("integration: CreateVariableDefinition requires name and key")
	}

	var defaultRaw json.RawMessage
	if p.DefaultValue != nil {
		raw, err := json.Marshal(p.DefaultValue)
		if err != nil {
			return VariableDefinition{}, fmt.Errorf("integration: marshal default value: %w", err)
		}
		defaultRaw = raw
	}

	body, err := json.Marshal(createVariableDefinitionBody{
		Name:         p.Name,
		Key:          p.Key,
		Type:         p.Type,
		Description:  p.Description,
		DefaultValue: defaultRaw,
	})
	if err != nil {
		return VariableDefinition{}, fmt.Errorf("integration: marshal variable definition: %w", err)
	}

	req, err := c.bearerRequest(http.MethodPost, "/projects/"+projectID+"/variable-definitions", nil, body, false)
	if err != nil {
		return VariableDefinition{}, err
	}
	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return VariableDefinition{}, err
	}
	var out VariableDefinition
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return VariableDefinition{}, fmt.Errorf("integration: decode variable definition: %w", err)
	}
	return out, nil
}

// EnsureVariableDefinition creates a variable definition, treating an
// already-exists conflict (HTTP 409) as success. It is idempotent, so it is safe
// to call repeatedly (for example on every install or process start) to
// guarantee a definition exists before upserting subjects by its key. It does
// not update an existing definition.
func (c *CRMClient) EnsureVariableDefinition(ctx context.Context, projectID string, p CreateVariableDefinitionParams) error {
	if _, err := c.CreateVariableDefinition(ctx, projectID, p); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict {
			return nil
		}
		return err
	}
	return nil
}

// CreateIntegrationVariableDefinition creates an integration-owned variable
// definition in a project, owned by integrationID. Unlike
// CreateVariableDefinition (which creates a project-owned definition), the
// resulting definition lives in the integration's own owner namespace: its key
// may coincide with a project variable's key or another integration's key
// without conflicting. integrationID must be this integration's platform id.
//
// It returns *APIError with status 409 when a definition with the same key
// already exists for this integration; use EnsureIntegrationVariableDefinition
// when you want that treated as success.
func (c *CRMClient) CreateIntegrationVariableDefinition(ctx context.Context, projectID, integrationID string, p CreateVariableDefinitionParams) (VariableDefinition, error) {
	if projectID == "" || integrationID == "" {
		return VariableDefinition{}, fmt.Errorf("integration: CreateIntegrationVariableDefinition requires projectID and integrationID")
	}
	if p.Name == "" || p.Key == "" {
		return VariableDefinition{}, fmt.Errorf("integration: CreateIntegrationVariableDefinition requires name and key")
	}

	var defaultRaw json.RawMessage
	if p.DefaultValue != nil {
		raw, err := json.Marshal(p.DefaultValue)
		if err != nil {
			return VariableDefinition{}, fmt.Errorf("integration: marshal default value: %w", err)
		}
		defaultRaw = raw
	}

	body, err := json.Marshal(createVariableDefinitionBody{
		Name:         p.Name,
		Key:          p.Key,
		Type:         p.Type,
		Description:  p.Description,
		DefaultValue: defaultRaw,
	})
	if err != nil {
		return VariableDefinition{}, fmt.Errorf("integration: marshal variable definition: %w", err)
	}

	req, err := c.bearerRequest(http.MethodPost, "/projects/"+projectID+"/integrations/"+integrationID+"/variable-definitions", nil, body, false)
	if err != nil {
		return VariableDefinition{}, err
	}
	resp, err := c.http.Do(ctx, req)
	if err != nil {
		return VariableDefinition{}, err
	}
	var out VariableDefinition
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return VariableDefinition{}, fmt.Errorf("integration: decode variable definition: %w", err)
	}
	return out, nil
}

// EnsureIntegrationVariableDefinition creates an integration-owned variable
// definition, treating an already-exists conflict (HTTP 409) as success. It is
// idempotent, so it is safe to call repeatedly (for example on every install or
// process start) to guarantee a definition exists before upserting subjects by
// its key. It does not update an existing definition.
func (c *CRMClient) EnsureIntegrationVariableDefinition(ctx context.Context, projectID, integrationID string, p CreateVariableDefinitionParams) error {
	if _, err := c.CreateIntegrationVariableDefinition(ctx, projectID, integrationID, p); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict {
			return nil
		}
		return err
	}
	return nil
}

func (c *CRMClient) bearerRequest(method, path string, query map[string]string, body []byte, idempotent bool) (httpclient.Request, error) {
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

func fieldsToWire(fields []Field) ([]fieldWire, error) {
	if len(fields) == 0 {
		return nil, nil
	}
	out := make([]fieldWire, 0, len(fields))
	for i, f := range fields {
		raw, err := json.Marshal(f.Value)
		if err != nil {
			return nil, fmt.Errorf("integration: marshal field[%d] %q: %w", i, f.Field, err)
		}
		out = append(out, fieldWire{Field: f.Field, Value: raw})
	}
	return out, nil
}
