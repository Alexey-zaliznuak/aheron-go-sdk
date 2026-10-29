package platform

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAnalyticsStageRoutesRetriesAndExactGraph(t *testing.T) {
	now := time.Now().UTC()
	var method, path, query string
	var reply any
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method || r.URL.Path != path || r.URL.RawQuery != query {
			t.Errorf("unexpected route: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer "+testToken("stages") || r.Header.Get("X-Internal-Token") != "" {
			t.Error("wrong identity")
		}
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		jsonReply(w, reply)
	}))
	defer srv.Close()
	c := clientFor(t, srv, provider(t, testToken("stages"))).Analytics
	ctx := context.Background()
	base := "/api/analytics/projects/" + projectID + "/experiments/" + schemeID + "/stage-sets"
	d := AnalyticsStageSet{ID: otherID, Revision: "1", CreatedAt: now, UpdatedAt: now, AnalyticsStageSetInput: AnalyticsStageSetInput{Name: "Stages", Stages: []AnalyticsStageDefinition{{ID: projectID, Name: "Sent"}}}}
	method, path, query, reply = "GET", base, "archived=false&limit=2", AnalyticsStageSetPage{Items: []AnalyticsStageSet{d}}
	if _, e := c.ListStageSets(ctx, projectID, schemeID, false, AnalyticsListOptions{Limit: 2}); e != nil {
		t.Fatal(e)
	}
	path, query, reply = base+"/"+otherID, "", d
	if _, e := c.GetStageSet(ctx, projectID, schemeID, otherID); e != nil {
		t.Fatal(e)
	}
	method = "PUT"
	in := AnalyticsSaveStageSet{OperationID: schemeID, ExpectedRevision: "0", StageSet: d.AnalyticsStageSetInput}
	for i := 0; i < 2; i++ {
		if _, e := c.SaveStageSet(ctx, projectID, schemeID, otherID, in); e != nil {
			t.Fatal(e)
		}
	}
	if bodies[len(bodies)-1] != bodies[len(bodies)-2] {
		t.Fatal("retry identity changed")
	}
	assign := AnalyticsAssignStage{OperationID: schemeID, SubjectID: projectID, StageID: projectID}
	method, path, reply = "POST", base+"/"+otherID+"/assign", AnalyticsStageReceipt{OperationID: schemeID, SubjectID: projectID, StageID: projectID, StageSetID: otherID, Changed: true, Sequence: "9007199254740993", AcceptedAt: now}
	for i := 0; i < 2; i++ {
		v, e := c.AssignStage(ctx, projectID, schemeID, otherID, assign)
		if e != nil || v.Sequence != "9007199254740993" {
			t.Fatal(v, e)
		}
	}
	if bodies[len(bodies)-1] != bodies[len(bodies)-2] {
		t.Fatal("assignment retry changed")
	}
	graph := AnalyticsStageGraph{StageSetID: otherID, ParticipantCount: "9007199254740993", Nodes: []AnalyticsStageGraphNode{{StageID: projectID, UniqueSubjects: "9007199254740993", Visits: "9007199254740994", CurrentSubjects: "3"}}, Edges: []AnalyticsStageGraphEdge{{FromStageID: projectID, ToStageID: schemeID, UniqueSubjects: "2"}}, ObservedAt: now, Consistency: "transactional", Completeness: "all-time"}
	method, path, reply = "GET", base+"/"+otherID+"/graph", graph
	v, e := c.StageGraph(ctx, projectID, schemeID, otherID)
	if e != nil || v.ParticipantCount != graph.ParticipantCount || v.Nodes[0].Visits != graph.Nodes[0].Visits {
		t.Fatal(v, e)
	}
	path, reply = base+"/"+otherID+"/view", AnalyticsStageView{Revision: "0", Positions: []AnalyticsStagePosition{}}
	if _, e := c.GetStageView(ctx, projectID, schemeID, otherID); e != nil {
		t.Fatal(e)
	}
	view := AnalyticsSaveStageView{OperationID: schemeID, ExpectedRevision: "0", Positions: []AnalyticsStagePosition{{StageID: projectID, X: 12.5, Y: -20}}}
	method, reply = "PUT", AnalyticsStageView{Revision: "1", Positions: view.Positions, UpdatedAt: now}
	for i := 0; i < 2; i++ {
		if _, e := c.SaveStageView(ctx, projectID, schemeID, otherID, view); e != nil {
			t.Fatal(e)
		}
	}
	if bodies[len(bodies)-1] != bodies[len(bodies)-2] {
		t.Fatal("view retry changed")
	}
	method, path, reply = "POST", "/api/analytics/projects/"+projectID+"/experiments/"+schemeID+"/commands", AnalyticsReceipt{OperationID: schemeID, AcceptedAt: now, EventIDs: []string{otherID}}
	_, e = c.Apply(ctx, projectID, schemeID, AnalyticsCommand{OperationID: schemeID, SubjectID: projectID, AssignStages: []AnalyticsStageAssignment{{StageSetID: otherID, StageID: projectID}}, Increments: []AnalyticsIncrement{{MetricID: otherID, Delta: 1}}})
	if e != nil {
		t.Fatal(e)
	}
	var body AnalyticsCommand
	if json.Unmarshal([]byte(bodies[len(bodies)-1]), &body) != nil || len(body.AssignStages) != 1 || len(body.Increments) != 1 {
		t.Fatal("compound request split/lost")
	}
}

func TestAnalyticsStageFailuresAreExplicitAndNeverRetried(t *testing.T) {
	calls := 0
	status := 503
	var body any = map[string]any{"error": map[string]any{"message": "SECRET"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()
	c := clientFor(t, srv, provider(t, testToken("stages"))).Analytics
	ctx := context.Background()
	_, err := c.AssignStage(ctx, projectID, schemeID, otherID, AnalyticsAssignStage{OperationID: schemeID, SubjectID: projectID, StageID: otherID})
	if err == nil || calls != 1 {
		t.Fatal("write retried", err, calls)
	}
	_, err = c.GetStageSet(ctx, projectID, schemeID, "../../secret")
	if !errors.Is(err, ErrInvalidInput) || calls != 1 {
		t.Fatal("unsafe routing", err)
	}
	_, err = c.SaveStageView(ctx, projectID, schemeID, otherID, AnalyticsSaveStageView{OperationID: schemeID, ExpectedRevision: "01"})
	if !errors.Is(err, ErrInvalidInput) || calls != 1 {
		t.Fatal("unsafe revision", err)
	}
	status = 200
	body = map[string]any{"stageSetId": otherID, "participantCount": "0", "nodes": []any{map[string]any{"stageId": projectID, "uniqueSubjects": "0", "visits": "-1", "currentSubjects": "0"}}, "edges": []any{}, "observedAt": time.Now(), "consistency": "transactional", "completeness": "all-time"}
	_, err = c.StageGraph(ctx, projectID, schemeID, otherID)
	if !errors.Is(err, ErrResponse) {
		t.Fatal("invalid graph silently accepted", err)
	}
}
