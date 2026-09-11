package schemetransfer

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/variables"
)

// CopyPlan describes occurrences in ONE concrete, prepared settings document.
// It is private callback data, not a public template or a static settings schema.
// The platform identifies and groups resources across blocks before publication.
type CopyPlan struct {
	References        []CopyReference    `json:"references"`
	Templates         []string           `json:"templates"`
	ImplicitResources []ImplicitResource `json:"implicitResources,omitempty"`
}

// CopyReference moves the source value out of settings, leaving a JSON null at
// Path. Values must never be exposed as public template resource identities.
type CopyReference struct {
	Path        string           `json:"path"`
	Resource    ResourceSelector `json:"resource"`
	Value       json.RawMessage  `json:"value"`
	ReadAs      string           `json:"readAs"`
	WriteAs     string           `json:"writeAs"`
	Access      string           `json:"access,omitempty"`
	Constraints json.RawMessage  `json:"constraints,omitempty"`
}

// Rule returns only this occurrence's semantics. There is no object/array schema
// in a callback plan; unmarked prepared values are literal integration output.
func (r CopyReference) Rule() Rule {
	s := r.Resource
	return Rule{Kind: "reference", Resource: &s, ReadAs: r.ReadAs, WriteAs: r.WriteAs, Access: r.Access, Constraints: r.Constraints, Empty: "requireBinding"}
}

func (r PrepareCopyResponse) ValidateFor(req PrepareCopyRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if req.ProtocolVersion != r.ProtocolVersion {
		return invalid("copyProtocolMismatch", "/protocolVersion")
	}
	return nil
}

// HydratedSettings is private input to the platform's existing resource parser.
// It restores source references without changing array length or value types.
// It is never safe to publish the return value directly.
func (r PrepareCopyResponse) HydratedSettings() (json.RawMessage, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	v, _ := DecodeJSON(r.Settings)
	for _, ref := range r.Plan.References {
		value, _ := DecodeJSON(ref.Value)
		if _, err := copyPointer(v, ref.Path, true, value); err != nil {
			return nil, err
		}
	}
	return json.Marshal(v)
}

func (r PrepareCopyResponse) validatePlan() error {
	if r.Plan == nil {
		return invalid("copyPlanRequired", "/plan")
	}
	v, err := DecodeJSON(r.Settings)
	if err != nil {
		return err
	}
	paths := map[string]bool{}
	checkPath := func(path string) (any, error) {
		for p := range paths {
			if p == path || strings.HasPrefix(p, path+"/") || strings.HasPrefix(path, p+"/") {
				return nil, invalid("overlappingCopyPaths", path)
			}
		}
		paths[path] = true
		return copyPointer(v, path, false, nil)
	}
	for _, ref := range r.Plan.References {
		value, err := checkPath(ref.Path)
		if err != nil {
			return err
		}
		if value != nil {
			return invalid("copyPlaceholderRequired", ref.Path)
		}
		rule := ref.Rule()
		if err := validateRuleSemantics(&rule, ref.Path); err != nil {
			return err
		}
		source, err := DecodeJSON(ref.Value)
		if err != nil {
			return err
		}
		if source == nil {
			return invalid("copySourceValueRequired", ref.Path)
		}
		if ref.Resource.Kind != "integrationResource" {
			if s, ok := source.(string); !ok || strings.TrimSpace(s) == "" {
				return invalid("copySourceValueRequired", ref.Path)
			}
		}
	}
	for _, path := range r.Plan.Templates {
		value, err := checkPath(path)
		if err != nil {
			return err
		}
		s, ok := value.(string)
		if !ok {
			return invalid("copyTemplateMustBeString", path)
		}
		if _, err := variables.ParseV1(s); err != nil {
			return invalid("invalidTemplate", path)
		}
	}
	return nil
}

// ValidateSources checks dynamic references against the pinned source catalog,
// including constraints. Resource ownership is still checked by identify/resolve.
func (p CopyPlan) ValidateSources(sources map[string]ResourceSource) error {
	schemas, err := validateResourceSources(sources)
	if err != nil {
		return err
	}
	for _, ref := range p.References {
		if ref.Resource.Kind != "integrationResource" {
			continue
		}
		if _, ok := sources[ref.Resource.SourceKey]; !ok {
			return invalid("unknownSource", ref.Path)
		}
		if len(ref.Constraints) == 0 {
			continue
		}
		schema := schemas[ref.Resource.SourceKey]
		if schema == nil {
			return invalid("constraintsSchemaRequired", ref.Path)
		}
		value, err := DecodeJSON(ref.Constraints)
		if err != nil {
			return err
		}
		if err := schema.Validate(value); err != nil {
			return invalid("invalidResourceConstraints", ref.Path)
		}
	}
	return nil
}

// copyPointer accepts only existing, canonical JSON Pointers. It cannot append
// to arrays, create paths or traverse a scalar. This prevents plan shape changes.
func copyPointer(root any, path string, replace bool, replacement any) (any, error) {
	if !strings.HasPrefix(path, "/") {
		return nil, invalid("invalidCopyPath", path)
	}
	parts := strings.Split(path[1:], "/")
	current := root
	for i, encoded := range parts {
		key := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		if Pointer("", key) != "/"+encoded {
			return nil, invalid("invalidCopyPath", path)
		}
		last := i == len(parts)-1
		switch v := current.(type) {
		case map[string]any:
			value, ok := v[key]
			if !ok {
				return nil, invalid("missingCopyPath", path)
			}
			if last {
				if replace {
					v[key] = replacement
				}
				return value, nil
			}
			current = value
		case []any:
			n, err := strconv.Atoi(key)
			if err != nil || n < 0 || n >= len(v) || strconv.Itoa(n) != key {
				return nil, invalid("invalidCopyPath", path)
			}
			value := v[n]
			if last {
				if replace {
					v[n] = replacement
				}
				return value, nil
			}
			current = value
		default:
			return nil, invalid("invalidCopyPath", path)
		}
	}
	return nil, invalid("invalidCopyPath", path)
}

// CopyBuilder starts from explicit, domain-validated output (usually a typed
// settings DTO). Do not pass raw request settings without auditing unknown fields
// and removing credentials, caches and identities that cannot be transferred.
type CopyBuilder struct {
	settings any
	plan     CopyPlan
	err      error
}

func NewCopyBuilder(settings any) *CopyBuilder {
	b := &CopyBuilder{plan: CopyPlan{References: []CopyReference{}, Templates: []string{}}}
	raw, err := json.Marshal(settings)
	if err == nil {
		b.settings, err = DecodeJSON(raw)
	}
	if _, ok := b.settings.(map[string]any); err == nil && !ok {
		err = invalid("settingsMustBeObject", "")
	}
	b.err = err
	return b
}

func (b *CopyBuilder) Reference(path string, selector ResourceSelector, readAs, writeAs, access string) *CopyBuilder {
	if b.err != nil {
		return b
	}
	v, err := copyPointer(b.settings, path, true, nil)
	if err != nil {
		b.err = err
		return b
	}
	raw, err := json.Marshal(v)
	if err != nil {
		b.err = err
		return b
	}
	b.plan.References = append(b.plan.References, CopyReference{Path: path, Resource: selector, Value: raw, ReadAs: readAs, WriteAs: writeAs, Access: access})
	return b
}

func (b *CopyBuilder) Resource(path, sourceKey string) *CopyBuilder {
	return b.Reference(path, ResourceSelector{Kind: "integrationResource", SourceKey: sourceKey}, "value", "value", "")
}

func (b *CopyBuilder) SubjectVariable(path, valueType, access string) *CopyBuilder {
	return b.Reference(path, ResourceSelector{Kind: "subjectVariable", Owner: "projectOrThisIntegration", ValueType: valueType}, "key", "key", access)
}

func (b *CopyBuilder) ResourceList(path, sourceKey string) *CopyBuilder {
	if b.err != nil {
		return b
	}
	v, err := copyPointer(b.settings, path, false, nil)
	if err != nil {
		b.err = err
		return b
	}
	a, ok := v.([]any)
	if !ok {
		b.err = invalid("copyListRequired", path)
		return b
	}
	for i := range a {
		b.Resource(Pointer(path, strconv.Itoa(i)), sourceKey)
	}
	return b
}

func (b *CopyBuilder) Template(path string) *CopyBuilder {
	b.plan.Templates = append(b.plan.Templates, path)
	return b
}

func (b *CopyBuilder) Require(resource ImplicitResource) *CopyBuilder {
	b.plan.ImplicitResources = append(b.plan.ImplicitResources, resource)
	return b
}

func (b *CopyBuilder) Build() (PrepareCopyResponse, error) {
	if b.err != nil {
		return PrepareCopyResponse{}, b.err
	}
	raw, err := json.Marshal(b.settings)
	if err != nil {
		return PrepareCopyResponse{}, err
	}
	// Detach the result so later builder calls cannot mutate a completed plan.
	encoded, err := json.Marshal(PrepareCopyResponse{ProtocolVersion: CallbackVersion, Settings: raw, Plan: &b.plan, Issues: []Issue{}})
	if err != nil {
		return PrepareCopyResponse{}, err
	}
	var result PrepareCopyResponse
	if err = json.Unmarshal(encoded, &result); err != nil {
		return PrepareCopyResponse{}, err
	}
	if err = result.Validate(); err != nil {
		return PrepareCopyResponse{}, err
	}
	return result, nil
}
