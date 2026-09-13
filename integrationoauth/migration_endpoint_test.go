package integrationoauth

import "testing"

func TestMigrationProofEndpoint(t *testing.T) {
	for _, raw := range []string{"", "https://integration.example/oauth-migration"} {
		if err := ValidateMigrationEndpoint(raw, "https://integration.example/install"); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{"http://integration.example/proof", "https://user:password@integration.example/proof", "https://integration.example/proof?", "https://integration.example/proof#", "https://integration.example/{projectId}", " https://integration.example/proof", "https://INTEGRATION.example:443/%69nstall", "https://integration.example/a/../install"} {
		if err := ValidateMigrationEndpoint(raw, "https://integration.example/install"); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}
