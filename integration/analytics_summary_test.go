package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/platform"
	"net/http"
	"testing"
	"time"
)

func TestCampaignStageSummaryUsesReadScopeAndExactBoundGraph(t *testing.T) {
	now := time.Now().UTC()
	binding := platform.AnalyticsCampaignStageBinding{ProjectID: crmOAuthProject, IntegrationID: crmOAuthIntegration, CampaignID: crmOAuthSubject, ExperimentID: crmOAuthProject, StageSetID: crmOAuthIntegration, TemplateID: crmOAuthIntegration, TemplateRevision: "1", Revision: "1", SourceGeneration: "1", Stages: []platform.AnalyticsBoundStage{{Key: "sent", StageID: crmOAuthSubject}}}
	set := platform.AnalyticsStageSet{ID: binding.StageSetID, Revision: "1", CreatedAt: now, UpdatedAt: now, Source: &platform.AnalyticsStageSetSource{IntegrationID: binding.IntegrationID, CampaignID: binding.CampaignID}, ReadOnly: true, AnalyticsStageSetInput: platform.AnalyticsStageSetInput{Archived: true, Stages: []platform.AnalyticsStageDefinition{{ID: crmOAuthSubject, Name: "Sent", Archived: true}}}}
	graph := platform.AnalyticsStageGraph{StageSetID: set.ID, ParticipantCount: "18446744073709551616", Nodes: []platform.AnalyticsStageGraphNode{{StageID: crmOAuthSubject, UniqueSubjects: "18446744073709551616", Visits: "18446744073709551617", CurrentSubjects: "18446744073709551616"}}, Edges: []platform.AnalyticsStageGraphEdge{}, ObservedAt: now, Consistency: "transactional", Completeness: "all-time"}
	reply := platform.AnalyticsCampaignStageSummary{Binding: binding, StageSet: set, Graph: graph}
	requests := 0
	status := 200
	client := analyticsOAuthFixture(t, crmOAuthSubject, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/oauth/token" {
			_ = r.ParseForm()
			if r.Form.Get("scope") != "analytics.sources.read" || r.Form.Get("audience") != "analytics" {
				t.Error("wrong summary authority")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "aho_" + base64.RawURLEncoding.EncodeToString(make([]byte, 32)), "token_type": "Bearer", "expires_in": 300, "scope": "analytics.sources.read"})
			return
		}
		requests++
		if r.Method != "GET" || r.URL.Path != "/analytics/projects/"+crmOAuthProject+"/sources/messengers/installations/"+crmOAuthSubject+"/campaigns/"+crmOAuthSubject+"/summary" || r.URL.RawQuery != "" || r.Header.Get("X-Internal-Token") != "" {
			t.Error("wrong summary route/credentials")
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(reply)
	})
	got, err := client.GetCampaignStageSummary(context.Background(), crmOAuthProject, crmOAuthSubject)
	if err != nil || got.Graph.ParticipantCount != graph.ParticipantCount || got.Graph.Nodes[0].Visits != graph.Nodes[0].Visits || !got.StageSet.Archived {
		t.Fatal(got, err)
	}
	reply.Graph.StageSetID = crmOAuthProject
	if _, err = client.GetCampaignStageSummary(context.Background(), crmOAuthProject, crmOAuthSubject); err == nil {
		t.Fatal("foreign graph accepted")
	}
	reply.Graph = graph
	reply.Graph.Nodes = append([]platform.AnalyticsStageGraphNode(nil), graph.Nodes...)
	reply.Graph.Nodes[0].Visits = "1.5"
	if _, err = client.GetCampaignStageSummary(context.Background(), crmOAuthProject, crmOAuthSubject); err == nil {
		t.Fatal("inexact count accepted")
	}
	status = 404
	before := requests
	if _, err = client.GetCampaignStageSummary(context.Background(), crmOAuthProject, crmOAuthSubject); err == nil || requests != before+1 {
		t.Fatal("missing binding hidden/retried", err)
	}
	before = requests
	if _, err = client.GetCampaignStageSummary(context.Background(), crmOAuthIntegration, crmOAuthSubject); err == nil || requests != before {
		t.Fatal("cross project request sent", err)
	}
}
