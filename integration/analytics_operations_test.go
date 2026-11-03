package integration

import (
	"github.com/Alexey-zaliznuak/aheron-go-sdk/platform"
	"net/http"
	"testing"
)

func TestAnalyticsOperationsRejectForeignProjectBeforeHTTP(t *testing.T) {
	c := analyticsOAuthFixture(t, crmOAuthSubject, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected upstream call") })
	if _, err := c.StageGraph(t.Context(), crmOAuthIntegration, crmOAuthSubject, crmOAuthSubject); err == nil {
		t.Fatal("foreign graph")
	}
	if _, err := c.AudienceSize(t.Context(), crmOAuthIntegration, crmOAuthSubject, crmOAuthSubject); err == nil {
		t.Fatal("foreign audience")
	}
	if _, err := c.Apply(t.Context(), crmOAuthIntegration, crmOAuthSubject, platform.AnalyticsCommand{SubjectID: crmOAuthSubject, OperationID: crmOAuthSubject}); err == nil {
		t.Fatal("foreign command")
	}
	if _, err := c.CreateStageSet(t.Context(), crmOAuthProject, crmOAuthSubject, crmOAuthSubject, platform.AnalyticsSaveStageSet{OperationID: crmOAuthSubject, ExpectedRevision: "1"}); err == nil {
		t.Fatal("update allowed")
	}
}
