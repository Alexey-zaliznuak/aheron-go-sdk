// Command echo is a minimal Aheron integration backend. It exposes one signed
// endpoint:
//
//   - /blocks/action receives an integrationAction request whose body shape is
//     designed by the integration author (action_request_template).
//     It resolves the parked step through its first declared output.
//
// The action_request_template configured on the platform for this example is:
//
//	{
//	  "context":            "{{context}}",
//	  "actionKey":          "{{actionKey}}",
//	  "settings":           "{{blockSettings}}",
//	  "vars":               "{{vars}}",
//	  "integrationContext": "{{integrationContext}}"
//	}
//
// Run it with the integration's own credentials in the environment:
//
//	INTEGRATION_ID   — this integration's platform id (uuid)
//	INTEGRATION_KEY  — this integration's Ed25519 private key (base64 seed/64b)
//	OAUTH_CLIENT_ID  — registered OAuth client
//	OAUTH_KEY_ID     — registered credential ID
//	OAUTH_TOKEN_URL  — trusted HTTPS token endpoint
//	PROJECT_ID      — project for this single-installation example
//	INSTALLATION_ID — existing installation lifetime for that project
//	JWKS_URL         — platform integration JWKS, e.g.
//	                   https://aheron.pro/.well-known/aheron-integration-jwks.json
//	ADDR             — listen address (default :8090)
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integration"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/integration/zaplog"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"

	"go.uber.org/zap"
)

// actionBody mirrors the action_request_template configured on the platform (see
// the package doc). Context is embedded so it can be passed to Steps.Resolve.
type actionBody struct {
	Context   integration.ExecutionContext `json:"context"`
	ActionKey string                       `json:"actionKey"`
	Settings  struct {
		Outputs []string `json:"outputs"`
	} `json:"settings"`
	Vars               json.RawMessage `json:"vars"`
	IntegrationContext json.RawMessage `json:"integrationContext"`
}

func main() {
	logger, _ := zap.NewProduction()
	defer func() { _ = logger.Sync() }()
	// This small example is bound to one already provisioned installation.
	// Multi-project integrations load the current identity from their durable
	// lifecycle store before each platform operation.
	private, err := base64.StdEncoding.DecodeString(os.Getenv("INTEGRATION_KEY"))
	if err != nil || (len(private) != ed25519.SeedSize && len(private) != ed25519.PrivateKeySize) {
		log.Fatal("invalid OAuth private key configuration")
	}
	if len(private) == ed25519.SeedSize {
		private = ed25519.NewKeyFromSeed(private)
	}
	provider, err := integrationoauth.NewProvider(integrationoauth.Config{
		ClientID: os.Getenv("OAUTH_CLIENT_ID"), KeyID: os.Getenv("OAUTH_KEY_ID"),
		PrivateKey: ed25519.PrivateKey(private), TokenEndpoint: os.Getenv("OAUTH_TOKEN_URL"),
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
	})
	if err != nil {
		log.Fatal("invalid OAuth client configuration")
	}

	client, err := integration.New(integration.Config{
		IntegrationID: os.Getenv("INTEGRATION_ID"),
		Logger:        zaplog.New(logger),
		ExecutionOAuth: &integration.ExecutionOAuthConfig{
			Provider: provider, ProjectID: os.Getenv("PROJECT_ID"), InstallationID: os.Getenv("INSTALLATION_ID"),
		},
	})
	if err != nil {
		log.Fatalf("build client: %v", err)
	}

	verifier, err := integration.NewVerifier(integration.VerifierConfig{
		JWKSURL: os.Getenv("JWKS_URL"),
		Logger:  zaplog.New(logger),
	})
	if err != nil {
		log.Fatalf("build verifier: %v", err)
	}

	// Action: one endpoint serves every action block; {{actionKey}} tells them
	// apart. The handler resolves the step through the block's first output.
	http.Handle("/blocks/action", verifier.Handle(func(ctx context.Context, r *http.Request) error {
		var body actionBody
		if err := integration.DecodeBody(r, &body); err != nil {
			return err
		}
		output := "ok"
		if len(body.Settings.Outputs) > 0 {
			output = body.Settings.Outputs[0]
		}
		logger.Info("action received",
			zap.String("actionKey", body.ActionKey),
			zap.String("executionContextId", body.Context.ID),
			zap.String("output", output),
		)
		return client.Steps.Resolve(ctx, body.Context, output, nil)
	}))

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8090"
	}
	logger.Info("integration listening", zap.String("addr", addr))
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
