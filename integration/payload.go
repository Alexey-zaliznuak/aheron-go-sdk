package integration

import (
	"encoding/json"
	"fmt"
)

// TypeAction is the envelope discriminator the platform sends for an
// integrationAction step (INTEGRATIONS.MD §4.1).
const TypeAction = "block.action"

// Envelope is the outer shape of every inbound platform request: a type
// discriminator and a type-specific payload kept raw for typed decoding.
type Envelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// Ref is a lightweight {id} reference to a platform entity.
type Ref struct {
	ID string `json:"id"`
}

// ResolveInfo is the correlation block the platform embeds in an inbound action.
// The integration echoes ExecutionContextID and ContextVersion back when it
// resolves the step; URL is the platform resolve endpoint and Outputs lists the
// block's declared outputs.
type ResolveInfo struct {
	URL                string   `json:"url"`
	ExecutionContextID string   `json:"executionContextId"`
	ContextVersion     int64    `json:"contextVersion"`
	Outputs            []string `json:"outputs"`
}

// ActionSettings carries the block instance settings the platform sends; for an
// integrationAction that is the declared set of outputs.
type ActionSettings struct {
	Outputs []string `json:"outputs"`
}

// ActionPayload is the decoded payload of a "block.action" request.
type ActionPayload struct {
	Settings ActionSettings  `json:"settings"`
	Input    *string         `json:"input,omitempty"`
	Subject  Ref             `json:"subject"`
	Project  Ref             `json:"project"`
	Vars     json.RawMessage `json:"vars"`
	Tags     []string        `json:"tags"`
	Resolve  ResolveInfo     `json:"resolve"`
}

// ActionRequest is a verified, decoded inbound integrationAction request. KeyID
// is the platform signing key id that authenticated the request (from the
// X-Aheron-Key-Id header; empty when the platform runs a single unlabeled key).
type ActionRequest struct {
	Type    string
	KeyID   string
	Payload ActionPayload
}

// DecodeVars unmarshals the block's vars payload into v (typically a struct or
// map). It is a no-op returning nil when vars is absent.
func (a ActionRequest) DecodeVars(v any) error {
	if len(a.Payload.Vars) == 0 {
		return nil
	}
	if err := json.Unmarshal(a.Payload.Vars, v); err != nil {
		return fmt.Errorf("integration: decode vars: %w", err)
	}
	return nil
}

// Resolve builds the ResolveParams that resolves THIS action through output,
// carrying the correlation (execution context id and version) from the inbound
// request. Pass it straight to Client.Steps.Resolve. variables may be nil.
func (a ActionRequest) Resolve(output string, variables map[string]any) ResolveParams {
	return ResolveParams{
		ExecutionContextID: a.Payload.Resolve.ExecutionContextID,
		ContextVersion:     a.Payload.Resolve.ContextVersion,
		Output:             output,
		Variables:          variables,
		URL:                a.Payload.Resolve.URL,
	}
}
