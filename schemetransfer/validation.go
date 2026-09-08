package schemetransfer

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed schema/*.json
var schemaFS embed.FS

const schemaURL = "https://schemas.aheron.local/transfer/v1/contract.json"
const MaxDocumentBytes = 1 << 20

// ContractError deliberately excludes offending values (settings may contain
// credentials). Path is a JSON Pointer relative to the validated document.
type ContractError struct {
	Code string
	Path string
}

func (e *ContractError) Error() string {
	return fmt.Sprintf("scheme transfer: %s at %s", e.Code, e.Path)
}
func invalid(code, path string) error { return &ContractError{code, path} }

type noLoader struct{}

func (noLoader) Load(string) (any, error) { return nil, errors.New("external schemas are disabled") }

var schemas = sync.OnceValues(func() (map[string]*jsonschema.Schema, error) {
	raw, err := schemaFS.ReadFile("schema/contract.json")
	if err != nil {
		return nil, err
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	c.UseLoader(noLoader{})
	if err = c.AddResource(schemaURL, v); err != nil {
		return nil, err
	}
	out := map[string]*jsonschema.Schema{}
	for name := range v.(map[string]any)["$defs"].(map[string]any) {
		s, err := c.Compile(schemaURL + "#/$defs/" + name)
		if err != nil {
			return nil, err
		}
		out[name] = s
	}
	return out, nil
})

// Schema returns the standalone, authoritative contract schema for tooling.
func Schema() []byte { b, _ := schemaFS.ReadFile("schema/contract.json"); return b }

// DecodeJSON enforces size/depth/node limits, duplicate-key rejection and exact
// JSON numbers. No partially decoded document escapes on an error.
func DecodeJSON(raw []byte) (any, error) {
	if len(raw) > MaxDocumentBytes {
		return nil, invalid("documentTooLarge", "")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	count := 0
	var read func(int, string) (any, error)
	read = func(depth int, path string) (any, error) {
		count++
		if depth > 64 || count > 50000 {
			return nil, invalid("documentTooComplex", path)
		}
		t, err := d.Token()
		if err != nil {
			return nil, invalid("invalidJSON", path)
		}
		switch t {
		case json.Delim('{'):
			m := map[string]any{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return nil, invalid("invalidJSON", path)
				}
				key, ok := k.(string)
				if !ok {
					return nil, invalid("invalidJSON", path)
				}
				p := Pointer(path, key)
				if _, ok = m[key]; ok {
					return nil, invalid("duplicateField", p)
				}
				v, err := read(depth+1, p)
				if err != nil {
					return nil, err
				}
				m[key] = v
			}
			if _, err := d.Token(); err != nil {
				return nil, invalid("invalidJSON", path)
			}
			return m, nil
		case json.Delim('['):
			a := []any{}
			for d.More() {
				v, err := read(depth+1, fmt.Sprintf("%s/%d", path, len(a)))
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			if _, err := d.Token(); err != nil {
				return nil, invalid("invalidJSON", path)
			}
			return a, nil
		default:
			if _, ok := t.(json.Delim); ok {
				return nil, invalid("invalidJSON", path)
			}
			return t, nil
		}
	}
	v, err := read(0, "")
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, invalid("invalidJSON", "")
	}
	return v, nil
}

func Pointer(parent, key string) string {
	return parent + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}

// Validate checks the JSON shape against a named $def. Semantic/catalog checks
// are provided separately by ValidateDeclaration and the resource owner.
func Validate(definition string, raw []byte) error {
	v, err := DecodeJSON(raw)
	if err != nil {
		return err
	}
	ss, err := schemas()
	if err != nil {
		return fmt.Errorf("compile embedded transfer schema: %w", err)
	}
	s, ok := ss[definition]
	if !ok {
		return invalid("unknownContract", "")
	}
	if err := s.Validate(v); err != nil {
		path := ""
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			location := ve.InstanceLocation
			var visit func(*jsonschema.ValidationError)
			visit = func(e *jsonschema.ValidationError) {
				if len(e.InstanceLocation) > len(location) {
					location = e.InstanceLocation
				}
				for _, cause := range e.Causes {
					visit(cause)
				}
			}
			visit(ve)
			for _, part := range location {
				path = Pointer(path, part)
			}
		}
		return invalid("invalidContract", path)
	}
	return nil
}

func validateTyped(name string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return invalid("invalidJSON", "")
	}
	return Validate(name, raw)
}

func ParseCopyRules(raw []byte) (CopyRules, error) {
	var out CopyRules
	if err := Validate("copyRules", raw); err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return CopyRules{}, invalid("invalidContract", "")
	}
	if err := validateRuleSemantics(out.Settings, ""); err != nil {
		return CopyRules{}, err
	}
	return out, nil
}

func validateRuleSemantics(r *Rule, path string) error {
	if r == nil {
		return nil
	}
	if r.Kind == "reference" {
		if (r.Empty == "bindDefault") != (r.DefaultReference != nil) {
			return invalid("invalidDefault", path)
		}
		if r.Resource.Kind == "integrationResource" {
			if r.ReadAs != "value" || r.WriteAs != "value" || r.DefaultReference != nil {
				return invalid("invalidResourceEncoding", path)
			}
		} else {
			if r.ReadAs == "value" || r.WriteAs == "value" {
				return invalid("invalidResourceEncoding", path)
			}
			if (r.Resource.Kind == "branch" || r.Resource.Kind == "tag") && (r.ReadAs != "id" || r.WriteAs != "id" || r.DefaultReference != nil) {
				return invalid("invalidResourceEncoding", path)
			}
		}
	}
	for _, k := range sortedKeys(r.Fields) {
		v := r.Fields[k]
		if err := validateRuleSemantics(&v, Pointer(path, k)); err != nil {
			return err
		}
	}
	if err := validateRuleSemantics(r.Items, Pointer(path, "items")); err != nil {
		return err
	}
	return validateRuleSemantics(r.Values, Pointer(path, "values"))
}

// ValidateDeclaration checks all links within one immutable integration version.
// It never follows a URL; external $refs in constraintsSchema are rejected.
func ValidateDeclaration(sources map[string]ResourceSource, rules map[string]*CopyRules, resourceValuesURL, validateURL string) error {
	if sources == nil {
		sources = map[string]ResourceSource{}
	}
	if err := validateTyped("resourceSources", sources); err != nil {
		return err
	}
	if len(sources) > 0 && resourceValuesURL == "" {
		return invalid("resourceValuesEndpointRequired", "/resourceValuesUrl")
	}
	state := map[string]int{}
	constraintSchemas := map[string]*jsonschema.Schema{}
	var visit func(string) error
	visit = func(k string) error {
		if state[k] == 1 {
			return invalid("cyclicSourceDependencies", Pointer("/resourceSources", k))
		}
		if state[k] == 2 {
			return nil
		}
		state[k] = 1
		s := sources[k]
		hasRequired := false
		for _, p := range sortedKeys(s.Parameters) {
			dep := s.Parameters[p]
			if _, ok := sources[dep.SourceKey]; !ok {
				return invalid("unknownSource", Pointer("/resourceSources", k))
			}
			hasRequired = hasRequired || dep.Required
			if err := visit(dep.SourceKey); err != nil {
				return err
			}
		}
		if s.IdentityScope == "parent" && !hasRequired {
			return invalid("parentParameterRequired", Pointer("/resourceSources", k))
		}
		if !contains(s.Supports, "resolve") {
			return invalid("resolveRequired", Pointer("/resourceSources", k))
		}
		if len(s.ConstraintsSchema) > 0 {
			v, err := DecodeJSON(s.ConstraintsSchema)
			if err != nil {
				return err
			}
			c := jsonschema.NewCompiler()
			c.UseLoader(noLoader{})
			const u = "https://schemas.aheron.local/constraints.json"
			if err = c.AddResource(u, v); err != nil {
				return invalid("invalidConstraintsSchema", Pointer("/resourceSources", k))
			}
			compiled, err := c.Compile(u)
			if err != nil {
				return invalid("invalidConstraintsSchema", Pointer("/resourceSources", k))
			}
			constraintSchemas[k] = compiled
		}
		state[k] = 2
		return nil
	}
	for _, k := range sortedKeys(sources) {
		if err := visit(k); err != nil {
			return err
		}
	}
	for _, k := range sortedKeys(rules) {
		r := rules[k]
		if r == nil {
			continue
		}
		raw, err := json.Marshal(r)
		if err != nil {
			return invalid("invalidContract", Pointer("/blocks", k))
		}
		parsed, err := ParseCopyRules(raw)
		if err != nil {
			return err
		}
		if parsed.Validation != nil && parsed.Validation.Mode == "integration" && validateURL == "" {
			return invalid("validatorEndpointRequired", Pointer("/blocks", k))
		}
		var walk func(*Rule) error
		walk = func(f *Rule) error {
			if f == nil {
				return nil
			}
			if f.Resource != nil && f.Resource.Kind == "integrationResource" {
				if _, ok := sources[f.Resource.SourceKey]; !ok {
					return invalid("unknownSource", Pointer("/blocks", k))
				}
				if len(f.Constraints) > 0 {
					schema := constraintSchemas[f.Resource.SourceKey]
					if schema == nil {
						return invalid("constraintsSchemaRequired", Pointer("/blocks", k))
					}
					v, err := DecodeJSON(f.Constraints)
					if err != nil {
						return err
					}
					if err := schema.Validate(v); err != nil {
						return invalid("invalidResourceConstraints", Pointer("/blocks", k))
					}
				}
			}
			for _, key := range sortedKeys(f.Fields) {
				v := f.Fields[key]
				if err := walk(&v); err != nil {
					return err
				}
			}
			if err := walk(f.Items); err != nil {
				return err
			}
			return walk(f.Values)
		}
		if err := walk(parsed.Settings); err != nil {
			return err
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func contains(a []string, v string) bool {
	for _, s := range a {
		if s == v {
			return true
		}
	}
	return false
}

func (r ValidationResult) Validate() error {
	if err := validateTyped("validationResult", r); err != nil {
		return err
	}
	expected := "passed"
	for _, issue := range r.Issues {
		if issue.Severity == "error" {
			expected = "blocked"
			break
		}
		if issue.Severity == "review" {
			expected = "needsReview"
		}
	}
	if r.Status != expected {
		return invalid("inconsistentValidationStatus", "/status")
	}
	return nil
}
