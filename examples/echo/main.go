// Command echo is a minimal Aheron integration backend. It exposes one endpoint
// that receives a signed integrationAction request from the platform, verifies
// it, and immediately resolves the parked step through its first declared output.
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
	"log"
	"net/http"
	"os"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integration"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/integration/zaplog"

	"go.uber.org/zap"
)

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

	// One endpoint per block type. The handler receives a verified, typed
	// request; it resolves the step through the block's first declared output.
	handler := verifier.HandleAction(func(ctx context.Context, req integration.ActionRequest) error {
		output := "ok"
		if len(req.Payload.Settings.Outputs) > 0 {
			output = req.Payload.Settings.Outputs[0]
		}
		logger.Info("action received",
			zap.String("subject", req.Payload.Subject.ID),
			zap.String("project", req.Payload.Project.ID),
			zap.String("output", output),
		)
		return client.Steps.Resolve(ctx, req.Resolve(output, nil))
	})

	http.Handle("/blocks/echo", handler)

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8090"
	}
	logger.Info("integration listening", zap.String("addr", addr))
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
