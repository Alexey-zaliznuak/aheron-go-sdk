package integration

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// This file decodes {{vars}}, the placeholder an integration author templates
// into the action_request_template to receive the values a block may substitute
// into its own settings.
//
// Its shape is a platform contract, and it is not a flat map: the platform sends
// three namespaces, so an integration that reads the payload as "key -> value"
// finds nothing under a bare key and silently writes empty strings wherever the
// author expected a variable.

// varsPlaceholder matches a {{ key }} reference; the captured group is the
// whitespace-trimmed key. Braces are not allowed inside, so an unterminated
// "{{" never swallows the text that follows it.
var varsPlaceholder = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)

// Vars is the decoded {{vars}} object of an action request. It carries the
// project's and the subject's variables by their bare key, plus every installed
// integration's integration-owned variables grouped by catalog slug.
//
// Decode it from the field the action_request_template templated {{vars}} into,
// then resolve the {{key}} references the author typed into the block settings
// with Substitute.
type Vars struct {
	Project      map[string]any            `json:"project"`
	Subject      map[string]any            `json:"subject"`
	Integrations map[string]map[string]any `json:"integrations"`
}

// ParseVars decodes a raw {{vars}} payload. A payload that is absent, null or
// malformed yields a zero Vars whose lookups all miss, so every placeholder
// resolves to "" instead of failing the block: a step whose scheme has no
// variables is perfectly normal.
func ParseVars(raw json.RawMessage) Vars {
	var v Vars
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &v)
	}
	return v
}

// Lookup resolves one placeholder key.
//
// A bare key is a project variable first and a subject variable second, so the
// keys the block editors offer work as typed. A dotted key selects a namespace:
// "project" and "subject" address those maps explicitly, and any other prefix is
// an integration's catalog slug, which is how a block reads another
// integration's values. A dotted key whose prefix names no namespace is walked
// into the value itself, so a variable holding an object can be addressed as
// {{payment.amount}}.
func (v Vars) Lookup(key string) (any, bool) {
	prefix, rest, dotted := strings.Cut(key, ".")
	if !dotted {
		if value, ok := v.Project[key]; ok {
			return value, true
		}
		value, ok := v.Subject[key]
		return value, ok
	}

	switch prefix {
	case "project":
		value, ok := v.Project[rest]
		return value, ok
	case "subject":
		value, ok := v.Subject[rest]
		return value, ok
	}

	if keys, ok := v.Integrations[prefix]; ok {
		value, ok := keys[rest]
		return value, ok
	}

	if value, ok := walkPath(v.Project, key); ok {
		return value, true
	}
	return walkPath(v.Subject, key)
}

// Substitute replaces every {{key}} in template with the referenced value's
// string form. An unknown key resolves to "", which is what makes "Заказ
// {{orderId}}" degrade to "Заказ " instead of leaking the placeholder into
// whatever the integration writes.
func (v Vars) Substitute(template string) string {
	return v.SubstituteFunc(template, nil)
}

// SubstituteFunc is Substitute with an escape hook applied to each substituted
// value before it is spliced in. It exists for templates whose surroundings have
// a syntax of their own — HTML message text, for instance — where a value must
// not be able to smuggle in markup.
func (v Vars) SubstituteFunc(template string, escape func(string) string) string {
	if !strings.Contains(template, "{{") {
		return template
	}

	return varsPlaceholder.ReplaceAllStringFunc(template, func(match string) string {
		key := strings.TrimSpace(varsPlaceholder.FindStringSubmatch(match)[1])
		value, ok := v.Lookup(key)
		if !ok {
			return ""
		}
		text := VarString(value)
		if escape != nil {
			text = escape(text)
		}
		return text
	})
}

// VarString renders a decoded variable value for textual interpolation.
//
// A number is written without a trailing ".0" because JSON has no integers: an
// order number arrives as a float and would otherwise print as "1024.000000".
// Anything structured falls back to its compact JSON form, which is at least
// lossless and visible.
func VarString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case json.Number:
		return typed.String()
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}

// walkPath follows a dotted path into a decoded JSON object.
func walkPath(root map[string]any, path string) (any, bool) {
	var current any = root
	for _, part := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}
