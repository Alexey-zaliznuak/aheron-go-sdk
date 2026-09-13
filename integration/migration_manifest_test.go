package integration

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMigrationProofManifest(t *testing.T) {
	body, err := (Manifest{OAuthMigrationPath: "/oauth-migration", InstallPath: "/install"}).resolve("https://integration.example")
	if err != nil || body.OAuthMigrationURL != "https://integration.example/oauth-migration" {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(body)
	if !strings.Contains(string(raw), `"oauthMigrationUrl":"https://integration.example/oauth-migration"`) {
		t.Fatal("missing camelCase declaration")
	}
	for _, endpoint := range []string{"/install", "http://integration.example/proof", "/proof?key=value", "/proof#fragment"} {
		if _, err := (Manifest{OAuthMigrationPath: endpoint, InstallPath: "/install"}).resolve("https://integration.example"); err == nil {
			t.Fatal("accepted invalid receiver", endpoint)
		}
	}
	body, err = (Manifest{}).resolve("https://integration.example")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(body)
	if strings.Contains(string(raw), "oauthMigrationUrl") {
		t.Fatal("legacy manifest gained a capability")
	}
}
