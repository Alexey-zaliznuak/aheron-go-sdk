package variables

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Values is an already-loaded execution scope. Maps are borrowed read-only;
// callers must not mutate them concurrently with Lookup. Definition aliases
// (ID -> value) must be populated by their owner if ID lookup is required.
// ProjectID is explicit: project.id never reads a project variable named id.
// Context and IntegrationState must only be provided in an authorized runtime.
type Values struct {
	Subject          map[string]any
	Project          map[string]any
	Integrations     map[string]map[string]any
	ProjectID        string
	Context          map[string]any
	IntegrationState map[string]any
}

func (v Values) Lookup(ref Reference) (any, bool, error) {
	if _, err := ref.Expression(); err != nil {
		return nil, false, err
	}
	var root map[string]any
	switch ref.Scope {
	case ScopeSubject:
		root = v.Subject
	case ScopeProject:
		root = v.Project
	case ScopeIntegration:
		root = v.Integrations[ref.Namespace]
	case ScopeRuntime:
		switch ref.Namespace {
		case "project":
			if v.ProjectID == "" {
				return nil, false, nil
			}
			value, found := Walk(v.ProjectID, ref.AccessPath)
			return value, found, nil
		case "context":
			root = v.Context
		case "integrationState":
			root = v.IntegrationState
		}
	}
	value, ok := root[ref.Key]
	if !ok {
		return nil, false, nil
	}
	value, ok = Walk(value, ref.AccessPath)
	return value, ok, nil
}

// Walk follows an access path in a decoded JSON value. Object properties and
// canonical zero-based array indexes are supported; it never guesses a key.
func Walk(value any, path []string) (any, bool) {
	for _, part := range path {
		switch object := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = object[part]
			if !ok {
				return nil, false
			}
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(object) || strconv.Itoa(i) != part {
				return nil, false
			}
			value = object[i]
		default:
			return nil, false
		}
	}
	return value, true
}

// DecodeValue decodes one JSON value while retaining exact numbers. Services
// use it when loading raw CRM values; ordinary json.Unmarshal would already
// lose large integers before a value reached the shared renderer.
func DecodeValue(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("variables: expected one JSON value")
	}
	return value, nil
}

// Format uses the same JSON/string convention as integration.VarString, but
// reports unrepresentable values rather than turning encoding errors into "".
func Format(value any) (string, error) {
	switch typed := value.(type) {
	case nil:
		return "", nil
	case string:
		return typed, nil
	case bool:
		return strconv.FormatBool(typed), nil
	case float64:
		// Marshal rejects NaN and infinities; keep the established non-exponent
		// representation for ordinary JSON numbers after validating them.
		if _, err := json.Marshal(typed); err != nil {
			return "", err
		}
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	case json.Number:
		return formatNumber(typed)
	default:
		encoded, err := json.Marshal(typed)
		return string(encoded), err
	}
}

// formatNumber expands an exponent without passing a monetary/integer value
// through float64. The bound prevents a tiny number token producing huge output.
func formatNumber(number json.Number) (string, error) {
	if _, err := json.Marshal(number); err != nil || string(number) == "" {
		return "", fmt.Errorf("variables: invalid JSON number")
	}
	sign, digits := "", string(number)
	if strings.HasPrefix(digits, "-") {
		sign, digits = "-", digits[1:]
	}
	exponent := 0
	if i := strings.IndexAny(digits, "eE"); i >= 0 {
		var err error
		exponent, err = strconv.Atoi(digits[i+1:])
		if err != nil || exponent < -4096 || exponent > 4096 {
			return "", fmt.Errorf("variables: number expansion exceeds 4096 digits")
		}
		digits = digits[:i]
	}
	point := len(digits)
	if i := strings.IndexByte(digits, '.'); i >= 0 {
		point = i
		digits = digits[:i] + digits[i+1:]
	}
	point += exponent
	if len(digits) > 4096 || point > 4096 || len(digits)-point > 4096 {
		return "", fmt.Errorf("variables: number expansion exceeds 4096 digits")
	}
	switch {
	case point <= 0:
		digits = "0." + strings.Repeat("0", -point) + digits
	case point >= len(digits):
		digits += strings.Repeat("0", point-len(digits))
	default:
		digits = digits[:point] + "." + digits[point:]
	}
	if strings.Contains(digits, ".") {
		digits = strings.TrimRight(strings.TrimRight(digits, "0"), ".")
	}
	digits = strings.TrimLeft(digits, "0")
	if digits == "" || strings.HasPrefix(digits, ".") {
		digits = "0" + digits
	}
	return sign + digits, nil
}
