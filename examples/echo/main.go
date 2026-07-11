// Command echo is a minimal Aheron integration backend. It exposes two signed
// endpoints:
//
//   - /install     receives {projectId, projectApiKey} once, when the
//     integration is installed into a project, and stores the key.
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
//	JWKS_URL         — platform integration JWKS, e.g.
//	                   https://aheron.pro/.well-known/aheron-integration-jwks.json
//	ADDR             — listen address (default :8090)
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integration"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/integration/zaplog"

	"go.uber.org/zap"
)

// apiKeyStore holds the project API keys the platform delivered on install. A
// real integration would persist these; here an in-memory map is enough.
type apiKeyStore struct {
	mu   sync.RWMutex
	keys map[string]string // projectId -> projectApiKey
}

func (s *apiKeyStore) set(projectID, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keys == nil {
		s.keys = map[string]string{}
	}
	s.keys[projectID] = key
}

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

	client, err := integration.New(integration.Config{
		IntegrationID: os.Getenv("INTEGRATION_ID"),
		PrivateKey:    os.Getenv("INTEGRATION_KEY"),
		Logger:        zaplog.New(logger),
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

	store := &apiKeyStore{}

	// Install: the platform delivers the project API key once, on install.
	http.Handle("/install", verifier.HandleInstall(func(_ context.Context, req integration.InstallRequest) error {
		store.set(req.ProjectID, req.ProjectAPIKey)
		logger.Info("integration installed",
			zap.String("project", req.ProjectID),
		)
		return nil
	}))

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
