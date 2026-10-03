package platform

import (
	"context"
	"net/http"
	"strings"
)

type CreateSchemeParams struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}
type UpdateSchemeParams struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

func (s *SchemesClient) Create(ctx context.Context, projectID string, p CreateSchemeParams) (Scheme, error) {
	if !resourceID.MatchString(projectID) || strings.TrimSpace(p.Name) == "" {
		return Scheme{}, ErrInvalidInput
	}
	var out Scheme
	err := s.client.requestJSON(ctx, "create scheme", http.MethodPost, "/projects/"+projectID+"/schemes", nil, p, &out)
	if err == nil && (!strings.EqualFold(out.ProjectID, projectID) || !resourceID.MatchString(out.ID)) {
		err = ErrResponse
	}
	return out, err
}
func (s *SchemesClient) Update(ctx context.Context, projectID, schemeID string, p UpdateSchemeParams) (Scheme, error) {
	if !resourceID.MatchString(projectID) || !resourceID.MatchString(schemeID) || (p.Name == nil && p.Description == nil) || (p.Name != nil && strings.TrimSpace(*p.Name) == "") {
		return Scheme{}, ErrInvalidInput
	}
	var out Scheme
	err := s.client.requestJSON(ctx, "update scheme", http.MethodPatch, "/projects/"+projectID+"/schemes/"+schemeID, nil, p, &out)
	if err == nil && (!strings.EqualFold(out.ProjectID, projectID) || !strings.EqualFold(out.ID, schemeID)) {
		err = ErrResponse
	}
	return out, err
}
func (s *SchemesClient) Delete(ctx context.Context, projectID, schemeID string) error {
	if !resourceID.MatchString(projectID) || !resourceID.MatchString(schemeID) {
		return ErrInvalidInput
	}
	return s.client.requestJSON(ctx, "delete scheme", http.MethodDelete, "/projects/"+projectID+"/schemes/"+schemeID, nil, nil, nil)
}
