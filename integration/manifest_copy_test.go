package integration

import (
	"testing"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/schemetransfer"
)

func TestManifestCopyContractsResolveAndKeepVersionFields(t *testing.T) {
	r := &schemetransfer.CopyRules{Version: 1, Mode: "literal", Validation: &schemetransfer.Validation{Mode: "integration"}}
	m := Manifest{ResourceValuesPath: "/resources", PrepareCopyPath: "/copy/prepare", ValidateCopySettingsPath: "/copy/validate", ResourceSources: map[string]schemetransfer.ResourceSource{"channels": {ValueType: "string", IdentityScope: "project", Supports: []string{"search", "resolve"}}}, Blocks: []Block{{Key: "send", Name: "Send", Kind: KindAction, CopyRules: r}}}
	body, err := m.resolve("https://messengers.example")
	if err != nil {
		t.Fatal(err)
	}
	if body.ResourceValuesURL != "https://messengers.example/resources" || body.PrepareCopyURL != "https://messengers.example/copy/prepare" || body.ValidateCopySettingsURL != "https://messengers.example/copy/validate" || body.Blocks[0].CopyRules != r || len(body.ResourceSources) != 1 {
		t.Fatalf("lost contract: %#v", body)
	}
	m.ValidateCopySettingsPath = ""
	if _, err = m.resolve("https://messengers.example"); err == nil {
		t.Fatal("missing validator accepted")
	}
}
