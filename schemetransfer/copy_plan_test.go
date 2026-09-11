package schemetransfer

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCopyBuilderPreservesLiteralTextTypesAndListShape(t *testing.T) {
	settings := json.RawMessage(`{"channelId":"private-channel","fileIds":["f2","f1","f2"],"text":"Hello {{lead.name}}","button":"{{literal}}","large":9007199254740993,"a/b~c":"value","saveTo":"answer"}`)
	result, err := NewCopyBuilder(settings).Resource("/channelId", "channels").ResourceList("/fileIds", "files").Template("/text").SubjectVariable("/saveTo", "string", "write").Resource("/a~1b~0c", "channels").Build()
	if err != nil {
		t.Fatal(err)
	}
	if result.ProtocolVersion != 2 || len(result.Plan.References) != 6 || !reflect.DeepEqual(result.Plan.Templates, []string{"/text"}) {
		t.Fatal("lost semantics")
	}
	v, _ := DecodeJSON(result.Settings)
	m := v.(map[string]any)
	if m["channelId"] != nil || m["button"] != "{{literal}}" || m["large"] != json.Number("9007199254740993") || !reflect.DeepEqual(m["fileIds"], []any{nil, nil, nil}) {
		t.Fatal("changed literal or list")
	}
	hydrated, err := result.HydratedSettings()
	if err != nil {
		t.Fatal(err)
	}
	a, _ := DecodeJSON(settings)
	b, _ := DecodeJSON(hydrated)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("source round trip changed settings")
	}
	if string(settings) == string(result.Settings) {
		t.Fatal("source IDs were not separated")
	}
}

func TestCopyPlanRejectsInvalidPathsAndAmbiguousSemantics(t *testing.T) {
	for name, build := range map[string]func() *CopyBuilder{
		"missing":    func() *CopyBuilder { return NewCopyBuilder(map[string]any{}).Resource("/channel", "channels") },
		"root":       func() *CopyBuilder { return NewCopyBuilder(map[string]any{}).Resource("", "channels") },
		"bad escape": func() *CopyBuilder { return NewCopyBuilder(map[string]any{"~2": "x"}).Resource("/~2", "channels") },
		"array append": func() *CopyBuilder {
			return NewCopyBuilder(map[string]any{"a": []string{"x"}}).Resource("/a/-", "channels")
		},
		"array alias": func() *CopyBuilder {
			return NewCopyBuilder(map[string]any{"a": []string{"x"}}).Resource("/a/00", "channels")
		},
		"empty source": func() *CopyBuilder { return NewCopyBuilder(map[string]any{"a": nil}).Resource("/a", "channels") },
		"duplicate": func() *CopyBuilder {
			return NewCopyBuilder(map[string]any{"a": "x"}).Resource("/a", "channels").Template("/a")
		},
		"overlap": func() *CopyBuilder {
			return NewCopyBuilder(map[string]any{"a": map[string]any{"b": "x"}}).Template("/a/b").Resource("/a", "channels")
		},
		"native value encoding": func() *CopyBuilder {
			return NewCopyBuilder(map[string]any{"a": "x"}).Reference("/a", ResourceSelector{Kind: "tag"}, "value", "value", "")
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := build().Build(); err == nil {
				t.Fatal("accepted invalid plan")
			}
		})
	}
}

func TestCopyProtocolCannotSilentlyDowngrade(t *testing.T) {
	v1 := PrepareCopyResponse{Settings: json.RawMessage(`{}`), Issues: []Issue{}}
	v2, err := NewCopyBuilder(map[string]any{}).Build()
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []int{1, 2} {
		req := PrepareCopyRequest{ProtocolVersion: version, ProjectID: "p", SchemeID: "s", IntegrationVersion: 3, BlockKey: "send", Settings: json.RawMessage(`{}`)}
		rawReq, _ := json.Marshal(req)
		for i, result := range []PrepareCopyResponse{v1, v2} {
			raw, _ := json.Marshal(result)
			if err := ValidateCallbackResponse(PrepareCopy, rawReq, raw); (err == nil) != (i+1 == version) {
				t.Fatalf("version %d, response %d: %v", version, i+1, err)
			}
		}
	}
	if _, err := ParseCopyRules([]byte(`{"version":2,"mode":"callback","settings":{"kind":"object","fields":{}}}`)); err == nil {
		t.Fatal("accepted static tree in callback mode")
	}
}

func TestCopyPlanValidatesDeclaredSourcesAndConstraints(t *testing.T) {
	r, err := NewCopyBuilder(map[string]any{"channel": "c"}).Resource("/channel", "channels").Build()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Plan.ValidateSources(nil); err == nil {
		t.Fatal("accepted unknown source")
	}
	sources := map[string]ResourceSource{"channels": {ValueType: "string", IdentityScope: "project", Supports: []string{"resolve", "identify"}}}
	if err := r.Plan.ValidateSources(sources); err != nil {
		t.Fatal(err)
	}
	r.Plan.References[0].Constraints = json.RawMessage(`{"platform":"telegram"}`)
	if err := r.Plan.ValidateSources(sources); err == nil {
		t.Fatal("accepted undeclared constraints")
	}
	source := sources["channels"]
	source.ConstraintsSchema = json.RawMessage(`{"type":"object","properties":{"platform":{"const":"telegram"}},"additionalProperties":false}`)
	sources["channels"] = source
	if err := r.Plan.ValidateSources(sources); err != nil {
		t.Fatal(err)
	}
}
