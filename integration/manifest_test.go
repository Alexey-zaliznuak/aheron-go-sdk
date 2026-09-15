package integration

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"
)

func TestManifestResolvePathsAgainstPublicBaseURL(t *testing.T) {
	m := Manifest{
		ConsolePath:          "/console",
		ActionPath:           "/api/actions",
		TriggerSyncPath:      "/trigger-sync",
		VariableValuesPath:   "/variable-values",
		VariableValueSources: map[string]VariableValueSource{"sheet": RemoteVariableValues()},
		Blocks: []Block{
			// IframePath left empty: the "/blocks/<key>" default is what every
			// integration uses, so most blocks never set it.
			{Key: "create-row", Kind: KindAction, Name: "Create row", Outputs: []string{"ok", "failed"}},
			{Key: "on-row", Kind: KindTrigger, Name: "Row added", IframePath: "/custom/on-row"},
		},
	}

	body, err := m.resolve("https://sheets.aheron.pro/")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if body.ActionURL != "https://sheets.aheron.pro/api/actions" {
		t.Errorf("ActionURL = %q", body.ActionURL)
	}
	if body.ConsoleURL != "https://sheets.aheron.pro/console" {
		t.Errorf("ConsoleURL = %q", body.ConsoleURL)
	}
	if body.Blocks[0].IframeURL != "https://sheets.aheron.pro/blocks/create-row" {
		t.Errorf("default IframeURL = %q", body.Blocks[0].IframeURL)
	}
	if body.Blocks[1].IframeURL != "https://sheets.aheron.pro/custom/on-row" {
		t.Errorf("explicit IframeURL = %q", body.Blocks[1].IframeURL)
	}
	if body.Blocks[0].Kind != "action" || body.Blocks[1].Kind != "trigger" {
		t.Errorf("kinds = %q, %q", body.Blocks[0].Kind, body.Blocks[1].Kind)
	}

	// Ports must be sent as [] rather than null: the platform stores them as JSON
	// documents where an empty list is what "no ports" means.
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"inputs":[]`) {
		t.Errorf("empty inputs should marshal as []: %s", raw)
	}
}

func TestManifestResolveConsolePages(t *testing.T) {
	m := Manifest{
		ConsolePath: "/console",
		ConsolePages: []ConsolePage{
			{Key: "dialogs", Label: "Диалоги", Path: "/console/dialogs",
				Icon: Icon{MimeType: "image/svg+xml", Content: []byte("<svg/>")}},
			{Key: "files", Label: "Файлы", Path: "/console/files"},
		},
	}

	body, err := m.resolve("https://messengers.aheron.pro")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(body.ConsolePages) != 2 {
		t.Fatalf("ConsolePages = %+v", body.ConsolePages)
	}
	// Order is the declaration order: it is what the sidebar shows.
	if body.ConsolePages[0].Key != "dialogs" || body.ConsolePages[1].Key != "files" {
		t.Errorf("pages lost their declared order: %+v", body.ConsolePages)
	}
	if body.ConsolePages[0].URL != "https://messengers.aheron.pro/console/dialogs" {
		t.Errorf("page URL = %q", body.ConsolePages[0].URL)
	}
	if body.ConsolePages[0].Icon == nil || body.ConsolePages[0].Icon.MimeType != "image/svg+xml" {
		t.Errorf("icon was not carried: %+v", body.ConsolePages[0].Icon)
	}
	// A page may ship without artwork; the platform renders a fallback.
	if body.ConsolePages[1].Icon != nil {
		t.Errorf("page without an icon should send none: %+v", body.ConsolePages[1].Icon)
	}

	// Icon bytes travel as base64, and a page without an icon omits the field.
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), base64.StdEncoding.EncodeToString([]byte("<svg/>"))) {
		t.Errorf("icon content should marshal as base64: %s", raw)
	}
	if strings.Count(string(raw), `"icon"`) != 1 {
		t.Errorf("exactly one page declares an icon: %s", raw)
	}
}

func TestManifestResolveRejectsBadConsolePages(t *testing.T) {
	tooMany := make([]ConsolePage, maxConsolePages+1)
	for i := range tooMany {
		tooMany[i] = ConsolePage{Key: string(rune('a' + i)), Label: "L", Path: "/p"}
	}

	cases := map[string][]ConsolePage{
		"no key":   {{Label: "Диалоги", Path: "/console/dialogs"}},
		"no label": {{Key: "dialogs", Path: "/console/dialogs"}},
		"no path":  {{Key: "dialogs", Label: "Диалоги"}},
		"duplicate key": {
			{Key: "dialogs", Label: "Диалоги", Path: "/console/dialogs"},
			{Key: "dialogs", Label: "Чаты", Path: "/console/chats"},
		},
		"path without leading slash": {{Key: "dialogs", Label: "Диалоги", Path: "console/dialogs"}},
		"unsupported icon type": {{Key: "dialogs", Label: "Диалоги", Path: "/console/dialogs",
			Icon: Icon{MimeType: "image/gif", Content: []byte("GIF89a")}}},
		"icon type without content": {{Key: "dialogs", Label: "Диалоги", Path: "/console/dialogs",
			Icon: Icon{MimeType: "image/png"}}},
		"icon content without type": {{Key: "dialogs", Label: "Диалоги", Path: "/console/dialogs",
			Icon: Icon{Content: []byte("<svg/>")}}},
		"icon too large": {{Key: "dialogs", Label: "Диалоги", Path: "/console/dialogs",
			Icon: Icon{MimeType: "image/png", Content: make([]byte, maxIconBytes+1)}}},
		"too many pages": tooMany,
	}

	for name, pages := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := (Manifest{ConsolePages: pages}).resolve("https://messengers.aheron.pro"); err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestIconFromFS(t *testing.T) {
	fsys := fstest.MapFS{
		"icons/dialogs.svg": &fstest.MapFile{Data: []byte("<svg/>")},
		"icons/dialogs.png": &fstest.MapFile{Data: []byte{0x89, 'P', 'N', 'G'}},
		"icons/notes.txt":   &fstest.MapFile{Data: []byte("nope")},
	}

	icon, err := IconFromFS(fsys, "icons/dialogs.svg")
	if err != nil {
		t.Fatalf("IconFromFS: %v", err)
	}
	if icon.MimeType != "image/svg+xml" || string(icon.Content) != "<svg/>" {
		t.Errorf("icon = %+v", icon)
	}
	if icon, err := IconFromFS(fsys, "icons/dialogs.png"); err != nil || icon.MimeType != "image/png" {
		t.Errorf("png icon = %+v, err = %v", icon, err)
	}
	if _, err := IconFromFS(fsys, "icons/notes.txt"); err == nil {
		t.Error("an unsupported extension must be rejected")
	}
	if _, err := IconFromFS(fsys, "icons/missing.svg"); err == nil {
		t.Error("a missing file must be reported")
	}
}

func TestManifestResolveKeepsAbsoluteAndOmittedEndpoints(t *testing.T) {
	m := Manifest{
		Blocks: []Block{{Key: "b", Kind: KindAction, Name: "B"}},
	}

	body, err := m.resolve("https://sheets.aheron.pro")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// An undeclared endpoint is omitted, which the platform reads as "clear it".
	if body.ActionURL != "" || body.TriggerSyncURL != "" {
		t.Errorf("undeclared endpoints should be empty: %+v", body)
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "actionUrl") {
		t.Errorf("empty actionUrl should be omitted: %s", raw)
	}
}

func TestManifestEmissionOmitsRetiredLegacyEndpoints(t *testing.T) {
	body, err := (Manifest{
		InstallationLifecyclePath: "/installation-lifecycle",
		ActionPath:                "/api/actions",
		TriggerSyncPath:           "/trigger-sync",
		Blocks:                    []Block{{Key: "b", Kind: KindAction, Name: "B"}},
	}).resolve("https://integration.example")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(raw)
	for _, field := range []string{"installUrl", "uninstallUrl", "oauthMigrationUrl"} {
		if strings.Contains(encoded, field) {
			t.Fatalf("emitted manifest contains retired field %q: %s", field, encoded)
		}
	}
	for _, field := range []string{"installationLifecycleUrl", "actionUrl", "triggerSyncUrl"} {
		if !strings.Contains(encoded, field) {
			t.Fatalf("emitted manifest omitted active field %q: %s", field, encoded)
		}
	}
}

func TestManifestResolveRejectsBadManifests(t *testing.T) {
	cases := map[string]Manifest{
		"relative path without base": {
			ActionPath: "/api/actions",
		},
		"path without leading slash": {
			ActionPath: "api/actions",
		},
		"duplicate block key": {
			Blocks: []Block{
				{Key: "b", Kind: KindAction, Name: "B"},
				{Key: "b", Kind: KindTrigger, Name: "B again"},
			},
		},
		"unknown kind": {
			Blocks: []Block{{Key: "b", Kind: Kind("webhook"), Name: "B"}},
		},
		"block without name": {
			Blocks: []Block{{Key: "b", Kind: KindAction}},
		},
		"block without key": {
			Blocks: []Block{{Kind: KindAction, Name: "B"}},
		},
		"declared and retired at once": {
			Blocks:  []Block{{Key: "b", Kind: KindAction, Name: "B"}},
			Retired: []string{"b"},
		},
		"sources without endpoint": {
			VariableValueSources: map[string]VariableValueSource{"x": RemoteVariableValues()},
		},
		"invalid action template": {
			ActionRequestTemplate: json.RawMessage(`{"broken":`),
		},
	}

	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			base := "https://sheets.aheron.pro"
			if name == "relative path without base" {
				base = ""
			}
			if _, err := m.resolve(base); err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestManifestBlockKeys(t *testing.T) {
	m := Manifest{Blocks: []Block{
		{Key: "a", Kind: KindAction, Name: "A"},
		{Key: "b", Kind: KindTrigger, Name: "B"},
	}}
	got := m.BlockKeys()
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("BlockKeys() = %v", got)
	}
}

func TestManifestFavoriteByDefault(t *testing.T) {
	for _, favorite := range []bool{false, true} {
		body, err := (Manifest{Blocks: []Block{{Key: "send-message", Name: "Send", Kind: KindAction, FavoriteByDefault: favorite}}}).resolve("https://example.com")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		var decoded struct {
			Blocks []struct {
				FavoriteByDefault bool `json:"favoriteByDefault"`
			} `json:"blocks"`
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if len(decoded.Blocks) != 1 || decoded.Blocks[0].FavoriteByDefault != favorite {
			t.Fatalf("flag lost: %s", raw)
		}
	}
}
