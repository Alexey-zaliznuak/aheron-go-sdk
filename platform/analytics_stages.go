package platform

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

// Stages are mutable, explicitly assigned states. They are not sticky A/B variants.
type AnalyticsStageDefinition struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Archived bool   `json:"archived"`
}
type AnalyticsStageSetInput struct {
	Name     string                     `json:"name"`
	Stages   []AnalyticsStageDefinition `json:"stages"`
	Archived bool                       `json:"archived"`
}

// AnalyticsStageSetSource is server-owned attribution. Bound stage sets are
// changed through their source contract; their graph and layout remain readable.
type AnalyticsStageSetSource struct {
	IntegrationID string `json:"integrationId"`
	CampaignID    string `json:"campaignId"`
}
type AnalyticsStageSet struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
	AnalyticsStageSetInput
	CreatedAt time.Time                `json:"createdAt"`
	UpdatedAt time.Time                `json:"updatedAt"`
	Source    *AnalyticsStageSetSource `json:"source,omitempty"`
	ReadOnly  bool                     `json:"readOnly"`
}
type AnalyticsStageSetPage struct {
	Items      []AnalyticsStageSet `json:"items"`
	NextCursor string              `json:"nextCursor,omitempty"`
}
type AnalyticsSaveStageSet struct {
	OperationID      string                 `json:"operationId"`
	ExpectedRevision string                 `json:"expectedRevision"`
	StageSet         AnalyticsStageSetInput `json:"stageSet"`
}
type AnalyticsStageAssignment struct {
	StageSetID string `json:"stageSetId"`
	StageID    string `json:"stageId"`
}
type AnalyticsAssignStage struct {
	OperationID string `json:"operationId"`
	SubjectID   string `json:"subjectId"`
	StageID     string `json:"stageId"`
}
type AnalyticsStageReceipt struct {
	GraphChanged    bool      `json:"graphChanged,omitempty"`
	Late            bool      `json:"late,omitempty"`
	OperationID     string    `json:"operationId"`
	StageSetID      string    `json:"stageSetId"`
	SubjectID       string    `json:"subjectId"`
	StageID         string    `json:"stageId"`
	PreviousStageID string    `json:"previousStageId,omitempty"`
	Changed         bool      `json:"changed"`
	Sequence        string    `json:"sequence"`
	AcceptedAt      time.Time `json:"acceptedAt"`
}
type AnalyticsStagePosition struct {
	StageID string  `json:"stageId"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
}
type AnalyticsStageView struct {
	Revision  string                   `json:"revision"`
	Positions []AnalyticsStagePosition `json:"positions"`
	UpdatedAt time.Time                `json:"updatedAt"`
}
type AnalyticsSaveStageView struct {
	OperationID      string                   `json:"operationId"`
	ExpectedRevision string                   `json:"expectedRevision"`
	Positions        []AnalyticsStagePosition `json:"positions"`
}
type AnalyticsStageGraphNode struct {
	StageID         string `json:"stageId"`
	UniqueSubjects  string `json:"uniqueSubjects"`
	Visits          string `json:"visits"`
	CurrentSubjects string `json:"currentSubjects"`
}
type AnalyticsStageGraphEdge struct {
	FromStageID    string `json:"fromStageId"`
	ToStageID      string `json:"toStageId"`
	UniqueSubjects string `json:"uniqueSubjects"`
}
type AnalyticsStageGraph struct {
	StageSetID       string                    `json:"stageSetId"`
	ParticipantCount string                    `json:"participantCount"`
	Nodes            []AnalyticsStageGraphNode `json:"nodes"`
	Edges            []AnalyticsStageGraphEdge `json:"edges"`
	ObservedAt       time.Time                 `json:"observedAt"`
	Consistency      string                    `json:"consistency"`
	Completeness     string                    `json:"completeness"`
}

func analyticsRevision(v string, positive bool) bool {
	n, err := strconv.ParseUint(v, 10, 64)
	return err == nil && analyticsDecimal.MatchString(v) && (!positive || n > 0)
}
func analyticsStageSetValid(v AnalyticsStageSet) bool {
	if !analyticsIDs(v.ID) || !analyticsRevision(v.Revision, true) || v.Stages == nil || v.CreatedAt.IsZero() || v.UpdatedAt.IsZero() {
		return false
	}
	for _, s := range v.Stages {
		if !analyticsIDs(s.ID) {
			return false
		}
	}
	return true
}
func analyticsStagePath(project, experiment, id string) (string, error) {
	p, err := analyticsPath(project, experiment, id)
	return p + "/stage-sets/" + id, err
}
func (a *AnalyticsClient) ListStageSets(ctx context.Context, project, experiment string, archived bool, o AnalyticsListOptions) (AnalyticsStageSetPage, error) {
	var out AnalyticsStageSetPage
	p, err := analyticsPath(project, experiment)
	if err != nil {
		return out, err
	}
	q, err := analyticsPage(o)
	if err != nil {
		return out, err
	}
	q.Set("archived", strconv.FormatBool(archived))
	err = a.client.requestJSON(ctx, "list analytics stage sets", http.MethodGet, p+"/stage-sets", q, nil, &out)
	if err == nil {
		if out.Items == nil || out.NextCursor != "" && !analyticsIDs(out.NextCursor) {
			return out, ErrResponse
		}
		for _, v := range out.Items {
			if !analyticsStageSetValid(v) {
				return out, ErrResponse
			}
		}
	}
	return out, err
}
func (a *AnalyticsClient) GetStageSet(ctx context.Context, project, experiment, id string) (AnalyticsStageSet, error) {
	var out AnalyticsStageSet
	p, err := analyticsStagePath(project, experiment, id)
	if err != nil {
		return out, err
	}
	err = a.client.get(ctx, "get analytics stage set", p, &out)
	if err == nil && (out.ID != id || !analyticsStageSetValid(out)) {
		err = ErrResponse
	}
	return out, err
}
func (a *AnalyticsClient) SaveStageSet(ctx context.Context, project, experiment, id string, in AnalyticsSaveStageSet) (AnalyticsStageSet, error) {
	var out AnalyticsStageSet
	p, err := analyticsStagePath(project, experiment, id)
	if err != nil {
		return out, err
	}
	if !analyticsIDs(in.OperationID) || !analyticsRevision(in.ExpectedRevision, false) {
		return out, ErrInvalidInput
	}
	err = a.client.requestJSON(ctx, "save analytics stage set", http.MethodPut, p, nil, in, &out)
	if err == nil && (out.ID != id || !analyticsStageSetValid(out)) {
		err = ErrResponse
	}
	return out, err
}
func (a *AnalyticsClient) AssignStage(ctx context.Context, project, experiment, id string, in AnalyticsAssignStage) (AnalyticsStageReceipt, error) {
	var out AnalyticsStageReceipt
	p, err := analyticsStagePath(project, experiment, id)
	if err != nil {
		return out, err
	}
	if !analyticsIDs(in.OperationID, in.SubjectID, in.StageID) {
		return out, ErrInvalidInput
	}
	err = a.client.requestJSON(ctx, "assign analytics stage", http.MethodPost, p+"/assign", nil, in, &out)
	if err == nil && (out.OperationID != in.OperationID || out.StageSetID != id || out.StageID != in.StageID || out.SubjectID != in.SubjectID || out.PreviousStageID != "" && !analyticsIDs(out.PreviousStageID) || !analyticsRevision(out.Sequence, false) || out.AcceptedAt.IsZero()) {
		err = ErrResponse
	}
	return out, err
}
func (a *AnalyticsClient) StageGraph(ctx context.Context, project, experiment, id string) (AnalyticsStageGraph, error) {
	var out AnalyticsStageGraph
	p, err := analyticsStagePath(project, experiment, id)
	if err != nil {
		return out, err
	}
	err = a.client.get(ctx, "get analytics stage graph", p+"/graph", &out)
	if err == nil {
		if out.StageSetID != id || !analyticsDecimal.MatchString(out.ParticipantCount) || out.Nodes == nil || out.Edges == nil || out.ObservedAt.IsZero() || out.Consistency == "" || out.Completeness == "" {
			return out, ErrResponse
		}
		for _, n := range out.Nodes {
			if !analyticsIDs(n.StageID) || !analyticsDecimal.MatchString(n.UniqueSubjects) || !analyticsDecimal.MatchString(n.Visits) || !analyticsDecimal.MatchString(n.CurrentSubjects) {
				return out, ErrResponse
			}
		}
		for _, e := range out.Edges {
			if !analyticsIDs(e.FromStageID, e.ToStageID) || !analyticsDecimal.MatchString(e.UniqueSubjects) {
				return out, ErrResponse
			}
		}
	}
	return out, err
}
func analyticsStageViewValid(v AnalyticsStageView) bool {
	if !analyticsRevision(v.Revision, false) || v.Positions == nil {
		return false
	}
	for _, p := range v.Positions {
		if !analyticsIDs(p.StageID) {
			return false
		}
	}
	return v.Revision == "0" || !v.UpdatedAt.IsZero()
}
func (a *AnalyticsClient) GetStageView(ctx context.Context, project, experiment, id string) (AnalyticsStageView, error) {
	var out AnalyticsStageView
	p, err := analyticsStagePath(project, experiment, id)
	if err != nil {
		return out, err
	}
	err = a.client.get(ctx, "get analytics stage view", p+"/view", &out)
	if err == nil && !analyticsStageViewValid(out) {
		err = ErrResponse
	}
	return out, err
}
func (a *AnalyticsClient) SaveStageView(ctx context.Context, project, experiment, id string, in AnalyticsSaveStageView) (AnalyticsStageView, error) {
	var out AnalyticsStageView
	p, err := analyticsStagePath(project, experiment, id)
	if err != nil {
		return out, err
	}
	if !analyticsIDs(in.OperationID) || !analyticsRevision(in.ExpectedRevision, false) {
		return out, ErrInvalidInput
	}
	err = a.client.requestJSON(ctx, "save analytics stage view", http.MethodPut, p+"/view", nil, in, &out)
	if err == nil && (!analyticsStageViewValid(out) || out.Revision == "0") {
		err = ErrResponse
	}
	return out, err
}
