package integration

import (
	"encoding/json"
	"strings"
	"testing"
)

func testVars(t *testing.T) Vars {
	t.Helper()

	v, err := ParseVars(json.RawMessage(`{
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
	if err != nil {
		t.Fatal(err)
	}
	return v
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
		{"no project fallback", "city", nil, false},
		{"subject wins a bare key collision", "shared", "субъектная", true},
		{"the subject namespace", "subject.shared", "субъектная", true},
		{"the project namespace", "project.shared", "проектная", true},
		{"another integration's variable", "messengers.tgUserId", "42", true},
		{"a path into a structured variable", "subject.payment.id", "p-1", true},
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
		{"a path into a structured variable", "{{subject.payment.amount}}", "199.5"},
		{"a structured variable", "{{items}}", `["a","b"]`},
		{"an empty template", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := vars.Substitute(tc.template); err != nil || got != tc.want {
				t.Errorf("Substitute(%q) = %q, want %q", tc.template, got, tc.want)
			}
		})
	}
}

// TestVarsSubstituteFuncEscapes covers the hook a template with a syntax of its
// own needs: the escape applies to the substituted value, never to the template
// around it.
func TestVarsSubstituteFuncEscapes(t *testing.T) {
	vars, err := ParseVars(json.RawMessage(`{"subject": {"name": "<b>Иван</b>"}}`))
	if err != nil {
		t.Fatal(err)
	}

	escape := func(s string) string { return strings.ReplaceAll(s, "<", "&lt;") }

	got, err := vars.SubstituteFunc("<i>{{name}}</i>", escape)
	if want := "<i>&lt;b>Иван&lt;/b></i>"; err != nil || got != want {
		t.Errorf("SubstituteFunc = %q, want %q", got, want)
	}
}

func TestParseVarsRejectsInvalidPayloads(t *testing.T) {
	for _, raw := range []string{`["a"]`, `{"a":`, `{"name":"Иван"}`, `{"subject":{},"project":[]}`, `{} {}`, `{"subject":{"ok":1},"project":"bad"}`} {
		vars, err := ParseVars(json.RawMessage(raw))
		if err == nil {
			t.Fatalf("expected error for %s", raw)
		}
		if _, ok := vars.Lookup("ok"); ok {
			t.Fatal("partially decoded scope escaped")
		}
	}
	for _, raw := range []string{"", "null", "{}"} {
		vars, err := ParseVars(json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		if got, err := vars.Substitute("[{{unknown}}]"); err != nil || got != "[]" {
			t.Fatalf("%q %v", got, err)
		}
	}
}

func TestUnifiedScopeAndExactNumbers(t *testing.T) {
	vars, err := ParseVars(json.RawMessage(`{"projectId":"actual-project","project":{"id":"shadow","amount":99},"subject":{"amount":9007199254740993,"price":1.5e5},"integrations":{"payments":{"amount":3}}}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := vars.Substitute("{{amount}}|{{project.amount}}|{{payments.amount}}|{{project.id}}|{{price}}")
	if err != nil || got != "9007199254740993|99|3|actual-project|150000" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := vars.Substitute("secret {{broken"); err == nil {
		t.Fatal("malformed template accepted")
	}
}
