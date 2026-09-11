package schemetransfer

import (
	"encoding/json"
	"strings"
	"testing"
)

const copyMediaID = "11111111-1111-1111-1111-111111111111"

func copyFileSources() map[string]ResourceSource {
	return map[string]ResourceSource{"files": {ValueType: "string", IdentityScope: "project", Supports: []string{"resolve", "identify"}, FileImport: &FileImport{Namespace: "messengers"}}}
}

func TestCopyFilesPreserveListPositionsAndNativeIdentities(t *testing.T) {
	settings := map[string]any{"fileIds": []string{"A", "B", "A"}, "text": "Привет, {{subject.name}}!"}
	b := NewCopyBuilder(settings).Template("/text")
	for _, path := range []string{"/fileIds/0", "/fileIds/2"} {
		b.File(path, "files", copyMediaID)
	}
	b.File("/fileIds/1", "files", "22222222-2222-2222-2222-222222222222")
	result, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if err := result.Plan.ValidateSources(copyFileSources()); err != nil {
		t.Fatal(err)
	}
	var prepared map[string]any
	_ = json.Unmarshal(result.Settings, &prepared)
	if a := prepared["fileIds"].([]any); len(a) != 3 || a[0] != nil || a[1] != nil || a[2] != nil {
		t.Fatalf("changed list: %v", a)
	}
	hydrated, err := result.HydratedSettings()
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(hydrated, &got)
	if a := got["fileIds"].([]any); a[0] != "A" || a[1] != "B" || a[2] != "A" {
		t.Fatalf("lost library IDs: %v", a)
	}
	if !strings.Contains(string(result.Settings), "subject.name") {
		t.Fatal("text changed")
	}
	if settings["fileIds"].([]string)[0] != "A" {
		t.Fatal("mutated caller")
	}
}

func TestCopyFilesRejectAmbiguousMarkers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		builder *CopyBuilder
	}{
		{"empty media", NewCopyBuilder(map[string]any{"id": "A"}).File("/id", "files", "")},
		{"URL", NewCopyBuilder(map[string]any{"id": "A"}).File("/id", "files", "https://private.invalid/file")},
		{"nil UUID", NewCopyBuilder(map[string]any{"id": "A"}).File("/id", "files", "00000000-0000-0000-0000-000000000000")},
		{"list", NewCopyBuilder(map[string]any{"ids": []string{"A"}}).File("/ids", "files", copyMediaID)},
		{"missing path", NewCopyBuilder(map[string]any{}).File("/id", "files", copyMediaID)},
		{"conflicting media", NewCopyBuilder(map[string]any{"a": "A", "b": "A"}).File("/a", "files", copyMediaID).File("/b", "files", "22222222-2222-2222-2222-222222222222")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.builder.Build(); err == nil {
				t.Fatal("accepted unsafe marker")
			}
		})
	}
	result, _ := NewCopyBuilder(map[string]any{"id": "A"}).File("/id", "files", copyMediaID).Build()
	result.Plan.References[0].Resource = ResourceSelector{Kind: "tag"}
	result.Plan.References[0].ReadAs, result.Plan.References[0].WriteAs = "id", "id"
	if result.Validate() == nil {
		t.Fatal("file attached to native tag")
	}
}

func TestCopyFilesRequireAProjectFileCatalogAndImportEndpoint(t *testing.T) {
	result, _ := NewCopyBuilder(map[string]any{"id": "A"}).File("/id", "files", copyMediaID).Build()
	sources := copyFileSources()
	if ValidateFileImportDeclaration(sources, "") == nil {
		t.Fatal("missing endpoint accepted")
	}
	if err := ValidateFileImportDeclaration(sources, "https://example.test/copy/files"); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*ResourceSource){
		func(s *ResourceSource) { s.FileImport = nil },
		func(s *ResourceSource) { s.ValueType = "number" },
		func(s *ResourceSource) { s.IdentityScope = "parent" },
		func(s *ResourceSource) { s.Parameters = map[string]SourceParameter{"self": {SourceKey: "files"}} },
		func(s *ResourceSource) { s.Supports = []string{"resolve"} },
		func(s *ResourceSource) { s.FileImport = &FileImport{Namespace: "../secret"} },
	} {
		sources := copyFileSources()
		source := sources["files"]
		edit(&source)
		sources["files"] = source
		if result.Plan.ValidateSources(sources) == nil {
			t.Fatalf("accepted %+v", source)
		}
	}
}

func TestImportCopyFileWireContract(t *testing.T) {
	req := ImportCopyFileRequest{ProtocolVersion: 2, ProjectID: copyMediaID, IntegrationVersion: 7, ImportID: copyMediaID, ResourceKey: "r0", SourceKey: "files", MediaFileID: copyMediaID}
	raw, _ := json.Marshal(req)
	result := []byte(`{"item":{"id":"libraryID","value":"libraryID","title":"File","selectable":true}}`)
	if err := ValidateCallbackResponse(ImportCopyFile, raw, result); err != nil {
		t.Fatal(err)
	}
	if err := ValidateFileImportSource(copyFileSources(), req); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		strings.Replace(string(raw), `"protocolVersion":2`, `"protocolVersion":1`, 1),
		strings.Replace(string(raw), `"resourceKey":"r0"`, `"resourceKey":"../r0"`, 1),
		strings.TrimSuffix(string(raw), "}") + `,"url":"https://source.invalid"}`,
		strings.TrimSuffix(string(raw), "}") + `,"sourceKey":"files"}`,
	} {
		if ValidateCallbackRequest(ImportCopyFile, []byte(input)) == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	for _, output := range []string{
		strings.Replace(string(result), `"selectable":true`, `"selectable":false`, 1),
		strings.TrimSuffix(string(result), "}") + `,"sourceProjectId":"private"}`,
	} {
		if ValidateCallbackResponse(ImportCopyFile, raw, []byte(output)) == nil {
			t.Fatalf("accepted %s", output)
		}
	}
}
