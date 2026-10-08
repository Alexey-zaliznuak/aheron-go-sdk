// Package aheron is the common Aheron API facade in the existing SDK module.
// Resource implementations are shared with the integration SDK. A credential
// identifies its actual principal; it never acquires another principal's rights.
package aheron

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integration"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/platform"
)

// Config selects exactly one credential. Integration-specific steps, triggers,
// manifests and file/link methods retain their existing integration.Config API.
type Config struct {
	UserTokenProvider platform.TokenProvider
	ProjectAPIKey     string
	CRMOAuth          *integration.CRMOAuthConfig
	BaseURL           string
	CRMURL            string
	DocumentationURL  string
	HTTPClient        *http.Client
	// DisableTimeout removes the client timeout; context and credential expiry still apply.
	DisableTimeout    bool
	Timeout           time.Duration
	AllowLoopbackHTTP bool
}

type Client struct {
	Projects      *platform.ProjectsClient
	Schemes       *platform.SchemesClient
	Files         *platform.FilesClient
	CRM           *integration.CRMClient
	Integrations  *platform.IntegrationsClient
	Documentation *platform.DocumentationClient
}

func New(cfg Config) (*Client, error) {
	n := 0
	if cfg.UserTokenProvider != nil {
		n++
	}
	if cfg.ProjectAPIKey != "" {
		n++
	}
	if cfg.CRMOAuth != nil {
		n++
	}
	if n != 1 {
		return nil, errors.New("aheron: configure exactly one credential")
	}
	provider := cfg.UserTokenProvider
	if provider == nil {
		provider = platform.TokenProviderFunc(func(context.Context) (platform.AccessToken, error) {
			return platform.AccessToken{}, platform.ErrCredential
		})
	}
	pc := platform.Config{BaseURL: cfg.BaseURL, TokenProvider: provider, HTTPClient: cfg.HTTPClient, Timeout: cfg.Timeout, DisableTimeout: cfg.DisableTimeout, AllowLoopbackHTTP: cfg.AllowLoopbackHTTP}
	p, err := platform.New(pc)
	if err != nil {
		return nil, err
	}
	ic := integration.Config{APIKey: cfg.ProjectAPIKey, CRMOAuth: cfg.CRMOAuth, CRMURL: cfg.CRMURL, Timeout: cfg.Timeout, DisableTimeout: cfg.DisableTimeout}
	if cfg.UserTokenProvider != nil {
		ic.CRMUser = &pc
	}
	i, err := integration.New(ic)
	if err != nil {
		return nil, err
	}
	docConfig := pc
	docConfig.BaseURL = cfg.DocumentationURL
	documentation, err := platform.NewDocumentation(docConfig)
	if err != nil {
		return nil, err
	}
	return &Client{Projects: p.Projects, Schemes: p.Schemes, Files: p.Files, CRM: i.CRM, Integrations: p.Integrations, Documentation: documentation}, nil
}
