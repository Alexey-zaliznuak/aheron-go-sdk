package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/httpclient"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/sign"
)

const resolvePath = "/api/integrations/resolve"

// StepsClient resolves parked integrationAction steps. All calls are signed with
// the integration's private key.
type StepsClient struct {
	http   *httpclient.Client
	id     string
	signer *sign.Signer
}

// ResolveParams identifies the parked context and the chosen outcome. Correlation
// is by (ExecutionContextID, ContextVersion), which the platform gave the
// integration in the inbound request's resolve block — use ActionRequest.Resolve
// to build these directly from an inbound request. Output must be one of the
// block's declared outputs. Variables optionally persists values: subject
// variables by bare key, project variables under a "project." prefix.
//
// URL is the absolute resolve endpoint. When set (ActionRequest.Resolve fills it
// from the inbound payload's resolve.url), the call targets it directly, which
// is deployment-agnostic. When empty, the call falls back to the configured
// ExecutionURL + the standard resolve path.
type ResolveParams struct {
	ExecutionContextID string
	ContextVersion     int64
	Output             string
	Variables          map[string]any
	URL                string
}

type resolveBody struct {
	ExecutionContextID string         `json:"executionContextId"`
	ContextVersion     int64          `json:"contextVersion"`
	Output             string         `json:"output"`
	Variables          map[string]any `json:"variables,omitempty"`
}

// Resolve advances a parked integrationAction context through the chosen output.
// The platform verifies the signature, ownership and version, then returns 202
// and applies the result asynchronously. Redelivery is safe, so this call is
// retried on transient failures.
func (c *StepsClient) Resolve(ctx context.Context, p ResolveParams) error {
	if p.ExecutionContextID == "" {
		return fmt.Errorf("integration: Resolve requires ExecutionContextID")
	}
	if p.Output == "" {
		return fmt.Errorf("integration: Resolve requires Output")
	}

	body, err := json.Marshal(resolveBody{
		ExecutionContextID: p.ExecutionContextID,
		ContextVersion:     p.ContextVersion,
		Output:             p.Output,
		Variables:          p.Variables,
	})
	if err != nil {
		return fmt.Errorf("integration: marshal resolve: %w", err)
	}

	path := resolvePath
	if p.URL != "" {
		// Absolute URL from the inbound payload; resty uses it verbatim.
		path = p.URL
	}
	req, err := buildSignedRequest(c.signer, c.id, http.MethodPost, path, nil, body, true)
	if err != nil {
		return err
	}
	_, err = c.http.Do(ctx, req)
	return err
}
