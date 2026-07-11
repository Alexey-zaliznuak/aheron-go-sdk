// Package integration is the Aheron integrations SDK for Go. It gives an
// integration backend both halves of the platform trust model:
//
//   - Outbound (integration -> platform): a Client that calls the platform's
//     signed endpoints — resolve a parked integrationAction step, activate a
//     trigger, list trigger instances — plus a CRM client for reading/writing
//     subject data with a project API key. Every signed call is authenticated
//     with the integration's own Ed25519 private key.
//   - Inbound (platform -> integration): a Verifier that authenticates the
//     signed requests the platform sends to the integration backend (install and
//     action), so handlers run only on verified bodies they decode themselves.
//
// Construct a Client with New and a Config. Sensible defaults are applied for
// URLs, timeout, retries and logging, so the minimal setup is just the
// integration id and private key (plus a project API key if you use the CRM
// client).
package integration

import (
	"errors"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/httpclient"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/sign"
)

// Default platform base URLs, used when the corresponding Config field is empty.
//
//   - ExecutionURL is the platform origin: the signed integration endpoints live
//     under "{ExecutionURL}/api/integrations/...". (Resolve normally targets the
//     absolute URL the platform supplies in the inbound payload, so this base is
//     used mainly for trigger activation/listing.)
//   - CRMURL carries the gateway's "/api/crm" prefix: CRM calls hit
//     "{CRMURL}/projects/...", matching the platform's public CRM routes.
const (
	DefaultExecutionURL = "https://aheron.pro"
	DefaultCRMURL       = "https://aheron.pro/api/crm"
	DefaultMediaURL     = "https://aheron.pro/api/media"
)

// Config configures a Client. IntegrationID and PrivateKey are required for the
// signed platform endpoints (Steps, Triggers); APIKey is required only for the
// CRM client. Zero-valued optional fields fall back to sensible defaults.
type Config struct {
	// IntegrationID is this integration's platform id (a uuid). It is sent in the
	// X-Integration-Id header of signed callbacks.
	IntegrationID string
	// PrivateKey is this integration's Ed25519 private key, base64 (std) encoded
	// — either a 32-byte seed or a full 64-byte key. It signs outbound callbacks.
	PrivateKey string
	// APIKey is the project API key (ahr_proj_...) granted to the integration at
	// install time. It authenticates CRM data calls. Optional.
	APIKey string

	// ExecutionURL is the base URL of the execution-service public API (where the
	// signed integration endpoints live). Defaults to DefaultExecutionURL.
	ExecutionURL string
	// CRMURL is the base URL of the crm-backend public API. Defaults to
	// DefaultCRMURL.
	CRMURL string
	// MediaURL is the base URL of the media-service public API. Defaults to
	// DefaultMediaURL.
	MediaURL string

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
	// Steps resolves parked integrationAction steps.
	Steps *StepsClient
	// Triggers activates and lists integration triggers.
	Triggers *TriggersClient
	// CRM reads and writes subject data with the project API key. It is nil-safe:
	// calling it without an APIKey configured returns an error.
	CRM *CRMClient
	// Files stores and retrieves project media files with the project API key.
	// It is nil-safe: calling it without an APIKey configured returns an error.
	Files *FilesClient

	integrationID string
	signer        *sign.Signer
}

// New builds a Client from cfg. It returns an error when the private key is set
// but cannot be parsed. A missing private key is allowed (the signed endpoints
// will then return an error when used), so an integration that only needs the
// CRM client can still construct a Client.
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
	if cfg.Logger == nil {
		cfg.Logger = NopLogger()
	}

	priv, err := sign.ParsePrivateKey(cfg.PrivateKey)
	if err != nil {
		return nil, err
	}
	var signer *sign.Signer
	if priv != nil {
		signer = sign.NewSigner(priv)
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

	execHTTP := httpclient.New(transportCfg(cfg.ExecutionURL))
	crmHTTP := httpclient.New(transportCfg(cfg.CRMURL))
	mediaHTTP := httpclient.New(transportCfg(cfg.MediaURL))

	c := &Client{
		integrationID: cfg.IntegrationID,
		signer:        signer,
	}
	c.Steps = &StepsClient{http: execHTTP, id: cfg.IntegrationID, signer: signer}
	c.Triggers = &TriggersClient{http: execHTTP, id: cfg.IntegrationID, signer: signer}
	c.CRM = &CRMClient{http: crmHTTP, apiKey: cfg.APIKey}
	c.Files = &FilesClient{http: mediaHTTP, apiKey: cfg.APIKey}
	return c, nil
}

// errNoSigner is returned by signed calls when no private key was configured.
var errNoSigner = errors.New("integration: no private key configured; set Config.PrivateKey to call signed endpoints")

// errNoAPIKey is returned by CRM calls when no project API key was configured.
var errNoAPIKey = errors.New("integration: no project API key configured; set Config.APIKey to call the CRM")
