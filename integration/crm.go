package integration

import (
	"context"
	"encoding/json"
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
	Find       []fieldWire `json:"find"`
	Create     []fieldWire `json:"create,omitempty"`
	Update     []fieldWire `json:"update,omitempty"`
	OnMultiple string      `json:"onMultiple,omitempty"`
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

	body, err := json.Marshal(upsertSubjectBody{Find: find, Create: create, Update: update, OnMultiple: p.OnMultiple})
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
