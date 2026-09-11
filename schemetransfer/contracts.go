// Package schemetransfer defines the versioned, declarative scheme transfer
// contract. It never fetches resources or reads CRM values. The platform and
// integrations remain responsible for ownership and domain validation.
package schemetransfer

import "encoding/json"

const Version = 1

// CallbackVersion is negotiated by the pinned block manifest, never guessed
// from the shape of its settings. Version 1 remains supported for old catalogs.
const CallbackVersion = 2

func CallbackCopyRules() *CopyRules { return &CopyRules{Version: CallbackVersion, Mode: "callback"} }

func (r CopyRules) UsesCallbackPlan() bool {
	return r.Version == CallbackVersion && r.Mode == "callback"
}

func (r CopyRules) RequiresIntegrationValidation() bool {
	return r.UsesCallbackPlan() || (r.Validation != nil && r.Validation.Mode == "integration")
}

type CopyRules struct {
	Version           int                `json:"version"`
	Mode              string             `json:"mode"`
	Settings          *Rule              `json:"settings,omitempty"`
	ImplicitResources []ImplicitResource `json:"implicitResources,omitempty"`
	Validation        *Validation        `json:"validation,omitempty"`
	Reason            string             `json:"reason,omitempty"`
	Notes             string             `json:"notes,omitempty"`
}

type Validation struct {
	Mode    string `json:"mode"`
	Message string `json:"message,omitempty"`
}

// Rule is a tagged union. Only the fields defined for Kind are accepted by the
// schema. Fields are optional unless Required or an explicit empty policy says
// otherwise. Nullable permits JSON null in containers and template fields.
type Rule struct {
	Kind             string            `json:"kind"`
	Required         bool              `json:"required,omitempty"`
	Nullable         bool              `json:"nullable,omitempty"`
	UnknownFields    string            `json:"unknownFields,omitempty"`
	Fields           map[string]Rule   `json:"fields,omitempty"`
	Items            *Rule             `json:"items,omitempty"`
	Keys             string            `json:"keys,omitempty"`
	Values           *Rule             `json:"values,omitempty"`
	ValueType        Types             `json:"valueType,omitempty"`
	Dialect          string            `json:"dialect,omitempty"`
	Resource         *ResourceSelector `json:"resource,omitempty"`
	ReadAs           string            `json:"readAs,omitempty"`
	WriteAs          string            `json:"writeAs,omitempty"`
	Access           string            `json:"access,omitempty"`
	Empty            string            `json:"empty,omitempty"`
	DefaultReference *DefaultReference `json:"defaultReference,omitempty"`
	Constraints      json.RawMessage   `json:"constraints,omitempty"`
	Reason           string            `json:"reason,omitempty"`
}

func (r Rule) MarshalJSON() ([]byte, error) {
	type plain Rule
	b, err := json.Marshal(plain(r))
	if err != nil {
		return nil, err
	}
	if r.Kind != "object" || len(r.Fields) > 0 {
		return b, nil
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	out["fields"] = json.RawMessage(`{}`)
	return json.Marshal(out)
}

// Types encodes a single type as a string, or a union as an array of strings.
type Types []string

func (t Types) MarshalJSON() ([]byte, error) {
	if len(t) == 1 {
		return json.Marshal(t[0])
	}
	return json.Marshal([]string(t))
}
func (t *Types) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*t = Types{s}
		return nil
	}
	var a []string
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*t = a
	return nil
}

type ResourceSelector struct {
	Kind      string `json:"kind"`
	SourceKey string `json:"sourceKey,omitempty"`
	Owner     string `json:"owner,omitempty"`
	ValueType string `json:"valueType,omitempty"`
}
type DefaultReference struct {
	Owner string `json:"owner"`
	Key   string `json:"key"`
}
type ImplicitResource struct {
	Kind      string `json:"kind"`
	Owner     string `json:"owner"`
	Key       string `json:"key"`
	ValueType string `json:"valueType"`
	Access    string `json:"access"`
}

type ResourceSource struct {
	ValueType         string                     `json:"valueType"`
	IdentityScope     string                     `json:"identityScope"`
	Supports          []string                   `json:"supports"`
	Parameters        map[string]SourceParameter `json:"parameters,omitempty"`
	ConstraintsSchema json.RawMessage            `json:"constraintsSchema,omitempty"`
	LegacyAdapter     *LegacyAdapter             `json:"legacyAdapter,omitempty"`
}
type SourceParameter struct {
	Required  bool   `json:"required"`
	SourceKey string `json:"sourceKey"`
}
type LegacyAdapter struct {
	VariableKey string `json:"variableKey"`
}

type LookupRequest struct {
	Mode               string                     `json:"mode"`
	ProjectID          string                     `json:"projectId"`
	IntegrationVersion int                        `json:"integrationVersion"`
	SourceKey          string                     `json:"sourceKey"`
	Query              string                     `json:"query,omitempty"`
	Limit              int                        `json:"limit,omitempty"`
	Cursor             string                     `json:"cursor,omitempty"`
	IDs                []string                   `json:"ids,omitempty"`
	Values             []json.RawMessage          `json:"values,omitempty"`
	Parameters         map[string]json.RawMessage `json:"parameters,omitempty"`
	Constraints        json.RawMessage            `json:"constraints,omitempty"`
}
type ResourceItem struct {
	ID         string          `json:"id"`
	Value      json.RawMessage `json:"value"`
	Title      string          `json:"title"`
	Selectable bool            `json:"selectable"`
	Reason     string          `json:"reason,omitempty"`
}
type LookupResponse struct {
	Items      []ResourceItem `json:"items"`
	NextCursor string         `json:"nextCursor,omitempty"`
}
type IdentifyMatch struct {
	InputIndex int           `json:"inputIndex"`
	Status     string        `json:"status"`
	Item       *ResourceItem `json:"item,omitempty"`
}
type IdentifyResponse struct {
	Matches []IdentifyMatch `json:"matches"`
}

// Callback requests pin the catalog version. A receiver must reject unsupported
// versions; it must never silently interpret settings as its current version.
type PrepareCopyRequest struct {
	ProtocolVersion    int             `json:"protocolVersion"`
	ProjectID          string          `json:"projectId"`
	SchemeID           string          `json:"schemeId"`
	IntegrationVersion int             `json:"integrationVersion"`
	BlockKey           string          `json:"blockKey"`
	Settings           json.RawMessage `json:"settings"`
}
type PrepareCopyResponse struct {
	ProtocolVersion int             `json:"protocolVersion,omitempty"`
	Settings        json.RawMessage `json:"settings"`
	Plan            *CopyPlan       `json:"plan,omitempty"`
	Issues          []Issue         `json:"issues"`
}
type ValidateCopySettingsRequest struct {
	ProtocolVersion    int             `json:"protocolVersion"`
	ProjectID          string          `json:"projectId"`
	IntegrationVersion int             `json:"integrationVersion"`
	BlockKey           string          `json:"blockKey"`
	Settings           json.RawMessage `json:"settings"`
}
type ValidationResult struct {
	Status string  `json:"status"`
	Issues []Issue `json:"issues"`
}
type Issue struct {
	Severity    string `json:"severity"`
	Code        string `json:"code"`
	Path        string `json:"path"`
	Message     string `json:"message"`
	StepRef     string `json:"stepRef,omitempty"`
	ResourceRef string `json:"resourceRef,omitempty"`
}

// Binding refers to a concrete JSON Pointer in one step's sanitized settings.
// Parts is a JSON union of literal strings and TextReference objects.
type Binding struct {
	Kind        string            `json:"kind"`
	Path        string            `json:"path"`
	ResourceRef string            `json:"resourceRef,omitempty"`
	WriteAs     string            `json:"writeAs,omitempty"`
	Parts       []json.RawMessage `json:"parts,omitempty"`
}
type TextReference struct {
	ResourceRef string   `json:"resourceRef"`
	AccessPath  []string `json:"accessPath,omitempty"`
}

// ImportPlan is private to an authorized target project. The digest and revision
// bind all selections; it is not included in a published TemplateDocument.
type ImportPlan struct {
	FormatVersion      int                        `json:"formatVersion"`
	ImportID           string                     `json:"importId"`
	TemplateRevisionID string                     `json:"templateRevisionId"`
	PlanRevision       int64                      `json:"planRevision"`
	Digest             string                     `json:"digest"`
	ExpiresAt          string                     `json:"expiresAt"`
	Target             ImportTarget               `json:"target"`
	AutoPolicy         string                     `json:"autoPolicy"`
	Status             string                     `json:"status"`
	ResourceMappings   map[string]ResourceMapping `json:"resourceMappings"`
}
type ImportTarget struct {
	ProjectID string `json:"projectId"`
	Mode      string `json:"mode"`
	Name      string `json:"name"`
}
type ResourceMapping struct {
	// defer leaves an integration resource unconfigured in an inactive copy.
	// It carries neither a source identity nor a target identity. The importer
	// must retain setup requirements and check them before activation.
	Action     string         `json:"action"`
	ID         string         `json:"id,omitempty"`
	Definition *NewDefinition `json:"definition,omitempty"`
}
type NewDefinition struct {
	Color       string          `json:"color,omitempty"`
	Description string          `json:"description,omitempty"`
	Value       json.RawMessage `json:"value,omitempty"`
	Name        string          `json:"name"`
	Key         string          `json:"key"`
	ValueType   string          `json:"valueType,omitempty"`
}
