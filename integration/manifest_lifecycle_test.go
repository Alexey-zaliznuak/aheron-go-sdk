package integration

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLifecycleManifest(t *testing.T) {
	m := Manifest{InstallPath: "/install", UninstallPath: "/uninstall", InstallationLifecyclePath: "/installation-lifecycle"}
	body, err := m.resolve("https://integration.example")
	if err != nil {
		t.Fatal(err)
	}
	if body.InstallationLifecycleURL != "https://integration.example/installation-lifecycle" {
		t.Fatal("missing lifecycle declaration")
	}
	raw, err := json.Marshal(body)
	if err != nil || !strings.Contains(string(raw), `"installationLifecycleUrl":`) {
		t.Fatal("missing wire declaration", err)
	}
	for _, path := range []string{"/install", "/uninstall"} {
		m.InstallationLifecyclePath = path
		if _, err := m.resolve("https://integration.example"); err == nil {
			t.Fatal("accepted legacy endpoint reuse")
		}
	}
	m.InstallationLifecyclePath = ""
	body, err = m.resolve("https://integration.example")
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(body)
	if err != nil || strings.Contains(string(raw), `"installationLifecycleUrl"`) {
		t.Fatal("legacy manifest gained lifecycle declaration", err)
	}
}
