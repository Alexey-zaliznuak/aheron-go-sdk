package schemetransfer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxProvisionBytes = 64 << 10

// ProvisionRequest is an internal, target-project-only command. A create may
// reuse a compatible exact key that appeared concurrently; it never overwrites
// an existing definition or value. Reuse additionally pins the selected ID.
// Source values and integration-owned definitions do not belong in this API.
type ProvisionRequest struct {
	Kind        string          `json:"kind"`
	Action      string          `json:"action"`
	ID          string          `json:"id,omitempty"`
	Key         string          `json:"key"`
	ValueType   string          `json:"valueType,omitempty"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Value       json.RawMessage `json:"value,omitempty"`
}

// ProvisionResult is also the durable receipt. It never contains CRM values.
type ProvisionResult struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Key       string `json:"key"`
	ValueType string `json:"valueType,omitempty"`
	Created   bool   `json:"created"`
}

var provisionUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var provisionVariableKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,127}$`)
var provisionRef = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func ValidProvisionAddress(projectID, importID, resourceRef string) bool {
	return provisionUUID.MatchString(projectID) && provisionUUID.MatchString(importID) && provisionRef.MatchString(resourceRef)
}
func (r ProvisionRequest) Validate() error {
	invalid := errors.New("invalid transfer resource request")
	if !utf8.ValidString(r.Name) || !utf8.ValidString(r.Description) || len(r.Name) > 512 || len(r.Description) > 8192 {
		return invalid
	}
	switch r.Kind {
	case "tag":
		if !provisionRef.MatchString(r.Key) || r.ValueType != "" || len(r.Value) > 0 {
			return invalid
		}
	case "subjectVariable", "projectVariable":
		if !provisionVariableKey.MatchString(r.Key) {
			return invalid
		}
		switch r.ValueType {
		case "string", "number", "boolean", "datetime", "array", "object":
		default:
			return invalid
		}
	default:
		return invalid
	}
	switch r.Action {
	case "reuse":
		if !provisionUUID.MatchString(r.ID) || r.Name != "" || r.Description != "" || len(r.Value) > 0 {
			return invalid
		}
	case "create":
		if r.ID != "" || strings.TrimSpace(r.Name) == "" {
			return invalid
		}
		if r.Kind == "projectVariable" {
			if len(r.Value) == 0 || len(r.Value) > MaxProvisionBytes || !validProvisionValue(r.Value, r.ValueType) {
				return invalid
			}
		} else if len(r.Value) > 0 {
			return invalid
		}
	default:
		return invalid
	}
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > MaxProvisionBytes {
		return invalid
	}
	return nil
}
func validProvisionValue(raw json.RawMessage, kind string) bool {
	value, err := DecodeJSON(raw)
	if err != nil {
		return false
	}
	// An explicit null is a supplied value, distinct from an omitted input.
	if value == nil {
		return true
	}
	switch kind {
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		_, ok := value.(json.Number)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "datetime":
		v, ok := value.(string)
		if !ok {
			return false
		}
		_, err := time.Parse(time.RFC3339Nano, v)
		return err == nil
	}
	return false
}

// Digest ignores JSON object member ordering while preserving number tokens.
func (r ProvisionRequest) Digest() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	value, err := DecodeJSON(raw)
	if err != nil {
		return "", err
	}
	raw, err = json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func (r ProvisionResult) Validate(request ProvisionRequest) error {
	if !provisionUUID.MatchString(r.ID) || r.Kind != request.Kind || r.Key != request.Key || r.ValueType != request.ValueType || request.Action == "reuse" && (!strings.EqualFold(r.ID, request.ID) || r.Created) {
		return errors.New("invalid transfer resource result")
	}
	return nil
}
