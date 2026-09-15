package integration

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLifecycleManifest(t *testing.T) {
	m := Manifest{InstallationLifecyclePath: "/installation-lifecycle"}
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
	m.InstallationLifecyclePath = ""
	body, err = m.resolve("https://integration.example")
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(body)
	if err != nil || strings.Contains(string(raw), `"installationLifecycleUrl"`) {
		t.Fatal("manifest gained lifecycle declaration", err)
	}
}
