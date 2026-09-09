package schemetransfer

import (
	"encoding/json"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ValidateSourceLookup checks a request against the selected catalog source.
// Parameters must already be trusted values resolved by the caller, not values
// supplied by a browser. Ownership/selectability remain the integration's job.
func ValidateSourceLookup(sources map[string]ResourceSource, req LookupRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	source, ok := sources[req.SourceKey]
	if !ok {
		return invalid("unknownSource", "/sourceKey")
	}
	if !contains(source.Supports, req.Mode) {
		return invalid("unsupportedLookupMode", "/mode")
	}
	for name, value := range req.Parameters {
		param, ok := source.Parameters[name]
		if !ok {
			return invalid("unknownSourceParameter", "/parameters")
		}
		parent, ok := sources[param.SourceKey]
		if !ok {
			return invalid("unknownSource", "/parameters")
		}
		if err := ValidateResourceValue(parent.ValueType, value); err != nil {
			return invalid("invalidSourceParameter", "/parameters")
		}
	}
	for name, param := range source.Parameters {
		if _, ok := req.Parameters[name]; param.Required && !ok {
			return invalid("sourceParameterRequired", "/parameters")
		}
	}
	if len(req.Constraints) != 0 {
		if len(source.ConstraintsSchema) == 0 {
			return invalid("constraintsNotSupported", "/constraints")
		}
		value, err := DecodeJSON(req.Constraints)
		if err != nil {
			return err
		}
		schema, err := compileResourceSchema(source.ConstraintsSchema)
		if err != nil {
			return invalid("invalidConstraintsSchema", "/constraints")
		}
		if err := schema.Validate(value); err != nil {
			return invalid("invalidResourceConstraints", "/constraints")
		}
	}
	if req.Mode == "identify" {
		for _, value := range req.Values {
			if err := ValidateResourceValue(source.ValueType, value); err != nil {
				return invalid("invalidResourceValue", "/values")
			}
		}
	}
	return nil
}

var resourceTypes = sync.OnceValues(func() (map[string]*jsonschema.Schema, error) {
	result := map[string]*jsonschema.Schema{}
	for _, name := range []string{"string", "number", "integer", "boolean", "object", "array"} {
		raw, _ := json.Marshal(map[string]string{"type": name})
		schema, err := compileResourceSchema(raw)
		if err != nil {
			return nil, err
		}
		result[name] = schema
	}
	return result, nil
})

// ValidateResourceValue checks native JSON types without a float64 conversion.
func ValidateResourceValue(valueType string, raw json.RawMessage) error {
	values, err := resourceTypes()
	if err != nil {
		return invalid("invalidResourceType", "")
	}
	schema, ok := values[valueType]
	if !ok {
		return invalid("invalidResourceType", "")
	}
	value, err := DecodeJSON(raw)
	if err != nil {
		return err
	}
	if err := schema.Validate(value); err != nil {
		return invalid("invalidResourceValue", "")
	}
	return nil
}

func compileResourceSchema(raw []byte) (*jsonschema.Schema, error) {
	value, err := DecodeJSON(raw)
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(noLoader{})
	compiler.AssertFormat()
	const uri = "https://schemas.aheron.local/resource-runtime.json"
	if err := compiler.AddResource(uri, value); err != nil {
		return nil, err
	}
	return compiler.Compile(uri)
}
