package integration

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
)

type VariableValuesLookupParams struct {
	Query  string   `json:"query,omitempty"`
	Cursor string   `json:"cursor,omitempty"`
	Limit  int      `json:"limit,omitempty"`
	Values []string `json:"values,omitempty"`
}

func (c *CRMClient) ListVariableValueSources(ctx context.Context, projectID, integrationID string) (map[string]VariableValueSource, error) {
	if c.oauth == nil || projectID != c.oauth.projectID || !integrationOAuthUUID(projectID) || !integrationOAuthUUID(integrationID) {
		return nil, integrationoauth.ErrRequest
	}
	var out struct {
		Sources map[string]VariableValueSource `json:"sources"`
	}
	err := callResourceJSON(ctx, c.oauth.valueSources, c.oauth.backendURL, http.MethodGet, "/integrations/projects/"+projectID+"/variable-values/"+integrationID, nil, &out, true, 200)
	if err == nil && out.Sources == nil {
		return nil, errCRMOAuthResponse
	}
	return out.Sources, err
}

// LookupVariableValues asks the platform to resolve the declared remote source.
// The token is sent only to the configured backend, never to a catalog URL.
func (c *CRMClient) LookupVariableValues(ctx context.Context, projectID, integrationID, key string, input VariableValuesLookupParams) (VariableValuesResponse, error) {
	if c.oauth == nil || projectID != c.oauth.projectID || !integrationOAuthUUID(projectID) || !integrationOAuthUUID(integrationID) || len(key) == 0 || len(key) > 128 {
		return VariableValuesResponse{}, integrationoauth.ErrRequest
	}
	for _, r := range key {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return VariableValuesResponse{}, integrationoauth.ErrRequest
		}
	}
	var out VariableValuesResponse
	err := callResourceJSON(ctx, c.oauth.valueSources, c.oauth.backendURL, http.MethodPost, "/integrations/projects/"+projectID+"/variable-values/"+integrationID+"/"+key, input, &out, true, 200)
	if err == nil && out.Items == nil {
		return VariableValuesResponse{}, errCRMOAuthResponse
	}
	return out, err
}

// GetSubjects returns existing project subjects, at most 200 per call. Missing
// IDs are omitted; the result is not promised in input order.
func (c *CRMClient) GetSubjects(ctx context.Context, projectID string, ids []string) ([]Subject, error) {
	if !integrationOAuthUUID(projectID) || len(ids) > 200 {
		return nil, integrationoauth.ErrRequest
	}
	if len(ids) == 0 {
		return []Subject{}, nil
	}
	for _, id := range ids {
		if !integrationOAuthUUID(id) {
			return nil, integrationoauth.ErrRequest
		}
	}
	input := struct {
		IDs []string `json:"ids"`
	}{ids}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	req, err := c.bearerRequest(http.MethodPost, "/projects/"+projectID+"/subjects/batch", nil, body, true)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, req)
	if err != nil {
		return nil, err
	}
	var out struct {
		Items []Subject `json:"items"`
	}
	if json.Unmarshal(resp.Body, &out) != nil || out.Items == nil {
		return nil, errCRMOAuthResponse
	}
	return out.Items, nil
}
