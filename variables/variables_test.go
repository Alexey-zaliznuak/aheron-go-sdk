package variables

import (
	"encoding/json"
	"errors"
	"html"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestReferenceIdentity(t *testing.T) {
	for _, tc := range []struct {
		input      string
		want       Reference
		expression string
	}{
		{"foo", Reference{Scope: ScopeSubject, Key: "foo"}, "subject.foo"},
		{"subject.foo", Reference{Scope: ScopeSubject, Key: "foo"}, "subject.foo"},
		{"project.foo", Reference{Scope: ScopeProject, Key: "foo"}, "project.foo"},
		{"messengers.foo", Reference{Scope: ScopeIntegration, Namespace: "messengers", Key: "foo"}, "messengers.foo"},
		{"subject.order.items.0.цена", Reference{Scope: ScopeSubject, Key: "order", AccessPath: []string{"items", "0", "цена"}}, "subject.order.items.0.цена"},
		{"project.id", Reference{Scope: ScopeRuntime, Namespace: "project", Key: "id"}, "project.id"},
		{"context.subjectId", Reference{Scope: ScopeRuntime, Namespace: "context", Key: "subjectId"}, "context.subjectId"},
		{"integrationState.token", Reference{Scope: ScopeRuntime, Namespace: "integrationState", Key: "token"}, "integrationState.token"},
		{" subject.10000000-0000-4000-8000-000000000001 ", Reference{Scope: ScopeSubject, Key: "10000000-0000-4000-8000-000000000001"}, "subject.10000000-0000-4000-8000-000000000001"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := ParseReferenceV1(tc.input)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%+v %v", got, err)
			}
			if expr, err := got.Expression(); err != nil || expr != tc.expression {
				t.Fatalf("%s %v", expr, err)
			}
		})
	}
	for _, bad := range []string{"", "a..b", ".foo", "subject.", "foo bar", "foo[0]", "{{foo}}", "subject.foo/bar", "subject.foo\nbar"} {
		if _, err := ParseReferenceV1(bad); !errors.Is(err, ErrInvalidReference) {
			t.Fatalf("accepted %q", bad)
		}
	}
	for _, bad := range []Reference{
		{Scope: ScopeIntegration, Namespace: "project", Key: "foo"},
		{Scope: ScopeProject, Key: "id"},
		{Scope: ScopeRuntime, Namespace: "project", Key: "foo"},
		{Scope: ScopeSubject, Key: "foo}}evil{{bar"},
		{Scope: ScopeSubject, Key: "foo", AccessPath: []string{"bar.baz"}},
	} {
		if _, err := bad.Expression(); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

func TestScopeDoesNotDependOnInstallationOrOtherValues(t *testing.T) {
	v := Values{ProjectID: "project-id", Subject: map[string]any{"foo": "subject", "payment": map[string]any{"amount": 1}, "object": map[string]any{"items": []any{map[string]any{"цена": 12}}, "null": nil}}, Project: map[string]any{"foo": "project", "id": "shadow"}, Integrations: map[string]map[string]any{"payment": {"amount": 2}}}
	for _, tc := range []struct {
		key   string
		want  any
		found bool
	}{
		{"foo", "subject", true}, {"project.foo", "project", true}, {"payment.amount", 2, true},
		{"subject.payment.amount", 1, true}, {"project.id", "project-id", true},
		{"subject.object.items.0.цена", 12, true}, {"subject.object.items.00", nil, false},
		{"subject.object.items.-1", nil, false}, {"subject.object.items.999999999999999999999999999999", nil, false},
		{"subject.object.null", nil, true}, {"subject.object.null.child", nil, false},
		{"subject.object.items.length", nil, false}, {"context.subjectId", nil, false},
	} {
		ref, err := ParseReferenceV1(tc.key)
		if err != nil {
			t.Fatal(err)
		}
		got, found, err := v.Lookup(ref)
		if err != nil || found != tc.found || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: %#v %v %v", tc.key, got, found, err)
		}
	}
	delete(v.Integrations, "payment")
	ref, _ := ParseReferenceV1("payment.amount")
	if _, found, err := v.Lookup(ref); err != nil || found {
		t.Fatal("fell back to subject object after uninstall")
	}
	delete(v.Subject, "foo")
	ref, _ = ParseReferenceV1("foo")
	if _, found, err := v.Lookup(ref); err != nil || found {
		t.Fatal("fell back to project")
	}
}

func TestParseRenderAndRewrite(t *testing.T) {
	source := "Привет, <b>{{ name }}</b> {{subject.order.items.0.title}}!"
	parsed, err := ParseV1(source)
	if err != nil {
		t.Fatal(err)
	}
	parts := parsed.Parts()
	for _, p := range parts {
		if p.Reference == nil && source[p.Start:p.End] != p.Text {
			t.Fatal("bad byte range")
		}
	}
	parts[1].Reference.Key = "tampered"
	parts[3].Reference.AccessPath[0] = "tampered"
	v := Values{Subject: map[string]any{"name": "<A>{{secret}}", "order": map[string]any{"items": []any{map[string]any{"title": "Book"}}}}}
	got, err := parsed.Render(v.Lookup, RenderOptions{Escape: html.EscapeString})
	if err != nil || got != "Привет, <b>&lt;A&gt;{{secret}}</b> Book!" {
		t.Fatalf("%q %v", got, err)
	}
	got, err = parsed.Rewrite(func(ref Reference) (Reference, error) {
		if ref.Key == "name" {
			ref.Key = "customer"
		}
		return ref, nil
	})
	if err != nil || got != "Привет, <b>{{subject.customer}}</b> {{subject.order.items.0.title}}!" {
		t.Fatalf("%q %v", got, err)
	}
	// Returned references are copies, including their path slices.
	_, err = parsed.Render(func(ref Reference) (any, bool, error) {
		if len(ref.AccessPath) > 0 {
			ref.AccessPath[0] = "mutated"
		}
		return "", true, nil
	}, RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, err = parsed.Render(v.Lookup, RenderOptions{})
	if err != nil || !strings.HasSuffix(got, " Book!") {
		t.Fatalf("resolver mutated template: %q %v", got, err)
	}
}

func TestInvalidTemplatesAndAtomicFailure(t *testing.T) {
	for _, input := range []string{"secret {{foo", "{{}}", "{{ }}", "{{a..b}}", "{{{foo}}}", "{{foo {{bar}}"} {
		_, err := ParseV1(input)
		var syntax *SyntaxError
		if !errors.As(err, &syntax) || !errors.Is(err, ErrInvalidTemplate) {
			t.Fatalf("%q: %v", input, err)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatal("source leaked")
		}
	}
	parsed, _ := ParseV1("ok {{known}} {{missing}}")
	v := Values{Subject: map[string]any{"known": "value"}}
	if got, err := parsed.Render(v.Lookup, RenderOptions{}); got != "" || !errors.Is(err, ErrUnresolved) {
		t.Fatalf("%q %v", got, err)
	}
	if got, err := parsed.Render(v.Lookup, RenderOptions{Missing: MissingEmpty}); got != "ok value " || err != nil {
		t.Fatalf("%q %v", got, err)
	}
	failure := errors.New("offline")
	if got, err := parsed.Render(func(Reference) (any, bool, error) { return nil, false, failure }, RenderOptions{Missing: MissingEmpty}); got != "" || !errors.Is(err, failure) {
		t.Fatalf("%q %v", got, err)
	}
	if got, err := parsed.Rewrite(func(Reference) (Reference, error) { return Reference{}, failure }); got != "" || !errors.Is(err, failure) {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := parsed.Render(nil, RenderOptions{}); err == nil {
		t.Fatal("nil resolver accepted")
	}
	if _, err := parsed.Render(v.Lookup, RenderOptions{Missing: 99}); err == nil {
		t.Fatal("unknown policy accepted")
	}
	for _, input := range []string{"", "a { b } }}"} {
		p, err := ParseV1(input)
		if err != nil {
			t.Fatal(err)
		}
		got, err := p.Render(nil, RenderOptions{})
		if err != nil || got != input {
			t.Fatalf("%q %v", got, err)
		}
	}
}

func TestNumberFormatting(t *testing.T) {
	for input, want := range map[string]string{"9007199254740993": "9007199254740993", "1.5e5": "150000", "1.234e-2": "0.01234", "-1e3": "-1000", "0.00": "0", "0e5": "0", "100.0100": "100.01", "1E+2": "100", "0.0001": "0.0001"} {
		got, err := Format(json.Number(input))
		if err != nil || got != want {
			t.Fatalf("%s: %q %v", input, got, err)
		}
	}
	for _, input := range []any{json.Number(""), json.Number("01"), json.Number("1e999999999999999"), json.Number("1e5000"), math.NaN(), math.Inf(1), make(chan int)} {
		if _, err := Format(input); err == nil {
			t.Fatalf("accepted %#v", input)
		}
	}
}

func FuzzParseRewrite(f *testing.F) {
	for _, seed := range []string{"", "Привет {{foo}}", "{{subject.object.items.0}}", "{{project.id}}", "{{bad"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		parsed, err := ParseV1(input)
		if err != nil {
			return
		}
		rewritten, err := parsed.Rewrite(func(ref Reference) (Reference, error) { return ref, nil })
		if err != nil {
			t.Fatal(err)
		}
		again, err := ParseV1(rewritten)
		if err != nil {
			t.Fatal(err)
		}
		stable, err := again.Rewrite(func(ref Reference) (Reference, error) { return ref, nil })
		if err != nil || stable != rewritten {
			t.Fatalf("unstable rewrite: %q -> %q", rewritten, stable)
		}
	})
}
