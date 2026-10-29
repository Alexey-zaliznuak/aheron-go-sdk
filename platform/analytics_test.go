package platform

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAnalyticsResourcePathsAndExactValues(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	base := "/api/analytics/projects/" + projectID
	folder := base + "/experiments/" + schemeID
	var wantMethod, wantPath, wantQuery string
	var response any
	var bodies []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != wantMethod || r.URL.Path != wantPath || r.URL.RawQuery != wantQuery {
			t.Errorf("route %s %s?%s, want %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery, wantMethod, wantPath, wantQuery)
		}
		if r.Header.Get("Authorization") != "Bearer "+testToken("analytics") || r.Header.Get("X-Internal-Token") != "" {
			t.Error("wrong authority")
		}
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		jsonReply(w, response)
	}))
	defer api.Close()
	a := clientFor(t, api, provider(t, testToken("analytics"))).Analytics
	wantMethod, wantPath = "GET", base+"/capabilities"
	response = AnalyticsCapabilities{ContractRevision: "analytics/1", DashboardContractRevision: "analytics-dashboards/1"}
	if _, e := a.Capabilities(ctx, projectID); e != nil {
		t.Fatal(e)
	}
	for _, kind := range []AnalyticsDefinitionKind{AnalyticsFolders, AnalyticsMetrics, AnalyticsAudiences, AnalyticsPartitions} {
		path := folder + "/" + string(kind)
		id := otherID
		if kind == AnalyticsFolders {
			path = base + "/experiments"
			id = schemeID
		}
		d := AnalyticsDefinition{ID: id, AnalyticsDefinitionInput: AnalyticsDefinitionInput{Name: "Purchases"}, CreatedAt: now}
		wantMethod, wantPath, wantQuery = "PUT", path+"/"+id, ""
		response = d
		for i := 0; i < 2; i++ {
			if _, e := a.CreateDefinition(ctx, projectID, schemeID, kind, id, d.AnalyticsDefinitionInput); e != nil {
				t.Fatal(e)
			}
		}
		if bodies[len(bodies)-1] != bodies[len(bodies)-2] {
			t.Fatal("retry body changed")
		}
		wantMethod = "GET"
		if _, e := a.GetDefinition(ctx, projectID, schemeID, kind, id); e != nil {
			t.Fatal(e)
		}
		wantPath, wantQuery = path, "cursor="+otherID+"&limit=2"
		response = AnalyticsDefinitionPage{Items: []AnalyticsDefinition{d}, NextCursor: otherID}
		page, e := a.ListDefinitions(ctx, projectID, schemeID, kind, AnalyticsListOptions{Cursor: otherID, Limit: 2})
		if e != nil || page.NextCursor != otherID {
			t.Fatal(page, e)
		}
	}
	wantQuery = ""
	wantPath = folder + "/subjects/" + otherID
	response = AnalyticsSubject{Audiences: []string{schemeID}, Sequence: 3}
	if _, e := a.Subject(ctx, projectID, schemeID, otherID); e != nil {
		t.Fatal(e)
	}
	wantPath += "/metrics/" + projectID
	response = AnalyticsIndividualValue{Value: "9007199254740993", Stored: true}
	v, e := a.IndividualValue(ctx, projectID, schemeID, otherID, projectID)
	if e != nil || v.Value != "9007199254740993" {
		t.Fatal(v, e)
	}
	wantPath = folder + "/audiences/" + otherID + "/size"
	response = AnalyticsAudienceSize{Value: "9007199254740993"}
	if _, e := a.AudienceSize(ctx, projectID, schemeID, otherID); e != nil {
		t.Fatal(e)
	}
	wantMethod, wantPath = "POST", folder+"/query"
	response = AnalyticsQueryResult{Points: []AnalyticsPoint{{At: now, Series: otherID, Value: "9007199254740993"}}, QueriedAt: now, Consistency: "eventual", Completeness: "notGuaranteed"}
	q, e := a.Query(ctx, projectID, schemeID, AnalyticsQuery{MetricID: otherID, From: now.Add(-time.Hour), To: now, Interval: "hour", Calculation: "sum"})
	if e != nil || q.Points[0].Value != "9007199254740993" {
		t.Fatal(q, e)
	}
	wantPath = folder + "/commands"
	response = AnalyticsReceipt{OperationID: projectID, AcceptedAt: now, EventIDs: []string{otherID}, SubjectSequence: 4}
	command := AnalyticsCommand{OperationID: projectID, SubjectID: otherID, Increments: []AnalyticsIncrement{{MetricID: schemeID, Delta: 3}}}
	for i := 0; i < 2; i++ {
		if _, e := a.Apply(ctx, projectID, schemeID, command); e != nil {
			t.Fatal(e)
		}
	}
	if bodies[len(bodies)-1] != bodies[len(bodies)-2] {
		t.Fatal("command retry changed")
	}
	d := AnalyticsDashboard{ID: otherID, Revision: "9007199254740993", CreatedAt: now, UpdatedAt: now, AnalyticsDashboardInput: AnalyticsDashboardInput{Name: "Overview", Period: AnalyticsDashboardPeriod{Kind: "relative", Relative: "7d"}, Panels: []AnalyticsDashboardPanel{}}}
	wantMethod, wantPath = "PUT", folder+"/dashboards/"+otherID
	response = d
	save := AnalyticsSaveDashboard{OperationID: projectID, ExpectedRevision: "9007199254740992", Dashboard: d.AnalyticsDashboardInput}
	for i := 0; i < 2; i++ {
		got, e := a.SaveDashboard(ctx, projectID, schemeID, otherID, save)
		if e != nil || got.Revision != d.Revision {
			t.Fatal(got, e)
		}
	}
	if bodies[len(bodies)-1] != bodies[len(bodies)-2] {
		t.Fatal("dashboard retry changed")
	}
	wantMethod = "GET"
	if got, e := a.GetDashboard(ctx, projectID, schemeID, otherID); e != nil || !reflect.DeepEqual(got, d) {
		t.Fatal(got, e)
	}
	wantPath, wantQuery = folder+"/dashboards", "archived=true&limit=2"
	response = AnalyticsDashboardPage{Items: []AnalyticsDashboard{d}}
	if _, e := a.ListDashboards(ctx, projectID, schemeID, true, AnalyticsListOptions{Limit: 2}); e != nil {
		t.Fatal(e)
	}
}

func TestAnalyticsUnknownOutcomeNoReplayAndDisabledValue(t *testing.T) {
	calls := 0
	status := 503
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, `{"error":{"code":"individual_value_disabled","message":"SECRET"}}`)
	}))
	defer api.Close()
	a := clientFor(t, api, provider(t, testToken("analytics"))).Analytics
	_, e := a.Apply(context.Background(), projectID, schemeID, AnalyticsCommand{OperationID: projectID, SubjectID: otherID})
	if e == nil || calls != 1 || strings.Contains(e.Error(), "SECRET") {
		t.Fatal(e, calls)
	}
	status = 409
	_, e = a.IndividualValue(context.Background(), projectID, schemeID, otherID, projectID)
	if !errors.Is(e, ErrAnalyticsIndividualValueDisabled) {
		t.Fatal(e)
	}
	before := calls
	for _, id := range []string{"../escape", "00000000-0000-0000-0000-000000000000", strings.ToUpper(projectID)} {
		if _, e := a.Capabilities(context.Background(), id); !errors.Is(e, ErrInvalidInput) {
			t.Fatal(id, e)
		}
	}
	if _, e := a.ListDefinitions(context.Background(), projectID, schemeID, "../escape", AnalyticsListOptions{}); !errors.Is(e, ErrInvalidInput) {
		t.Fatal(e)
	}
	if _, e := a.ListDashboards(context.Background(), projectID, schemeID, false, AnalyticsListOptions{Limit: 101}); !errors.Is(e, ErrInvalidInput) {
		t.Fatal(e)
	}
	if calls != before {
		t.Fatal("invalid identifiers reached network")
	}
}

func TestAnalyticsRejectsMalformedReceiptsAndNumericValues(t *testing.T) {
	var body any = map[string]any{"value": 9007199254740993, "stored": true}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { jsonReply(w, body) }))
	defer api.Close()
	c := clientFor(t, api, provider(t, testToken("one")))
	if _, e := c.Analytics.IndividualValue(context.Background(), projectID, schemeID, otherID, projectID); !errors.Is(e, ErrResponse) {
		t.Fatal(e)
	}
	body = AnalyticsReceipt{OperationID: otherID, AcceptedAt: time.Now(), EventIDs: []string{projectID}}
	if _, e := c.Analytics.Apply(context.Background(), projectID, schemeID, AnalyticsCommand{OperationID: projectID, SubjectID: otherID}); !errors.Is(e, ErrResponse) {
		t.Fatal(e)
	}
	var out AnalyticsIndividualValue
	if json.Unmarshal([]byte(`{"value":"9007199254740993","stored":true}`), &out) != nil || out.Value != "9007199254740993" {
		t.Fatal(out)
	}
}
