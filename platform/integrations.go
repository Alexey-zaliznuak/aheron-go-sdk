package platform

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

type IntegrationsClient struct{ client *Client }

// IntegrationMCP describes an installed provider without credentials. The URL
// comes from its published manifest and must never be supplied by the model.
type IntegrationMCP struct {
	IntegrationID  string `json:"integrationId"`
	InstallationID string `json:"installationId"`
	Slug           string `json:"slug"`
	Name           string `json:"name"`
	Version        int    `json:"version"`
	MCPURL         string `json:"mcpUrl"`
}

// MCPConnection is private transport configuration, not a tool result.
type MCPConnection struct {
	Provider  IntegrationMCP `json:"provider"`
	Token     string         `json:"token"`
	ExpiresAt time.Time      `json:"expiresAt"`
}

func (s *IntegrationsClient) ListMCP(ctx context.Context, projectID string) ([]IntegrationMCP, error) {
	if !resourceID.MatchString(projectID) {
		return nil, ErrInvalidInput
	}
	var result []IntegrationMCP
	err := s.client.requestJSON(ctx, "list integration MCP", http.MethodGet, "/projects/"+projectID+"/integration-mcp", nil, nil, &result)
	if err != nil {
		return nil, err
	}
	for _, provider := range result {
		if !validMCPProvider(provider) {
			return nil, ErrResponse
		}
	}
	return result, nil
}

func (s *IntegrationsClient) MCPConnection(ctx context.Context, projectID, integrationID string) (MCPConnection, error) {
	if !resourceID.MatchString(projectID) || !resourceID.MatchString(integrationID) {
		return MCPConnection{}, ErrInvalidInput
	}
	var result MCPConnection
	err := s.client.requestJSON(ctx, "connect integration MCP", http.MethodPost, "/projects/"+projectID+"/integration-mcp/"+integrationID+"/connection", nil, struct{}{}, &result)
	if err == nil && (!validMCPProvider(result.Provider) || result.Provider.IntegrationID != integrationID || result.Token == "" || len(result.Token) > 8192 || !result.ExpiresAt.After(time.Now())) {
		err = ErrResponse
	}
	return result, err
}

func validMCPProvider(p IntegrationMCP) bool {
	u, err := url.Parse(p.MCPURL)
	return resourceID.MatchString(p.IntegrationID) && resourceID.MatchString(p.InstallationID) && p.Version > 0 &&
		err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Opaque == ""
}

func (MCPConnection) String() string   { return "[redacted MCP delegation]" }
func (MCPConnection) GoString() string { return "[redacted MCP delegation]" }
