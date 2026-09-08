package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/variables"
)

// Vars is the platform's {{vars}} envelope. Variables are grouped by owner;
// projectId and the optional runtime namespaces are supplied by execution.
// Bare references always select Subject, never Project or Integrations.
type Vars struct {
	ProjectID        string                    `json:"projectId,omitempty"`
	Project          map[string]any            `json:"project"`
	Subject          map[string]any            `json:"subject"`
	Integrations     map[string]map[string]any `json:"integrations"`
	Context          map[string]any            `json:"context,omitempty"`
	IntegrationState map[string]any            `json:"integrationState,omitempty"`
}

// ParseVars decodes the platform envelope without rounding JSON numbers through
// float64. Absent/null payloads represent an empty scope. Malformed or flat
// payloads are errors. No partially decoded scope is returned on error.
func ParseVars(raw json.RawMessage) (Vars, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return Vars{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	var result Vars
	if err := decoder.Decode(&result); err != nil {
		return Vars{}, fmt.Errorf("integration: invalid vars envelope: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Vars{}, fmt.Errorf("integration: vars envelope must contain one JSON value")
	}
	return result, nil
}

// Values exposes the shared IO-free scope. Maps are borrowed and must not be
// mutated concurrently with lookups.
func (v Vars) Values() variables.Values {
	return variables.Values{
		Subject: v.Subject, Project: v.Project, Integrations: v.Integrations,
		ProjectID: v.ProjectID, Context: v.Context, IntegrationState: v.IntegrationState,
	}
}

// Lookup resolves one aheronVarsV1 reference without braces. For callers that
// need to distinguish invalid syntax from missing values, parse it using
// variables.ParseReferenceV1 and call Values().Lookup.
func (v Vars) Lookup(key string) (any, bool) {
	ref, err := variables.ParseReferenceV1(key)
	if err != nil {
		return nil, false
	}
	value, found, err := v.Values().Lookup(ref)
	return value, found && err == nil
}

// Substitute renders settings using aheronVarsV1. Missing references become
// empty text; malformed templates must be reported to the operator. Exporters
// must resolve definitions using Template.Parts, not runtime values.
func (v Vars) Substitute(template string) (string, error) {
	return v.SubstituteFunc(template, nil)
}

// SubstituteFunc escapes each substituted value once, leaving literal text
// unchanged. Substituted strings are never reparsed as variable expressions.
func (v Vars) SubstituteFunc(template string, escape func(string) string) (string, error) {
	parsed, err := variables.ParseV1(template)
	if err != nil {
		return "", err
	}
	return parsed.Render(v.Values().Lookup, variables.RenderOptions{Missing: variables.MissingEmpty, Escape: escape})
}

// VarString retains the convenience formatter for already validated values.
// Call variables.Format where encoding failures need to be reported.
func VarString(value any) string {
	text, _ := variables.Format(value)
	return text
}
