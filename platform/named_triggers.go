package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

type NamedTriggersClient struct {
	client *Client
}

// NamedTriggerActivation identifies a named trigger and its subject. A nil
// payload omits the field. Use json.RawMessage("null") to send JSON null.
type NamedTriggerActivation struct {
	ActivationKey string          `json:"activationKey"`
	SubjectID     string          `json:"subjectId"`
	Payload       json.RawMessage `json:"payload,omitempty"`
}

type NamedTriggerActivationResult struct {
	ExecutionContextIDs []string `json:"executionContextIds"`
}

// NewNamedTriggers binds the project credential only to named activation.
func NewNamedTriggers(cfg Config, projectAPIKey string) (*NamedTriggersClient, error) {
	if cfg.TokenProvider != nil || projectAPIKey == "" || strings.ContainsAny(projectAPIKey, " \t\r\n") {
		return nil, ErrConfig
	}
	client, err := newClient(cfg)
	if err != nil {
		return nil, err
	}
	client.projectAPIKey = projectAPIKey
	return &NamedTriggersClient{client: client}, nil
}

// Activate queues the trigger. This call does not retry or wait for execution.
func (c *NamedTriggersClient) Activate(ctx context.Context, input NamedTriggerActivation) (NamedTriggerActivationResult, error) {
	var result NamedTriggerActivationResult
	err := c.client.requestJSON(ctx, "activate named trigger", http.MethodPost, "/execution/triggers/activate", nil, input, &result)
	return result, err
}
