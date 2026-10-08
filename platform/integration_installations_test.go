package platform

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInstallationsRemainVisibleWithoutMCP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken("user") {
			t.Error("missing user credential")
		}
		switch r.URL.Path {
		case "/api/projects/" + projectID + "/integrations":
			jsonReply(w, []IntegrationInstallation{{ProjectID: projectID, IntegrationID: otherID, Status: "active"}})
		case "/api/projects/" + projectID + "/integration-mcp":
			jsonReply(w, []IntegrationMCP{})
		default:
			t.Error("unexpected endpoint")
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client := clientFor(t, server, provider(t, testToken("user")))
	installed, err := client.Integrations.ListInstallations(t.Context(), projectID)
	if err != nil || len(installed) != 1 || installed[0].IntegrationID != otherID {
		t.Fatalf("installation lost: %+v %v", installed, err)
	}
	mcp, err := client.Integrations.ListMCP(t.Context(), projectID)
	if err != nil || len(mcp) != 0 {
		t.Fatalf("MCP availability changed installation semantics: %+v %v", mcp, err)
	}
}

func TestInstallationsRejectForeignProjectResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, []IntegrationInstallation{{ProjectID: otherID, IntegrationID: otherID, Status: "active"}})
	}))
	defer server.Close()
	client := clientFor(t, server, provider(t, testToken("user")))
	if _, err := client.Integrations.ListInstallations(t.Context(), projectID); !errors.Is(err, ErrResponse) {
		t.Fatalf("foreign project accepted: %v", err)
	}
	if _, err := client.Integrations.ListInstallations(t.Context(), "../escape"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid input accepted: %v", err)
	}
}
