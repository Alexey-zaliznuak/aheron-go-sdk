package integration

import (
	"encoding/json"
	"strings"
	"testing"
)

func testVars(t *testing.T) Vars {
	t.Helper()

	return ParseVars(json.RawMessage(`{
		"project": {"city": "Москва", "shared": "проектная"},
		"subject": {
			"name": "Иван",
			"orderId": 1024,
			"amount": 199.50,
			"paid": true,
			"empty": null,
			"payment": {"amount": 199.5, "id": "p-1"},
			"items": ["a", "b"],
			"shared": "субъектная"
		},
		"integrations": {"messengers": {"tgUserId": "42"}}
	}`))
}

func TestVarsLookup(t *testing.T) {
	vars := testVars(t)

	for _, tc := range []struct {
		name  string
		key   string
		want  any
		found bool
	}{
		{"a subject variable by its bare key", "name", "Иван", true},
		{"a project variable by its bare key", "city", "Москва", true},
		{"project wins a bare key collision", "shared", "проектная", true},
		{"the subject namespace", "subject.shared", "субъектная", true},
		{"the project namespace", "project.shared", "проектная", true},
		{"another integration's variable", "messengers.tgUserId", "42", true},
		{"a path into a structured variable", "payment.id", "p-1", true},
		{"an unknown key", "nope", nil, false},
		{"an unknown namespace", "nope.nope", nil, false},
		{"a known namespace without the key", "subject.nope", nil, false},
		{"a null variable is present", "empty", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := vars.Lookup(tc.key)
			if ok != tc.found {
				t.Fatalf("Lookup(%q) found = %v, want %v", tc.key, ok, tc.found)
			}
			if got != tc.want {
				t.Errorf("Lookup(%q) = %#v, want %#v", tc.key, got, tc.want)
			}
		})
	}
}

func TestVarsSubstitute(t *testing.T) {
	vars := testVars(t)

	for _, tc := range []struct {
		name     string
		template string
		want     string
	}{
		{"plain text", "Иван", "Иван"},
		{"a variable alone", "{{name}}", "Иван"},
		{"a variable inside text", "Заказ {{orderId}} от {{name}}", "Заказ 1024 от Иван"},
		{"spaces inside the braces", "{{ name }}", "Иван"},
		{"a whole number keeps no decimals", "{{orderId}}", "1024"},
		{"a fractional number keeps its digits", "{{amount}}", "199.5"},
		{"a boolean", "{{paid}}", "true"},
		{"a null variable", "[{{empty}}]", "[]"},
		{"an unknown variable", "Заказ {{nope}}", "Заказ "},
		{"a namespaced variable", "{{project.city}}", "Москва"},
		{"a path into a structured variable", "{{payment.amount}}", "199.5"},
		{"a structured variable", "{{items}}", `["a","b"]`},
		{"an empty template", "", ""},
		{"an unclosed placeholder", "{{name", "{{name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := vars.Substitute(tc.template); got != tc.want {
				t.Errorf("Substitute(%q) = %q, want %q", tc.template, got, tc.want)
			}
		})
	}
}

// TestVarsSubstituteFuncEscapes covers the hook a template with a syntax of its
// own needs: the escape applies to the substituted value, never to the template
// around it.
func TestVarsSubstituteFuncEscapes(t *testing.T) {
	vars := ParseVars(json.RawMessage(`{"subject": {"name": "<b>Иван</b>"}}`))

	escape := func(s string) string { return strings.ReplaceAll(s, "<", "&lt;") }

	got := vars.SubstituteFunc("<i>{{name}}</i>", escape)
	if want := "<i>&lt;b>Иван&lt;/b></i>"; got != want {
		t.Errorf("SubstituteFunc = %q, want %q", got, want)
	}
}

func TestParseVarsTolerateUnusablePayloads(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"absent", ""},
		{"null", "null"},
		{"an array", `["a"]`},
		{"broken json", `{"a":`},
		{"a flat map without the namespaces", `{"name": "Иван"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vars := ParseVars(json.RawMessage(tc.raw))
			if _, ok := vars.Lookup("name"); ok {
				t.Error("Lookup found a value in an unusable payload")
			}
			if got := vars.Substitute("[{{name}}]"); got != "[]" {
				t.Errorf("Substitute = %q, want %q", got, "[]")
			}
		})
	}
}
