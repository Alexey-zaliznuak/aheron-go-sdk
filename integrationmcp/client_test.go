package integrationmcp

import (
	"context"
	"encoding/json"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/platform"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestScopedDiscoveryAndCall(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "provider", Version: "1"}, nil)
	var writes atomic.Int32
	type args struct {
		Value string `json:"value"`
	}
	type output struct {
		Value string `json:"value"`
	}
	mcp.AddTool(server, &mcp.Tool{Name: "read", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(_ context.Context, _ *mcp.CallToolRequest, a args) (*mcp.CallToolResult, output, error) {
		return nil, output{a.Value}, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "write"}, func(_ context.Context, _ *mcp.CallToolRequest, a args) (*mcp.CallToolResult, output, error) {
		writes.Add(1)
		return nil, output{a.Value}, nil
	})
	server.AddTool(&mcp.Tool{Name: "opaque", InputSchema: map[string]any{"type": "object"}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		raw := req.Params.Arguments
		return &mcp.CallToolResult{StructuredContent: raw, Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	httpServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer delegated" {
			t.Errorf("unexpected authorization")
			w.WriteHeader(401)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	client, err := Connect(t.Context(), platform.MCPConnection{Provider: platform.IntegrationMCP{MCPURL: httpServer.URL}, Token: "delegated", ExpiresAt: time.Now().Add(time.Minute)}, httpServer.Client().Transport)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	tools, err := client.ListTools(t.Context())
	if err != nil || len(tools) != 3 {
		t.Fatalf("discovery %v %v", tools, err)
	}
	result, err := client.Call(t.Context(), "read", json.RawMessage(`{"value":"hello"}`), true)
	if err != nil || string(result.Data) != `{"value":"hello"}` {
		t.Fatalf("call %s %v", result.Data, err)
	}
	if _, err = client.Call(t.Context(), "write", json.RawMessage(`{"value":"hello"}`), true); err != ErrPermission {
		t.Fatalf("read path allowed write: %v", err)
	}
	if _, err = client.Call(t.Context(), "write", json.RawMessage(`{"value":1}`), false); err != ErrContract {
		t.Fatalf("invalid arguments accepted: %v", err)
	}
	if writes.Load() != 0 {
		t.Fatal("rejected calls executed")
	}
	if _, err = client.Call(t.Context(), "write", json.RawMessage(`{"value":"hello"}`), false); err != nil {
		t.Fatal(err)
	}
	if writes.Load() != 1 {
		t.Fatal("mutation not executed exactly once")
	}
	result, err = client.Call(t.Context(), "opaque", json.RawMessage(`{"value":9007199254740993}`), true)
	if err != nil || !strings.Contains(string(result.Data), "9007199254740993") {
		t.Fatalf("opaque JSON rounded: %s %v", result.Data, err)
	}
}
func TestEndpointBindingRejectsRedirect(t *testing.T) {
	var leaked atomic.Bool
	sink := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer sink.Close()
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	_, err := Connect(t.Context(), platform.MCPConnection{Provider: platform.IntegrationMCP{MCPURL: redirect.URL}, Token: "delegated", ExpiresAt: time.Now().Add(time.Minute)}, redirect.Client().Transport)
	if err == nil || leaked.Load() {
		t.Fatal("delegation followed redirect")
	}
}
