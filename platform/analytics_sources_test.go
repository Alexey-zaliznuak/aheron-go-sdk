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

func TestAnalyticsTemplatesRevisionAndReplay(t *testing.T) {
	now := time.Now().UTC()
	template := AnalyticsStageTemplate{TemplateID: otherID, Name: "Рассылка", Revision: "9007199254740993", ParentRevision: "9007199254740992", Stages: []AnalyticsTemplateStage{{Key: "received", Name: "Отправлено"}}, CreatedAt: now, UpdatedAt: now}
	base := "/api/analytics/projects/" + projectID + "/stage-templates"
	var method, path, query string
	var reply any
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method || r.URL.Path != path || r.URL.RawQuery != query || r.Header.Get("Authorization") != "Bearer "+testToken("templates") {
			t.Errorf("incorrect authenticated route: %s %s", r.Method, r.URL)
		}
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		jsonReply(w, reply)
	}))
	defer server.Close()
	c := clientFor(t, server, provider(t, testToken("templates"))).Analytics
	ctx := context.Background()
	method, path, query, reply = "GET", base, "archived=true&limit=2", AnalyticsStageTemplatePage{Items: []AnalyticsStageTemplate{template}, NextCursor: otherID}
	page, err := c.ListStageTemplates(ctx, projectID, true, AnalyticsListOptions{Limit: 2})
	if err != nil || len(page.Items) != 1 || page.Items[0].Revision != template.Revision {
		t.Fatalf("lost exact template revision: %#v %v", page, err)
	}
	path, query, reply = base+"/"+otherID, "", template
	if _, err := c.GetStageTemplate(ctx, projectID, otherID); err != nil {
		t.Fatal(err)
	}
	path += "/versions/" + template.Revision
	if _, err := c.GetStageTemplateVersion(ctx, projectID, otherID, template.Revision); err != nil {
		t.Fatal(err)
	}
	method, path = "PUT", base+"/"+otherID
	input := AnalyticsSaveStageTemplate{OperationID: schemeID, ExpectedRevision: template.ParentRevision, Name: template.Name, Stages: template.Stages}
	for range 2 {
		if _, err := c.SaveStageTemplate(ctx, projectID, otherID, input); err != nil {
			t.Fatal(err)
		}
	}
	if bodies[len(bodies)-1] != bodies[len(bodies)-2] {
		t.Fatal("retry changed command identity")
	}
	var saved AnalyticsSaveStageTemplate
	if json.Unmarshal([]byte(bodies[len(bodies)-1]), &saved) != nil || saved.ExpectedRevision != input.ExpectedRevision || saved.OperationID != schemeID {
		t.Fatal("request lost exact revision/operation")
	}
	before := len(bodies)
	for _, revision := range []string{"0", "01", "18446744073709551616", "../version"} {
		_, err := c.GetStageTemplateVersion(ctx, projectID, otherID, revision)
		if !errors.Is(err, ErrInvalidInput) || len(bodies) != before {
			t.Fatalf("invalid revision went to network: %q %v", revision, err)
		}
	}
	method, path, reply = "GET", base+"/"+otherID, AnalyticsStageTemplate{TemplateID: schemeID}
	if _, err := c.GetStageTemplate(ctx, projectID, otherID); !errors.Is(err, ErrResponse) {
		t.Fatal("wrong template accepted", err)
	}
}
