package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const stepID = "e139b62a-56d6-4cb5-a6ae-bb7b4cb11744"
const edgeID = "9b1a94a2-ab26-493a-bf03-cf984f3af6af"

func graphFixture() SchemeGraph {
	return SchemeGraph{SchemeID: schemeID, Revision: 12,
		Steps:    []Step{{ID: stepID, SchemeID: schemeID, Type: "message", Settings: json.RawMessage(`{"text":"Содержимое шага","nested":{"condition":"paid"}}`)}},
		Branches: []Branch{{ID: otherID, SchemeID: schemeID, Key: "success", Role: "primary", Settings: json.RawMessage(`{"condition":"active"}`)}},
		Edges:    []Edge{{ID: edgeID, SchemeID: schemeID, FromStepID: stepID, ToStepID: stepID, BranchID: otherID, OutputKey: "success", InputKey: "in", Settings: json.RawMessage(`{"delay":5}`)}},
	}
}

func TestSchemeGraphAndStepContracts(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer "+testToken("reader") {
			t.Error("wrong read transport or identity")
		}
		switch r.URL.Path {
		case "/api/projects/" + projectID + "/schemes/" + schemeID + "/graph":
			jsonReply(w, graphFixture())
		case "/api/projects/" + projectID + "/schemes/" + schemeID + "/steps/" + stepID:
			jsonReply(w, graphFixture().Steps[0])
		default:
			t.Errorf("wrong resource path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c := clientFor(t, server, provider(t, testToken("reader")))
	graph, err := c.Schemes.GetGraph(context.Background(), projectID, schemeID)
	if err != nil || graph.Revision != 12 || len(graph.Steps) != 1 || len(graph.Branches) != 1 || len(graph.Edges) != 1 {
		t.Fatalf("graph contract: %+v %v", graph, err)
	}
	if !strings.Contains(string(graph.Steps[0].Settings), "Содержимое шага") || graph.Edges[0].OutputKey != "success" || graph.Branches[0].Role != "primary" {
		t.Fatal("graph contents were dropped")
	}
	step, err := c.Schemes.GetStep(context.Background(), projectID, schemeID, stepID)
	if err != nil || string(step.Settings) != string(graph.Steps[0].Settings) {
		t.Fatalf("step contents: %+v %v", step, err)
	}
	if _, err = c.Schemes.GetGraph(context.Background(), "../outside", schemeID); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("invalid graph scope accepted")
	}
	if _, err = c.Schemes.GetStep(context.Background(), projectID, schemeID, "../outside"); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("invalid step scope accepted")
	}
	if calls.Load() != 2 {
		t.Fatal("invalid input reached transport or request replayed")
	}
}

func TestGraphRejectsIncompleteOrCrossScopeData(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*SchemeGraph)
	}{
		{"wrong scheme", func(g *SchemeGraph) { g.SchemeID = otherID }},
		{"foreign step", func(g *SchemeGraph) { g.Steps[0].SchemeID = otherID }},
		{"foreign branch", func(g *SchemeGraph) { g.Branches[0].SchemeID = otherID }},
		{"foreign edge", func(g *SchemeGraph) { g.Edges[0].SchemeID = otherID }},
		{"missing steps", func(g *SchemeGraph) { g.Steps = nil }},
		{"duplicate step", func(g *SchemeGraph) { g.Steps = append(g.Steps, g.Steps[0]) }},
		{"dangling step", func(g *SchemeGraph) { g.Edges[0].ToStepID = otherID }},
		{"dangling branch", func(g *SchemeGraph) { g.Edges[0].BranchID = stepID }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			graph := graphFixture()
			tc.change(&graph)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { jsonReply(w, graph) }))
			defer server.Close()
			c := clientFor(t, server, provider(t, testToken("reader")))
			if _, err := c.Schemes.GetGraph(context.Background(), projectID, schemeID); !errors.Is(err, ErrResponse) {
				t.Fatalf("invalid graph accepted: %v", err)
			}
		})
	}
}

func TestGraphAndStepPermissionFailures(t *testing.T) {
	for _, status := range []int{401, 403, 404} {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			http.Error(w, "SECRET upstream data", status)
		}))
		c := clientFor(t, server, provider(t, testToken("reader")))
		_, graphErr := c.Schemes.GetGraph(context.Background(), projectID, schemeID)
		_, stepErr := c.Schemes.GetStep(context.Background(), projectID, schemeID, stepID)
		server.Close()
		for _, err := range []error{graphErr, stepErr} {
			var api *APIError
			if !errors.As(err, &api) || api.StatusCode != status || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("unsafe or lost permission error: %v", err)
			}
		}
		if calls.Load() != 2 {
			t.Fatal("permission failure was replayed")
		}
	}
}
