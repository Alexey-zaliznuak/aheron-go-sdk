// Package integration is the Aheron integrations SDK for Go. It gives an
// integration backend both halves of the platform trust model:
//
//   - Outbound (integration -> platform): a Client that calls the platform's
//     OAuth endpoints — resolve a parked integrationAction step, activate a
//     trigger, list trigger instances, publish the integration's own catalog
//     manifest — plus a CRM client for reading/writing subject data.
//   - Inbound (platform -> integration): a Verifier that authenticates the
//     signed requests the platform sends to the integration backend (lifecycle and
//     action), so handlers run only on verified bodies they decode themselves.
//
// Construct a Client with New and a Config. Sensible defaults are applied for
// URLs, timeout, retries and logging. Installation-scoped platform calls need
// the corresponding OAuth configuration; explicit project API keys are only
// for user-created credentials.
package integration

import (
	"errors"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/httpclient"
)

// Default platform base URLs, used when the corresponding Config field is empty.
// Each base already carries its gateway prefix (the platform routes every
// service under its own "/api/<service>" namespace):
//
//   - ExecutionURL carries the gateway's "/api/execution" prefix: the signed
//     integration endpoints live at "{ExecutionURL}/integrations/..." (resolve,
//     trigger activation/listing).
//   - CRMURL carries the gateway's "/api/crm" prefix: CRM calls hit
//     "{CRMURL}/projects/...", matching the platform's public CRM routes.
//   - MediaURL carries the gateway's "/api/media" prefix.
//   - CatalogURL carries the bare gateway "/api" prefix: the catalog lives on
//     the platform backend, whose routes are "/api/integrations/...".
const (
	DefaultExecutionURL = "https://aheron.pro/api/execution"
	DefaultCRMURL       = "https://aheron.pro/api/crm"
	DefaultMediaURL     = "https://aheron.pro/api/media"
	DefaultCatalogURL   = "https://aheron.pro/api"
	DefaultLinksURL     = "https://link.aheron.pro/api"
)

// DefaultJWKSURL is the platform's well-known integration JWKS endpoint on
// aheron.pro. It is the fallback used when VerifierConfig.JWKSURL or
// ConsoleVerifierConfig.JWKSURL is empty. Both the inbound request signature
// keys and the console view-token signing keys are published here.
const DefaultJWKSURL = "https://aheron.pro/.well-known/aheron-integration-jwks.json"

// Config configures a Client. Steps/Triggers use ExecutionOAuth and CRM uses
// CRMOAuth when configured. Files uses FilesOAuth. Other calls retain their signature/API-key
// requirements. Optional fields have defaults.
type Config struct {
	// IntegrationID is this integration's platform id (a uuid). It is sent in the
	// X-Integration-Id header of signed callbacks.
	IntegrationID string
	// APIKey is an explicit, user-created project API key. It authenticates CRM,
	// files, or links calls when no scoped OAuth client is configured.
	APIKey string

	// ExecutionOAuth authenticates Steps and Triggers with installation-scoped
	// OAuth. When configured, these methods never fall back to signed requests.
	ExecutionOAuth *ExecutionOAuthConfig
	// CRMOAuth authenticates every CRM method for one project installation.
	CRMOAuth *CRMOAuthConfig
	// FilesOAuth authenticates all Files methods for one project installation.
	FilesOAuth *FilesOAuthConfig
	// LinksOAuth authenticates project links, excluding application callback registration.
	LinksOAuth *LinksOAuthConfig
	// ApplicationOAuth authenticates Catalog.Sync and Links.RegisterCallback.
	// It carries no installation identity and never replaces project permissions.
	ApplicationOAuth *ApplicationOAuthConfig

	// ExecutionURL is the base URL of the execution-service public API, already
	// carrying the gateway's "/api/execution" prefix (the signed integration
	// endpoints live at "{ExecutionURL}/integrations/..."). Defaults to
	// DefaultExecutionURL.
	ExecutionURL string
	// CRMURL is the base URL of the crm-backend public API. Defaults to
	// DefaultCRMURL.
	CRMURL string
	// MediaURL is the base URL of the media-service public API. Defaults to
	// DefaultMediaURL.
	MediaURL string
	// CatalogURL is the base URL of the platform backend's public API, carrying
	// the gateway's "/api" prefix. Defaults to DefaultCatalogURL. Used by the
	// Catalog client.
	CatalogURL string
	// LinksURL points directly to link-service, including /api. Proxies must preserve the signed request path.
	LinksURL string

	// PublicBaseURL is this integration's own externally reachable base URL, with
	// no trailing slash. Catalog.Sync resolves the relative paths of a Manifest
	// against it, which is what keeps deployment addresses out of the source.
	PublicBaseURL string

	// Transport tuning. Zero values fall back to the httpclient defaults.
	Timeout      time.Duration
	RetryCount   int
	RetryWaitMin time.Duration
	RetryWaitMax time.Duration

	// Logger receives SDK logs. Defaults to a no-op (silent).
	Logger Logger
}

// Client is the outbound half of the SDK: it groups the platform capabilities an
// integration uses. It is safe for concurrent use.
type Client struct {
	// Steps resolves parked integrationAction steps with ExecutionOAuth.
	Steps *StepsClient
	// Triggers activates and lists integration triggers.
	Triggers *TriggersClient
	// CRM reads and writes data using CRMOAuth or an explicit project API key.
	CRM *CRMClient
	// Files stores and retrieves project media files with FilesOAuth or a project API key.
	Files *FilesClient
	// Catalog publishes this integration's own block and endpoint declarations.
	Catalog *CatalogClient
	Links   *LinksClient

	integrationID string
}

// New builds a Client from cfg. Installation-scoped calls require their OAuth
// configuration; explicit project API keys remain available for user-created
// credentials.
func New(cfg Config) (*Client, error) {
	if cfg.ExecutionURL == "" {
		cfg.ExecutionURL = DefaultExecutionURL
	}
	if cfg.CRMURL == "" {
		cfg.CRMURL = DefaultCRMURL
	}
	if cfg.MediaURL == "" {
		cfg.MediaURL = DefaultMediaURL
	}
	if cfg.CatalogURL == "" {
		cfg.CatalogURL = DefaultCatalogURL
	}
	if cfg.LinksURL == "" {
		cfg.LinksURL = DefaultLinksURL
	}
	if cfg.Logger == nil {
		cfg.Logger = NopLogger()
	}

	transportCfg := func(baseURL string) httpclient.Config {
		return httpclient.Config{
			BaseURL:      baseURL,
			Timeout:      cfg.Timeout,
			RetryCount:   cfg.RetryCount,
			RetryWaitMin: cfg.RetryWaitMin,
			RetryWaitMax: cfg.RetryWaitMax,
			Logger:       cfg.Logger,
		}
	}

	execOAuth, err := newExecutionOAuth(cfg.ExecutionURL, cfg.ExecutionOAuth)
	if err != nil {
		return nil, err
	}
	crmHTTP := httpclient.New(transportCfg(cfg.CRMURL))
	crmOAuth, err := newCRMOAuth(cfg.CRMURL, cfg.CRMOAuth)
	if err != nil {
		return nil, err
	}
	mediaHTTP := httpclient.New(transportCfg(cfg.MediaURL))
	filesOAuth, err := newFilesOAuth(cfg.MediaURL, cfg.FilesOAuth)
	if err != nil {
		return nil, err
	}
	linksOAuth, err := newLinksOAuth(cfg.LinksURL, cfg.LinksOAuth)
	if err != nil {
		return nil, err
	}
	catalogOAuth, err := newApplicationOAuth(cfg.CatalogURL, "catalog", cfg.ApplicationOAuth)
	if err != nil {
		return nil, err
	}
	callbacksOAuth, err := newApplicationOAuth(cfg.LinksURL, "links", cfg.ApplicationOAuth)
	if err != nil {
		return nil, err
	}

	c := &Client{integrationID: cfg.IntegrationID}
	c.Steps = &StepsClient{oauth: execOAuth}
	c.Triggers = &TriggersClient{oauth: execOAuth}
	c.CRM = &CRMClient{http: crmHTTP, apiKey: cfg.APIKey, oauth: crmOAuth}
	c.Files = &FilesClient{http: mediaHTTP, apiKey: cfg.APIKey, oauth: filesOAuth}
	c.Links = &LinksClient{applicationOAuth: callbacksOAuth, oauth: linksOAuth, http: httpclient.New(transportCfg(cfg.LinksURL)), baseURL: cfg.LinksURL, id: cfg.IntegrationID, apiKey: cfg.APIKey}
	c.Catalog = &CatalogClient{
		oauth:         catalogOAuth,
		publicBaseURL: cfg.PublicBaseURL,
		log:           cfg.Logger,
	}
	return c, nil
}

// errNoAPIKey is returned by CRM calls when no project API key was configured.
var errNoAPIKey = errors.New("integration: no project API key configured; set Config.APIKey to call the CRM")
