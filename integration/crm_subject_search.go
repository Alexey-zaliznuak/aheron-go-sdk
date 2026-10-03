package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// SubjectTagCondition tests whether a tag is assigned: Op is has or notHas.
type SubjectTagCondition struct {
	TagID string `json:"tagId"`
	Op    string `json:"op"`
}

// SubjectTagConditionGroup combines its conditions with AND.
type SubjectTagConditionGroup struct {
	Conditions []SubjectTagCondition `json:"conditions"`
}

// SubjectTagsFilter combines Groups with OR. AnyOf/NoneOf are the legacy
// representation accepted by CRM and must not be mixed with Groups.
type SubjectTagsFilter struct {
	Groups []SubjectTagConditionGroup `json:"groups,omitempty"`
	AnyOf  []string                   `json:"anyOf,omitempty"`
	NoneOf []string                   `json:"noneOf,omitempty"`
}

// SubjectVariableCondition addresses a definition by ID, including integration
// definitions. Op is eq, exists, notExists or contains. Eq and contains require
// Value; contains tests array membership, not a substring of a string.
// RawMessage preserves JSON null, false, zero and structured values.
type SubjectVariableCondition struct {
	DefinitionID string          `json:"definitionId"`
	Op           string          `json:"op"`
	Value        json.RawMessage `json:"value,omitempty"`
}

// SubjectFilter combines the tag expression and all variable conditions with
// AND. Inclusion is applied with OR after these conditions, then exclusion
// wins: matched = !excluded && (conditionsMatch || included).
type SubjectFilter struct {
	Tags              *SubjectTagsFilter         `json:"tags,omitempty"`
	Variables         []SubjectVariableCondition `json:"variables,omitempty"`
	IncludeSubjectIDs []string                   `json:"includeSubjectIds,omitempty"`
	ExcludeSubjectIDs []string                   `json:"excludeSubjectIds,omitempty"`
}

// SearchSubjectsRequest selects a page of project subjects. A nil Filter matches
// everyone. Mode is matched (default, return matches only) or all (return every
// subject with its Matched flag). Search is a case-insensitive display-name
// prefix. Cursor is an opaque token from the previous response. Limit defaults
// to 50 and is capped by CRM at 200.
type SearchSubjectsRequest struct {
	Filter *SubjectFilter `json:"filter"`
	Mode   string         `json:"mode,omitempty"`
	Search string         `json:"search,omitempty"`
	Cursor string         `json:"cursor,omitempty"`
	Limit  int            `json:"limit,omitempty"`
}

// SearchSubjectItem is a subject paired with the filter result.
type SearchSubjectItem struct {
	ID          string    `json:"id"`
	DisplayName *string   `json:"displayName,omitempty"`
	Description *string   `json:"description,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	Matched     bool      `json:"matched"`
}

// SearchSubjectsResponse is ordered by createdAt DESC, id DESC. A non-nil cursor
// is returned for a full page; the following page may still be empty.
type SearchSubjectsResponse struct {
	Items      []SearchSubjectItem `json:"items"`
	NextCursor *string             `json:"nextCursor"`
}

// SearchSubjects reads a page from POST /projects/{projectID}/subjects/search.
// OAuth installations require crm.read and remain bound to their project.
// This POST is read-only and safe to replay under the transport's retry policy.
func (c *CRMClient) SearchSubjects(ctx context.Context, projectID string, input SearchSubjectsRequest) (SearchSubjectsResponse, error) {
	if !integrationOAuthUUID(projectID) {
		return SearchSubjectsResponse{}, fmt.Errorf("integration: SearchSubjects requires a UUID projectID")
	}
	body, err := json.Marshal(input)
	if err != nil {
		return SearchSubjectsResponse{}, fmt.Errorf("integration: marshal subject search: %w", err)
	}
	req, err := c.bearerRequest(http.MethodPost, "/projects/"+projectID+"/subjects/search", nil, body, true)
	if err != nil {
		return SearchSubjectsResponse{}, err
	}
	resp, err := c.do(ctx, req)
	if err != nil {
		return SearchSubjectsResponse{}, err
	}
	var out SearchSubjectsResponse
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		return SearchSubjectsResponse{}, c.decodeError("subject search", err)
	}
	return out, nil
}
