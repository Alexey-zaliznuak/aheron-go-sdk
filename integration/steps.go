package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/httpclient"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/internal/sign"
)

// resolvePath is relative to the configured ExecutionURL, which already carries
// the gateway's "/api/execution" prefix.
const resolvePath = "/integrations/resolve"

const maxResolveIdempotencyKeyBytes = 256

// StepsClient resolves parked integrationAction steps. All calls are signed with
// the integration's private key.
type StepsClient struct {
	http   *httpclient.Client
	id     string
	signer *sign.Signer
}

type resolveBody struct {
	ExecutionContextID string         `json:"executionContextId"`
	ContextVersion     int64          `json:"contextVersion"`
	Output             string         `json:"output"`
	Variables          map[string]any `json:"variables,omitempty"`
	Mode               string         `json:"mode,omitempty"`
	StepID             string         `json:"stepId,omitempty"`
	IdempotencyKey     string         `json:"idempotencyKey,omitempty"`
}

// ResolveOptions controls optional delivery guarantees for ResolveWithOptions
// and ReactivateWithOptions. IdempotencyKey is scoped by the platform to this
// integration and execution context. Reusing it with the same request replays
// the original accepted outcome; reusing it with different request data is a
// conflict.
type ResolveOptions struct {
	IdempotencyKey string
}

// Resolve advances a parked integrationAction context through the chosen output.
// It correlates by the ExecutionContext (id + version) the platform sent in the
// action request body, so pass the ExecutionContext decoded from that body.
// output must be one of the block's declared outputs; variables optionally
// persists values (subject variables by bare key, project variables under a
// "project." prefix) and may be nil.
//
// The platform verifies the signature, ownership and version, then returns 202
// and applies the result asynchronously. Redelivery is safe, so this call is
// retried on transient failures. The request targets the configured ExecutionURL
// + the standard resolve path.
func (c *StepsClient) Resolve(ctx context.Context, ec ExecutionContext, output string, variables map[string]any) error {
	return c.ResolveWithOptions(ctx, ec, output, variables, ResolveOptions{})
}

// ResolveWithOptions is Resolve with an optional durable idempotency key. Use a
// stable key from the caller's durable interaction/job identity; never generate
// a fresh key for each retry. The current keyed protocol does not support
// variables: pass nil or an empty map so the platform can commit every protected
// side effect in one transaction.
func (c *StepsClient) ResolveWithOptions(ctx context.Context, ec ExecutionContext, output string, variables map[string]any, options ResolveOptions) error {
	if ec.ID == "" {
		return fmt.Errorf("integration: Resolve requires ExecutionContext.ID")
	}
	if output == "" {
		return fmt.Errorf("integration: Resolve requires output")
	}
	if err := validateResolveOptions("Resolve", variables, options); err != nil {
		return err
	}

	body, err := json.Marshal(resolveBody{
		ExecutionContextID: ec.ID,
		ContextVersion:     ec.Version,
		Output:             output,
		Variables:          variables,
		IdempotencyKey:     options.IdempotencyKey,
	})
	if err != nil {
		return fmt.Errorf("integration: marshal resolve: %w", err)
	}

	req, err := buildSignedRequest(c.signer, c.id, http.MethodPost, resolvePath, nil, body, true)
	if err != nil {
		return err
	}
	_, err = c.http.Do(ctx, req)
	return err
}

// Reactivate re-routes the subject through the step's chosen output even after
// the context has moved on — "the user pressed an old button again" semantics.
// Unlike Resolve there is no correlation: the platform ignores the context's
// current status/version, checks only that the integration owns ec.StepID, and
// overwrites the subject's position on the output edge's branch (like a trigger
// activation). Pass the ExecutionContext the platform sent when the step
// originally ran; ID, StepID and output are required. variables optionally
// persists values, as in Resolve.
//
// The platform returns 202 and applies the re-route asynchronously. An output
// with no wired edge is a no-op on the platform side.
func (c *StepsClient) Reactivate(ctx context.Context, ec ExecutionContext, output string, variables map[string]any) error {
	return c.ReactivateWithOptions(ctx, ec, output, variables, ResolveOptions{})
}

// ReactivateWithOptions is Reactivate with the same optional durable
// idempotency semantics as ResolveWithOptions.
func (c *StepsClient) ReactivateWithOptions(ctx context.Context, ec ExecutionContext, output string, variables map[string]any, options ResolveOptions) error {
	if ec.ID == "" {
		return fmt.Errorf("integration: Reactivate requires ExecutionContext.ID")
	}
	if ec.StepID == "" {
		return fmt.Errorf("integration: Reactivate requires ExecutionContext.StepID")
	}
	if output == "" {
		return fmt.Errorf("integration: Reactivate requires output")
	}
	if err := validateResolveOptions("Reactivate", variables, options); err != nil {
		return err
	}

	body, err := json.Marshal(resolveBody{
		ExecutionContextID: ec.ID,
		ContextVersion:     ec.Version,
		Output:             output,
		Variables:          variables,
		Mode:               "reactivate",
		StepID:             ec.StepID,
		IdempotencyKey:     options.IdempotencyKey,
	})
	if err != nil {
		return fmt.Errorf("integration: marshal reactivate: %w", err)
	}

	req, err := buildSignedRequest(c.signer, c.id, http.MethodPost, resolvePath, nil, body, true)
	if err != nil {
		return err
	}
	_, err = c.http.Do(ctx, req)
	return err
}

func validateResolveOptions(operation string, variables map[string]any, options ResolveOptions) error {
	if options.IdempotencyKey == "" {
		return nil
	}
	if strings.TrimSpace(options.IdempotencyKey) == "" {
		return fmt.Errorf("integration: %s idempotency key must not be blank", operation)
	}
	if len(options.IdempotencyKey) > maxResolveIdempotencyKeyBytes {
		return fmt.Errorf("integration: %s idempotency key must be at most %d bytes", operation, maxResolveIdempotencyKeyBytes)
	}
	if len(variables) != 0 {
		return fmt.Errorf("integration: %s variables are not supported with an idempotency key", operation)
	}
	return nil
}
