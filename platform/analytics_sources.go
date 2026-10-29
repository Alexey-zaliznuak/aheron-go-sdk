package platform

import (
	"context"
	"net/http"
	"time"
)

type AnalyticsTemplateStage struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Archived bool   `json:"archived"`
}
type AnalyticsStageTemplate struct {
	TemplateID     string                   `json:"templateId"`
	Name           string                   `json:"name"`
	Revision       string                   `json:"revision"`
	ParentRevision string                   `json:"parentRevision"`
	Stages         []AnalyticsTemplateStage `json:"stages"`
	Archived       bool                     `json:"archived"`
	CreatedAt      time.Time                `json:"createdAt"`
	UpdatedAt      time.Time                `json:"updatedAt"`
}
type AnalyticsSaveStageTemplate struct {
	OperationID      string                   `json:"operationId"`
	ExpectedRevision string                   `json:"expectedRevision"`
	Name             string                   `json:"name"`
	Stages           []AnalyticsTemplateStage `json:"stages"`
	Archived         bool                     `json:"archived"`
}
type AnalyticsStageTemplatePage struct {
	Items      []AnalyticsStageTemplate `json:"items"`
	NextCursor string                   `json:"nextCursor,omitempty"`
}
type AnalyticsBoundStage struct {
	Key     string `json:"key"`
	StageID string `json:"stageId"`
}
type AnalyticsCampaignStageBinding struct {
	ProjectID        string                `json:"projectId"`
	IntegrationID    string                `json:"integrationId"`
	CampaignID       string                `json:"campaignId"`
	ExperimentID     string                `json:"experimentId"`
	TemplateID       string                `json:"templateId"`
	TemplateRevision string                `json:"templateRevision"`
	StageSetID       string                `json:"stageSetId"`
	Stages           []AnalyticsBoundStage `json:"stages"`
	Revision         string                `json:"revision"`
	SourceGeneration string                `json:"sourceGeneration"`
	MigrationState   string                `json:"migrationState"`
	CreatedAt        time.Time             `json:"createdAt"`
}
type AnalyticsCreateCampaignStageBinding struct {
	OperationID        string `json:"operationId"`
	ExpectedRevision   string `json:"expectedRevision"`
	UseDefaultTemplate bool   `json:"useDefaultTemplate,omitempty"`
	TemplateID         string `json:"templateId,omitempty"`
	TemplateRevision   string `json:"templateRevision,omitempty"`
}
type AnalyticsCampaignStageEvent struct {
	EventID          string    `json:"eventId"`
	SubjectID        string    `json:"subjectId"`
	OccurredAt       time.Time `json:"occurredAt"`
	SourceGeneration string    `json:"sourceGeneration"`
	TemplateID       string    `json:"templateId"`
	TemplateRevision string    `json:"templateRevision"`
	StageKey         string    `json:"stageKey,omitempty"`
	LegacyStatusID   string    `json:"legacyStatusId,omitempty"`
	DeliveryID       string    `json:"deliveryId"`
}
type AnalyticsCampaignStageEventResult struct {
	EventID  string                 `json:"eventId"`
	Accepted bool                   `json:"accepted"`
	Changed  bool                   `json:"changed"`
	Stale    bool                   `json:"stale"`
	Receipt  *AnalyticsStageReceipt `json:"receipt,omitempty"`
}

func analyticsTemplateValid(v AnalyticsStageTemplate) bool {
	if !analyticsIDs(v.TemplateID) || !analyticsRevision(v.Revision, true) || !analyticsRevision(v.ParentRevision, false) || len(v.Stages) == 0 || len(v.Stages) > 32 || v.CreatedAt.IsZero() || v.UpdatedAt.IsZero() {
		return false
	}
	keys := map[string]bool{}
	for _, s := range v.Stages {
		if s.Key == "" || s.Name == "" || keys[s.Key] {
			return false
		}
		keys[s.Key] = true
	}
	return true
}
func (a *AnalyticsClient) ListStageTemplates(ctx context.Context, project string, archived bool, options AnalyticsListOptions) (AnalyticsStageTemplatePage, error) {
	var out AnalyticsStageTemplatePage
	p, e := analyticsPath(project)
	if e != nil {
		return out, e
	}
	q, e := analyticsPage(options)
	if e != nil {
		return out, e
	}
	if archived {
		q.Set("archived", "true")
	}
	e = a.client.requestJSON(ctx, "list analytics stage templates", http.MethodGet, p+"/stage-templates", q, nil, &out)
	if e == nil {
		if out.Items == nil || out.NextCursor != "" && !analyticsIDs(out.NextCursor) {
			return out, ErrResponse
		}
		for _, t := range out.Items {
			if !analyticsTemplateValid(t) {
				return out, ErrResponse
			}
		}
	}
	return out, e
}
func (a *AnalyticsClient) GetStageTemplate(ctx context.Context, project, id string) (AnalyticsStageTemplate, error) {
	return a.getStageTemplate(ctx, project, id, "")
}
func (a *AnalyticsClient) GetStageTemplateVersion(ctx context.Context, project, id, revision string) (AnalyticsStageTemplate, error) {
	if !analyticsRevision(revision, true) {
		return AnalyticsStageTemplate{}, ErrInvalidInput
	}
	return a.getStageTemplate(ctx, project, id, revision)
}
func (a *AnalyticsClient) getStageTemplate(ctx context.Context, project, id, revision string) (AnalyticsStageTemplate, error) {
	var out AnalyticsStageTemplate
	p, e := analyticsPath(project)
	if e != nil || !analyticsIDs(id) {
		return out, ErrInvalidInput
	}
	p += "/stage-templates/" + id
	if revision != "" {
		p += "/versions/" + revision
	}
	e = a.client.get(ctx, "get analytics stage template", p, &out)
	if e == nil && (!analyticsTemplateValid(out) || out.TemplateID != id || revision != "" && out.Revision != revision) {
		e = ErrResponse
	}
	return out, e
}
func (a *AnalyticsClient) SaveStageTemplate(ctx context.Context, project, id string, in AnalyticsSaveStageTemplate) (AnalyticsStageTemplate, error) {
	var out AnalyticsStageTemplate
	p, e := analyticsPath(project)
	if e != nil || !analyticsIDs(id, in.OperationID) || !analyticsRevision(in.ExpectedRevision, false) {
		return out, ErrInvalidInput
	}
	e = a.client.requestJSON(ctx, "save analytics stage template", http.MethodPut, p+"/stage-templates/"+id, nil, in, &out)
	if e == nil && (!analyticsTemplateValid(out) || out.TemplateID != id) {
		e = ErrResponse
	}
	return out, e
}
