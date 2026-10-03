// Package integrationmcp connects to an installed integration through the
// platform-issued delegation. User credentials never cross this boundary.
package integrationmcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/platform"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var ErrContract = errors.New("integrationmcp: invalid provider contract")
var ErrPermission = errors.New("integrationmcp: tool is not read-only")
var ErrTool = errors.New("integrationmcp: tool rejected the request")

// Client binds one short-lived delegation to one exact published endpoint.
// Create it for an operation, then Close; it does not cache user credentials.
type Client struct {
	session *mcp.ClientSession
	cancel  context.CancelFunc
}
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	ReadOnly    bool           `json:"readOnly"`
	Destructive bool           `json:"destructive"`
	Idempotent  bool           `json:"idempotent"`
}
type Result struct {
	Data    json.RawMessage `json:"data,omitempty"`
	Text    []string        `json:"text,omitempty"`
	IsError bool            `json:"isError,omitempty"`
}

func Connect(ctx context.Context, connection platform.MCPConnection, transport http.RoundTripper) (*Client, error) {
	endpoint, err := url.Parse(connection.Provider.MCPURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || connection.Token == "" || !connection.ExpiresAt.After(time.Now()) {
		return nil, ErrContract
	}
	if transport == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.Proxy = nil
		transport = t
	}
	ctx, cancel := context.WithDeadline(ctx, connection.ExpiresAt)
	hc := &http.Client{Timeout: 30 * time.Second, Transport: &boundTransport{transport, endpoint.String(), connection.Token, connection.ExpiresAt}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	client := mcp.NewClient(&mcp.Implementation{Name: "aheron-integration-gateway", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint.String(), HTTPClient: hc, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	return &Client{session, cancel}, nil
}
func (c *Client) Close() { _ = c.session.Close(); c.cancel() }
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	result := []Tool{}
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 8; page++ {
		list, err := c.session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		for _, t := range list.Tools {
			if t == nil || t.Name == "" || len(t.Name) > 128 || seen[t.Name] || len(result) >= 128 {
				return nil, ErrContract
			}
			seen[t.Name] = true
			raw, err := json.Marshal(t.InputSchema)
			if err != nil || len(raw) > 64<<10 || len(t.Description) > 8192 {
				return nil, ErrContract
			}
			var schema map[string]any
			if json.Unmarshal(raw, &schema) != nil || schema["type"] != "object" {
				return nil, ErrContract
			}
			item := Tool{Name: t.Name, Description: t.Description, InputSchema: schema, Destructive: true}
			if t.Annotations != nil {
				item.ReadOnly = t.Annotations.ReadOnlyHint
				item.Idempotent = t.Annotations.IdempotentHint
				if t.Annotations.DestructiveHint != nil {
					item.Destructive = *t.Annotations.DestructiveHint
				}
			}
			result = append(result, item)
		}
		if list.NextCursor == "" {
			return result, nil
		}
		if list.NextCursor == cursor {
			return nil, ErrContract
		}
		cursor = list.NextCursor
	}
	return nil, ErrContract
}

// Call checks the discovered schema and effect before dispatch. It never
// retries a call; a transport failure after a write has an unknown outcome.
func (c *Client) Call(ctx context.Context, name string, args json.RawMessage, readOnly bool) (Result, error) {
	var input map[string]any
	if len(args) > 64<<10 || json.Unmarshal(args, &input) != nil || input == nil {
		return Result{}, ErrContract
	}
	tools, err := c.ListTools(ctx)
	if err != nil {
		return Result{}, err
	}
	var selected *Tool
	for i := range tools {
		if tools[i].Name == name {
			selected = &tools[i]
			break
		}
	}
	if selected == nil {
		return Result{}, ErrContract
	}
	if readOnly && !selected.ReadOnly {
		return Result{}, ErrPermission
	}
	raw, _ := json.Marshal(selected.InputSchema)
	var schema jsonschema.Schema
	if json.Unmarshal(raw, &schema) != nil {
		return Result{}, ErrContract
	}
	resolved, err := schema.Resolve(nil)
	if err != nil || resolved.Validate(input) != nil {
		return Result{}, ErrContract
	}
	reply, err := c.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return Result{}, err
	}
	if reply == nil {
		return Result{}, ErrContract
	}
	result := Result{IsError: reply.IsError}
	if reply.StructuredContent != nil {
		result.Data, err = json.Marshal(reply.StructuredContent)
		if err != nil {
			return Result{}, ErrContract
		}
	}
	for _, content := range reply.Content {
		if t, ok := content.(*mcp.TextContent); ok {
			result.Text = append(result.Text, t.Text)
		} else {
			return Result{}, ErrContract
		}
	}
	encoded, _ := json.Marshal(result)
	if len(encoded) > 256<<10 {
		return Result{}, ErrContract
	}
	return result, nil
}

type boundTransport struct {
	base            http.RoundTripper
	endpoint, token string
	expires         time.Time
}

func (t *boundTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.String() != t.endpoint || !t.expires.After(time.Now()) {
		return nil, ErrContract
	}
	req := r.Clone(r.Context())
	req.Header = req.Header.Clone()
	req.Header.Set("Authorization", "Bearer "+t.token)
	response, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		response.Body.Close()
		return nil, ErrContract
	}
	response.Body = &boundedBody{ReadCloser: response.Body, remaining: 1 << 20}
	return response, nil
}

type boundedBody struct {
	io.ReadCloser
	remaining int64
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if int64(len(p)) > b.remaining+1 {
		p = p[:b.remaining+1]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	if b.remaining < 0 {
		return 0, ErrContract
	}
	return n, err
}
