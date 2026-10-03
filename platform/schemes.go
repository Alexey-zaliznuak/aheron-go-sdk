package platform

import (
	"context"
	"strings"
	"time"
)

type Scheme struct {
	ExecutionStatus string    `json:"executionStatus"`
	ID              string    `json:"id"`
	ProjectID       string    `json:"projectId"`
	Name            string    `json:"name"`
	Description     *string   `json:"description,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
}

type SchemesClient struct{ client *Client }

func (s *SchemesClient) List(ctx context.Context, projectID string) ([]Scheme, error) {
	if !resourceID.MatchString(projectID) {
		return nil, ErrInvalidInput
	}
	var result []Scheme
	if err := s.client.get(ctx, "list schemes", "/projects/"+projectID+"/schemes", &result); err != nil {
		return nil, err
	}
	for _, scheme := range result {
		if !resourceID.MatchString(scheme.ID) || !strings.EqualFold(scheme.ProjectID, projectID) {
			return nil, ErrResponse
		}
	}
	return result, nil
}

func (s *SchemesClient) Get(ctx context.Context, projectID, schemeID string) (Scheme, error) {
	if !resourceID.MatchString(projectID) || !resourceID.MatchString(schemeID) {
		return Scheme{}, ErrInvalidInput
	}
	var result Scheme
	if err := s.client.get(ctx, "get scheme", "/projects/"+projectID+"/schemes/"+schemeID, &result); err != nil {
		return Scheme{}, err
	}
	if !strings.EqualFold(result.ID, schemeID) || !strings.EqualFold(result.ProjectID, projectID) {
		return Scheme{}, ErrResponse
	}
	return result, nil
}
