package platform

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// SchemeGraph is one revision of the saved draft graph, not execution history.
type SchemeGraph struct {
	SchemeID string   `json:"schemeId"`
	Revision int64    `json:"revision"`
	Steps    []Step   `json:"steps"`
	Branches []Branch `json:"branches"`
	Edges    []Edge   `json:"edges"`
}

type CopySetupRequirement struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

type Step struct {
	ID                  string                 `json:"id"`
	SchemeID            string                 `json:"schemeId"`
	Type                string                 `json:"type"`
	Name                *string                `json:"name,omitempty"`
	Settings            json.RawMessage        `json:"settings"`
	Position            json.RawMessage        `json:"position"`
	IntegrationID       *string                `json:"integrationId,omitempty"`
	IntegrationVersion  *int                   `json:"integrationVersion,omitempty"`
	IntegrationBlockKey *string                `json:"integrationBlockKey,omitempty"`
	CopySetup           []CopySetupRequirement `json:"copySetup,omitempty"`
	CreatedAt           time.Time              `json:"createdAt"`
}

type Branch struct {
	ID        string          `json:"id"`
	SchemeID  string          `json:"schemeId"`
	Key       string          `json:"key,omitempty"`
	Role      string          `json:"role,omitempty"`
	Name      *string         `json:"name,omitempty"`
	Settings  json.RawMessage `json:"settings"`
	CreatedAt time.Time       `json:"createdAt"`
}

type Edge struct {
	ID         string          `json:"id"`
	SchemeID   string          `json:"schemeId"`
	FromStepID string          `json:"fromStepId"`
	ToStepID   string          `json:"toStepId"`
	OutputKey  string          `json:"outputKey"`
	InputKey   string          `json:"inputKey"`
	BranchID   string          `json:"branchId"`
	Settings   json.RawMessage `json:"settings"`
	CreatedAt  time.Time       `json:"createdAt"`
}

func (s *SchemesClient) GetGraph(ctx context.Context, projectID, schemeID string) (SchemeGraph, error) {
	if !resourceID.MatchString(projectID) || !resourceID.MatchString(schemeID) {
		return SchemeGraph{}, ErrInvalidInput
	}
	var graph SchemeGraph
	if err := s.client.get(ctx, "get scheme graph", "/projects/"+projectID+"/schemes/"+schemeID+"/graph", &graph); err != nil {
		return SchemeGraph{}, err
	}
	if !strings.EqualFold(graph.SchemeID, schemeID) || graph.Revision < 0 || graph.Steps == nil || graph.Branches == nil || graph.Edges == nil {
		return SchemeGraph{}, ErrResponse
	}
	steps, branches, edges := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, step := range graph.Steps {
		id := strings.ToLower(step.ID)
		if !resourceID.MatchString(step.ID) || !strings.EqualFold(step.SchemeID, schemeID) || steps[id] {
			return SchemeGraph{}, ErrResponse
		}
		steps[id] = true
	}
	for _, branch := range graph.Branches {
		id := strings.ToLower(branch.ID)
		if !resourceID.MatchString(branch.ID) || !strings.EqualFold(branch.SchemeID, schemeID) || branches[id] {
			return SchemeGraph{}, ErrResponse
		}
		branches[id] = true
	}
	for _, edge := range graph.Edges {
		id := strings.ToLower(edge.ID)
		if !resourceID.MatchString(edge.ID) || !strings.EqualFold(edge.SchemeID, schemeID) || edges[id] ||
			!steps[strings.ToLower(edge.FromStepID)] || !steps[strings.ToLower(edge.ToStepID)] || !branches[strings.ToLower(edge.BranchID)] {
			return SchemeGraph{}, ErrResponse
		}
		edges[id] = true
	}
	return graph, nil
}

func (s *SchemesClient) GetStep(ctx context.Context, projectID, schemeID, stepID string) (Step, error) {
	if !resourceID.MatchString(projectID) || !resourceID.MatchString(schemeID) || !resourceID.MatchString(stepID) {
		return Step{}, ErrInvalidInput
	}
	var step Step
	if err := s.client.get(ctx, "get scheme step", "/projects/"+projectID+"/schemes/"+schemeID+"/steps/"+stepID, &step); err != nil {
		return Step{}, err
	}
	if !strings.EqualFold(step.ID, stepID) || !strings.EqualFold(step.SchemeID, schemeID) {
		return Step{}, ErrResponse
	}
	return step, nil
}
