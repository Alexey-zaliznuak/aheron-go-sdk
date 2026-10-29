package integration

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/platform"
)

type AnalyticsOAuthConfig struct {
	Provider       *integrationoauth.Provider
	ProjectID      string
	InstallationID string
	HTTPClient     *http.Client
}

// AnalyticsClient accesses campaign analytics with current installation OAuth.
// Stored bindings belong to the integration, independently of reinstallation.
// The project template catalog is read-only apart from explicit default creation.
type AnalyticsClient struct {
	projectID, installationID, baseURL string
	read, write                        *integrationoauth.Client
}

func newAnalyticsClient(baseURL string, cfg *AnalyticsOAuthConfig) (*AnalyticsClient, error) {
	c := &AnalyticsClient{baseURL: strings.TrimRight(baseURL, "/")}
	if cfg == nil {
		return c, nil
	}
	c.projectID, c.installationID = cfg.ProjectID, cfg.InstallationID
	for _, op := range []struct {
		dest  **integrationoauth.Client
		scope string
	}{{&c.read, "analytics.sources.read"}, {&c.write, "analytics.sources.write"}} {
		v, e := integrationoauth.NewClient(integrationoauth.ClientConfig{Provider: cfg.Provider, BaseURL: baseURL, HTTPClient: cfg.HTTPClient, TokenRequest: integrationoauth.Request{ProjectID: cfg.ProjectID, InstallationID: cfg.InstallationID, Audience: "analytics", Scopes: []string{op.scope}}})
		if e != nil {
			return nil, e
		}
		*op.dest = v
	}
	return c, nil
}
func (c *AnalyticsClient) projectPath(project string) (string, error) {
	if c == nil || c.read == nil || project != c.projectID || !integrationOAuthUUID(project) {
		return "", integrationoauth.ErrRequest
	}
	return "/projects/" + project, nil
}
func (c *AnalyticsClient) bindingPath(project, campaign string) (string, error) {
	p, e := c.projectPath(project)
	if e != nil || !integrationOAuthUUID(campaign) {
		return "", integrationoauth.ErrRequest
	}
	return p + "/sources/messengers/installations/" + c.installationID + "/campaigns/" + campaign, nil
}
func revisionOK(v string, zero bool) bool {
	n, e := strconv.ParseUint(v, 10, 64)
	return e == nil && strconv.FormatUint(n, 10) == v && (zero || n > 0)
}

func analyticsSourceStageKey(v string) bool {
	return utf8.ValidString(v) && len(v) > 0 && len(v) <= 128 && strings.TrimSpace(v) == v && strings.IndexFunc(v, unicode.IsControl) < 0
}
func (c *AnalyticsClient) ListStageTemplates(ctx context.Context, project string, archived bool, o platform.AnalyticsListOptions) (platform.AnalyticsStageTemplatePage, error) {
	var out platform.AnalyticsStageTemplatePage
	p, e := c.projectPath(project)
	if e != nil {
		return out, e
	}
	if o.Limit < 0 || o.Limit > 100 || o.Cursor != "" && !integrationOAuthUUID(o.Cursor) {
		return out, integrationoauth.ErrRequest
	}
	q := url.Values{}
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.Cursor != "" {
		q.Set("cursor", o.Cursor)
	}
	if archived {
		q.Set("archived", "true")
	}
	e = callResourceJSON(ctx, c.read, c.baseURL, http.MethodGet, p+"/sources/messengers/stage-templates?"+q.Encode(), nil, &out, true, 200)
	if e == nil && out.Items == nil {
		e = errCampaignResponse
	}
	return out, e
}
func (c *AnalyticsClient) GetStageTemplate(ctx context.Context, project, id string) (platform.AnalyticsStageTemplate, error) {
	return c.getTemplate(ctx, project, id, "")
}
func (c *AnalyticsClient) GetStageTemplateVersion(ctx context.Context, project, id, revision string) (platform.AnalyticsStageTemplate, error) {
	if !revisionOK(revision, false) {
		return platform.AnalyticsStageTemplate{}, integrationoauth.ErrRequest
	}
	return c.getTemplate(ctx, project, id, revision)
}
func (c *AnalyticsClient) getTemplate(ctx context.Context, project, id, revision string) (platform.AnalyticsStageTemplate, error) {
	var out platform.AnalyticsStageTemplate
	p, e := c.projectPath(project)
	if e != nil || !integrationOAuthUUID(id) {
		return out, integrationoauth.ErrRequest
	}
	p += "/sources/messengers/stage-templates/" + id
	if revision != "" {
		p += "/versions/" + revision
	}
	e = callResourceJSON(ctx, c.read, c.baseURL, http.MethodGet, p, nil, &out, true, 200)
	if e == nil && (out.TemplateID != id || !revisionOK(out.Revision, false) || revision != "" && out.Revision != revision) {
		e = errCampaignResponse
	}
	return out, e
}
func (c *AnalyticsClient) EnsureDefaultStageTemplate(ctx context.Context, project, operationID string) (platform.AnalyticsStageTemplate, error) {
	var out platform.AnalyticsStageTemplate
	p, e := c.projectPath(project)
	if e != nil || !integrationOAuthUUID(operationID) {
		return out, integrationoauth.ErrRequest
	}
	e = callResourceJSON(ctx, c.write, c.baseURL, http.MethodPost, p+"/sources/messengers/stage-templates/default", struct {
		OperationID string `json:"operationId"`
	}{operationID}, &out, true, 200)
	if e == nil && (!integrationOAuthUUID(out.TemplateID) || !revisionOK(out.Revision, false) || len(out.Stages) == 0) {
		e = errCampaignResponse
	}
	return out, e
}
func (c *AnalyticsClient) GetCampaignStageBinding(ctx context.Context, project, campaign string) (platform.AnalyticsCampaignStageBinding, error) {
	var out platform.AnalyticsCampaignStageBinding
	p, e := c.bindingPath(project, campaign)
	if e != nil {
		return out, e
	}
	e = callResourceJSON(ctx, c.read, c.baseURL, http.MethodGet, p+"/binding", nil, &out, true, 200)
	return c.checkBinding(out, project, campaign, e)
}
func (c *AnalyticsClient) CreateCampaignStageBinding(ctx context.Context, project, campaign string, in platform.AnalyticsCreateCampaignStageBinding) (platform.AnalyticsCampaignStageBinding, error) {
	var out platform.AnalyticsCampaignStageBinding
	p, e := c.bindingPath(project, campaign)
	if e != nil || !integrationOAuthUUID(in.OperationID) || in.ExpectedRevision != "0" {
		return out, integrationoauth.ErrRequest
	}
	if in.UseDefaultTemplate {
		if in.TemplateID != "" || in.TemplateRevision != "" {
			return out, integrationoauth.ErrRequest
		}
	} else if !integrationOAuthUUID(in.TemplateID) || !revisionOK(in.TemplateRevision, false) {
		return out, integrationoauth.ErrRequest
	}
	e = callResourceJSON(ctx, c.write, c.baseURL, http.MethodPut, p+"/binding", in, &out, true, 200)
	return c.checkBinding(out, project, campaign, e)
}
func (c *AnalyticsClient) checkBinding(out platform.AnalyticsCampaignStageBinding, p, id string, e error) (platform.AnalyticsCampaignStageBinding, error) {
	if e == nil && (out.ProjectID != p || out.CampaignID != id || !integrationOAuthUUID(out.IntegrationID) || !integrationOAuthUUID(out.ExperimentID) || !integrationOAuthUUID(out.StageSetID) || !integrationOAuthUUID(out.TemplateID) || !revisionOK(out.SourceGeneration, false) || !revisionOK(out.TemplateRevision, false) || !revisionOK(out.Revision, false) || out.Stages == nil) {
		e = errCampaignResponse
	}
	if e == nil {
		seen := make(map[string]bool, len(out.Stages))
		for _, stage := range out.Stages {
			if !analyticsSourceStageKey(stage.Key) || !integrationOAuthUUID(stage.StageID) || seen[stage.Key] {
				return out, errCampaignResponse
			}
			seen[stage.Key] = true
		}
	}
	return out, e
}
func (c *AnalyticsClient) AcceptCampaignStageEvent(ctx context.Context, project, campaign string, in platform.AnalyticsCampaignStageEvent) (platform.AnalyticsCampaignStageEventResult, error) {
	var out platform.AnalyticsCampaignStageEventResult
	p, e := c.bindingPath(project, campaign)
	if e != nil || !integrationOAuthUUID(in.EventID) || !integrationOAuthUUID(in.SubjectID) || !integrationOAuthUUID(in.DeliveryID) || !integrationOAuthUUID(in.TemplateID) || !revisionOK(in.TemplateRevision, false) || !revisionOK(in.SourceGeneration, false) || in.OccurredAt.IsZero() || (in.StageKey == "") == (in.LegacyStatusID == "") {
		return out, integrationoauth.ErrRequest
	}
	if in.StageKey != "" && !analyticsSourceStageKey(in.StageKey) || in.LegacyStatusID != "" && !integrationOAuthUUID(in.LegacyStatusID) {
		return out, integrationoauth.ErrRequest
	}
	e = callResourceJSON(ctx, c.write, c.baseURL, http.MethodPost, p+"/events", in, &out, true, 200)
	if e == nil && (out.EventID != in.EventID || out.Stale && out.Changed) {
		e = errCampaignResponse
	}
	return out, e
}
