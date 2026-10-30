package integration

import (
	"net/http"
	"strings"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
)

type AnalyticsOAuthConfig struct {
	Provider       *integrationoauth.Provider
	ProjectID      string
	InstallationID string
	HTTPClient     *http.Client
}

// AnalyticsClient accesses ordinary analytics definitions with current project grants.
type AnalyticsClient struct {
	projectID, installationID, baseURL string
	definitionsRead, definitionsWrite  *integrationoauth.Client
}

func newAnalyticsClient(baseURL string, cfg *AnalyticsOAuthConfig) (*AnalyticsClient, error) {
	c := &AnalyticsClient{baseURL: strings.TrimRight(baseURL, "/")}
	if cfg == nil {
		return c, nil
	}
	c.projectID, c.installationID = cfg.ProjectID, cfg.InstallationID
	for _, op := range []struct {
		dest  **integrationoauth.Client
		scope string
	}{{&c.definitionsRead, "analytics.read"}, {&c.definitionsWrite, "analytics.write"}} {
		v, e := integrationoauth.NewClient(integrationoauth.ClientConfig{Provider: cfg.Provider, BaseURL: baseURL, HTTPClient: cfg.HTTPClient, TokenRequest: integrationoauth.Request{ProjectID: cfg.ProjectID, InstallationID: cfg.InstallationID, Audience: "analytics", Scopes: []string{op.scope}}})
		if e != nil {
			return nil, e
		}
		*op.dest = v
	}
	return c, nil
}
