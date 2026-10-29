package platform

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// AnalyticsClient uses the shared user transport under /api/analytics. It never
// retries writes, uses internal credentials, or substitutes a project API key.
type AnalyticsClient struct{ client *Client }

type AnalyticsDefinitionKind string

const (
	AnalyticsFolders    AnalyticsDefinitionKind = "experiments"
	AnalyticsMetrics    AnalyticsDefinitionKind = "metrics"
	AnalyticsAudiences  AnalyticsDefinitionKind = "audiences"
	AnalyticsPartitions AnalyticsDefinitionKind = "partitions"
)

type AnalyticsCapabilities struct {
	ContractRevision          string   `json:"contractRevision"`
	StageContractRevision     string   `json:"stageContractRevision,omitempty"`
	TemplateContractRevision  string   `json:"templateContractRevision,omitempty"`
	SourceContractRevision    string   `json:"sourceContractRevision,omitempty"`
	MaxStages                 int      `json:"maxStages,omitempty"`
	DashboardContractRevision string   `json:"dashboardContractRevision"`
	MaxDashboardPanels        int      `json:"maxDashboardPanels"`
	MetricTypes               []string `json:"metricTypes"`
	Calculations              []string `json:"calculations"`
	Intervals                 []string `json:"intervals"`
	Membership                string   `json:"membership"`
	Assignment                string   `json:"assignment"`
	EventTime                 string   `json:"eventTime"`
	IndividualStorage         string   `json:"individualStorage"`
	MaxAudiencesPerSubject    int      `json:"maxAudiencesPerSubject"`
	MaxPartitionsPerSubject   int      `json:"maxPartitionsPerSubject"`
	Consistency               string   `json:"consistency"`
	Completeness              string   `json:"completeness"`
}
type AnalyticsVariant struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type AnalyticsDefinitionInput struct {
	Name            string             `json:"name"`
	Type            string             `json:"type,omitempty"`
	Unit            string             `json:"unit,omitempty"`
	StorePerSubject bool               `json:"storePerSubject,omitempty"`
	Variants        []AnalyticsVariant `json:"variants,omitempty"`
}
type AnalyticsDefinition struct {
	ID string `json:"id"`
	AnalyticsDefinitionInput
	CreatedAt time.Time `json:"createdAt"`
}
type AnalyticsDefinitionPage struct {
	Items      []AnalyticsDefinition `json:"items"`
	NextCursor string                `json:"nextCursor,omitempty"`
}
type AnalyticsListOptions struct {
	Cursor string
	Limit  int
}
type AnalyticsAssignment struct {
	PartitionID string `json:"partitionId"`
	VariantID   string `json:"variantId"`
}
type AnalyticsSubject struct {
	Audiences   []string              `json:"audiences"`
	Assignments []AnalyticsAssignment `json:"assignments"`
	Sequence    uint64                `json:"sequence"`
}

// Values remain decimal strings, including values above JavaScript's safe integer range.
type AnalyticsIndividualValue struct {
	Value  string `json:"value"`
	Stored bool   `json:"stored"`
}
type AnalyticsAudienceSize struct {
	Value string `json:"value"`
}
type AnalyticsQuery struct {
	MetricID    string    `json:"metricId"`
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
	Interval    string    `json:"interval"`
	Calculation string    `json:"calculation"`
	PartitionID string    `json:"partitionId,omitempty"`
	AudienceID  string    `json:"audienceId,omitempty"`
}
type AnalyticsPoint struct {
	At     time.Time `json:"at"`
	Series string    `json:"series"`
	Value  string    `json:"value"`
}
type AnalyticsQueryResult struct {
	Points       []AnalyticsPoint `json:"points"`
	QueriedAt    time.Time        `json:"queriedAt"`
	Consistency  string           `json:"consistency"`
	Completeness string           `json:"completeness"`
}
type AnalyticsIncrement struct {
	MetricID string `json:"metricId"`
	Delta    int64  `json:"delta"`
}
type AnalyticsCommand struct {
	OperationID    string                     `json:"operationId"`
	SubjectID      string                     `json:"subjectId"`
	JoinAudiences  []string                   `json:"joinAudiences,omitempty"`
	AssignVariants []AnalyticsAssignment      `json:"assignVariants,omitempty"`
	Increments     []AnalyticsIncrement       `json:"increments,omitempty"`
	AssignStages   []AnalyticsStageAssignment `json:"assignStages,omitempty"`
}
type AnalyticsReceipt struct {
	OperationID      string                  `json:"operationId"`
	AcceptedAt       time.Time               `json:"acceptedAt"`
	EventIDs         []string                `json:"eventIds"`
	SubjectSequence  uint64                  `json:"subjectSequence"`
	StageAssignments []AnalyticsStageReceipt `json:"stageAssignments,omitempty"`
}
type AnalyticsDashboardPeriod struct {
	Kind     string `json:"kind"`
	Relative string `json:"relative,omitempty"`
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
}
type AnalyticsDashboardPanel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	MetricID    string `json:"metricId"`
	Calculation string `json:"calculation"`
	Interval    string `json:"interval"`
	View        string `json:"view"`
	AudienceID  string `json:"audienceId,omitempty"`
	PartitionID string `json:"partitionId,omitempty"`
}
type AnalyticsDashboardInput struct {
	Name     string                    `json:"name"`
	Period   AnalyticsDashboardPeriod  `json:"period"`
	Panels   []AnalyticsDashboardPanel `json:"panels"`
	Archived bool                      `json:"archived"`
}
type AnalyticsDashboard struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
	AnalyticsDashboardInput
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
type AnalyticsSaveDashboard struct {
	OperationID      string                  `json:"operationId"`
	ExpectedRevision string                  `json:"expectedRevision"`
	Dashboard        AnalyticsDashboardInput `json:"dashboard"`
}
type AnalyticsDashboardPage struct {
	Items      []AnalyticsDashboard `json:"items"`
	NextCursor string               `json:"nextCursor,omitempty"`
}

func analyticsIDs(ids ...string) bool {
	for _, id := range ids {
		v, err := uuid.Parse(id)
		if err != nil || v == uuid.Nil || v.String() != id {
			return false
		}
	}
	return true
}
func analyticsPath(project string, ids ...string) (string, error) {
	if !analyticsIDs(append([]string{project}, ids...)...) {
		return "", ErrInvalidInput
	}
	p := "/analytics/projects/" + project
	if len(ids) > 0 {
		p += "/experiments/" + ids[0]
	}
	return p, nil
}
func analyticsDefinitionPath(p, e string, k AnalyticsDefinitionKind) (string, error) {
	if k == AnalyticsFolders {
		base, err := analyticsPath(p)
		return base + "/experiments", err
	}
	if k != AnalyticsMetrics && k != AnalyticsAudiences && k != AnalyticsPartitions {
		return "", ErrInvalidInput
	}
	base, err := analyticsPath(p, e)
	return base + "/" + string(k), err
}
func analyticsPage(o AnalyticsListOptions) (url.Values, error) {
	if o.Limit < 0 || o.Limit > 100 || o.Cursor != "" && !analyticsIDs(o.Cursor) {
		return nil, ErrInvalidInput
	}
	q := url.Values{}
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.Cursor != "" {
		q.Set("cursor", o.Cursor)
	}
	return q, nil
}
func (a *AnalyticsClient) Capabilities(ctx context.Context, project string) (AnalyticsCapabilities, error) {
	var out AnalyticsCapabilities
	p, e := analyticsPath(project)
	if e != nil {
		return out, e
	}
	e = a.client.get(ctx, "analytics capabilities", p+"/capabilities", &out)
	if e == nil && (out.ContractRevision == "" || out.DashboardContractRevision == "") {
		e = ErrResponse
	}
	return out, e
}
func (a *AnalyticsClient) ListDefinitions(ctx context.Context, project, experiment string, kind AnalyticsDefinitionKind, o AnalyticsListOptions) (AnalyticsDefinitionPage, error) {
	var out AnalyticsDefinitionPage
	p, e := analyticsDefinitionPath(project, experiment, kind)
	if e != nil {
		return out, e
	}
	q, e := analyticsPage(o)
	if e != nil {
		return out, e
	}
	e = a.client.requestJSON(ctx, "list analytics definitions", http.MethodGet, p, q, nil, &out)
	if e == nil {
		if out.Items == nil || out.NextCursor != "" && !analyticsIDs(out.NextCursor) {
			return out, ErrResponse
		}
		for _, d := range out.Items {
			if !analyticsIDs(d.ID) {
				return out, ErrResponse
			}
		}
	}
	return out, e
}
func (a *AnalyticsClient) GetDefinition(ctx context.Context, project, experiment string, kind AnalyticsDefinitionKind, id string) (AnalyticsDefinition, error) {
	return a.definition(ctx, project, experiment, kind, id, nil)
}
func (a *AnalyticsClient) CreateDefinition(ctx context.Context, project, experiment string, kind AnalyticsDefinitionKind, id string, in AnalyticsDefinitionInput) (AnalyticsDefinition, error) {
	return a.definition(ctx, project, experiment, kind, id, &in)
}
func (a *AnalyticsClient) definition(ctx context.Context, project, experiment string, kind AnalyticsDefinitionKind, id string, in *AnalyticsDefinitionInput) (AnalyticsDefinition, error) {
	var out AnalyticsDefinition
	p, e := analyticsDefinitionPath(project, experiment, kind)
	if e != nil {
		return out, e
	}
	if !analyticsIDs(id) {
		return out, ErrInvalidInput
	}
	method := http.MethodGet
	var body any
	if in != nil {
		method = http.MethodPut
		body = in
	}
	e = a.client.requestJSON(ctx, "analytics definition", method, p+"/"+id, nil, body, &out)
	if e == nil && (out.ID != id || out.CreatedAt.IsZero()) {
		e = ErrResponse
	}
	return out, e
}
func (a *AnalyticsClient) Subject(ctx context.Context, project, experiment, subject string) (AnalyticsSubject, error) {
	var out AnalyticsSubject
	p, e := analyticsPath(project, experiment, subject)
	if e != nil {
		return out, e
	}
	e = a.client.get(ctx, "analytics subject", p+"/subjects/"+subject, &out)
	return out, e
}

var analyticsDecimal = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

func (a *AnalyticsClient) IndividualValue(ctx context.Context, project, experiment, subject, metric string) (AnalyticsIndividualValue, error) {
	var out AnalyticsIndividualValue
	p, e := analyticsPath(project, experiment, subject, metric)
	if e != nil {
		return out, e
	}
	e = a.client.get(ctx, "individual analytics value", p+"/subjects/"+subject+"/metrics/"+metric, &out)
	if e == nil && (!out.Stored || !analyticsDecimal.MatchString(out.Value)) {
		e = ErrResponse
	}
	return out, e
}
func (a *AnalyticsClient) AudienceSize(ctx context.Context, project, experiment, audience string) (AnalyticsAudienceSize, error) {
	var out AnalyticsAudienceSize
	p, e := analyticsPath(project, experiment, audience)
	if e != nil {
		return out, e
	}
	e = a.client.get(ctx, "analytics audience size", p+"/audiences/"+audience+"/size", &out)
	if e == nil && !analyticsDecimal.MatchString(out.Value) {
		e = ErrResponse
	}
	return out, e
}
func (a *AnalyticsClient) Query(ctx context.Context, project, experiment string, in AnalyticsQuery) (AnalyticsQueryResult, error) {
	var out AnalyticsQueryResult
	p, e := analyticsPath(project, experiment, in.MetricID)
	if e != nil {
		return out, e
	}
	e = a.client.requestJSON(ctx, "query analytics", http.MethodPost, p+"/query", nil, in, &out)
	if e == nil {
		if out.Points == nil || out.QueriedAt.IsZero() || out.Consistency == "" || out.Completeness == "" {
			return out, ErrResponse
		}
		for _, v := range out.Points {
			if v.At.IsZero() || !analyticsDecimal.MatchString(v.Value) {
				return out, ErrResponse
			}
		}
	}
	return out, e
}
func (a *AnalyticsClient) Apply(ctx context.Context, project, experiment string, in AnalyticsCommand) (AnalyticsReceipt, error) {
	var out AnalyticsReceipt
	p, e := analyticsPath(project, experiment, in.SubjectID, in.OperationID)
	if e != nil {
		return out, e
	}
	e = a.client.requestJSON(ctx, "apply analytics command", http.MethodPost, p+"/commands", nil, in, &out)
	if e == nil && (out.OperationID != in.OperationID || out.AcceptedAt.IsZero() || out.EventIDs == nil) {
		e = ErrResponse
	}
	return out, e
}
func (a *AnalyticsClient) ListDashboards(ctx context.Context, project, experiment string, archived bool, o AnalyticsListOptions) (AnalyticsDashboardPage, error) {
	var out AnalyticsDashboardPage
	p, e := analyticsPath(project, experiment)
	if e != nil {
		return out, e
	}
	q, e := analyticsPage(o)
	if e != nil {
		return out, e
	}
	q.Set("archived", strconv.FormatBool(archived))
	e = a.client.requestJSON(ctx, "list analytics dashboards", http.MethodGet, p+"/dashboards", q, nil, &out)
	if e == nil {
		if out.Items == nil || out.NextCursor != "" && !analyticsIDs(out.NextCursor) {
			return out, ErrResponse
		}
		for _, d := range out.Items {
			if !validAnalyticsDashboard(d) {
				return out, ErrResponse
			}
		}
	}
	return out, e
}
func validAnalyticsDashboard(d AnalyticsDashboard) bool {
	r, e := strconv.ParseUint(d.Revision, 10, 64)
	return analyticsIDs(d.ID) && e == nil && r > 0 && d.Panels != nil && !d.CreatedAt.IsZero() && !d.UpdatedAt.IsZero()
}
func (a *AnalyticsClient) GetDashboard(ctx context.Context, project, experiment, id string) (AnalyticsDashboard, error) {
	var out AnalyticsDashboard
	p, e := analyticsPath(project, experiment, id)
	if e != nil {
		return out, e
	}
	e = a.client.get(ctx, "get analytics dashboard", p+"/dashboards/"+id, &out)
	if e == nil && (out.ID != id || !validAnalyticsDashboard(out)) {
		e = ErrResponse
	}
	return out, e
}
func (a *AnalyticsClient) SaveDashboard(ctx context.Context, project, experiment, id string, in AnalyticsSaveDashboard) (AnalyticsDashboard, error) {
	var out AnalyticsDashboard
	p, e := analyticsPath(project, experiment, id, in.OperationID)
	if e != nil {
		return out, e
	}
	if _, e = strconv.ParseUint(in.ExpectedRevision, 10, 64); e != nil || !analyticsDecimal.MatchString(in.ExpectedRevision) {
		return out, ErrInvalidInput
	}
	e = a.client.requestJSON(ctx, "save analytics dashboard", http.MethodPut, p+"/dashboards/"+id, nil, in, &out)
	if e == nil && (out.ID != id || !validAnalyticsDashboard(out)) {
		e = ErrResponse
	}
	return out, e
}
