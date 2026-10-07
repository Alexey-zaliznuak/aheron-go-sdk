package docs

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func fixture() Package {
	return Package{FormatVersion: 1, ProviderKey: "platform/execution", ContractRevision: "native-blocks/1", SourceRevision: strings.Repeat("a", 40), Documents: []Document{{DocumentKey: "blocks/action", TopicKey: "blocks/action", Locale: "ru", Title: "Действие", Summary: "Переменные", CommonMarkdown: "```python\r\n  return vars\r\n```", AgentAppendixMarkdown: "<agent>"}}}
}

func TestCanonicalV1Vector(t *testing.T) {
	in := fixture()
	p, raw, err := CanonicalPackage(in)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"formatVersion":1,"providerKey":"platform/execution","contractRevision":"native-blocks/1","sourceRevision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","documents":[{"documentKey":"blocks/action","topicKey":"blocks/action","locale":"ru","title":"Действие","summary":"Переменные","commonMarkdown":"` + "```python\\n  return vars\\n```" + `","humanAppendixMarkdown":"","agentAppendixMarkdown":"\u003cagent\u003e","relatedTopics":[],"requires":[]}]}`
	if string(raw) != want {
		t.Fatalf("canonical bytes changed:\n%s\nwant:\n%s", raw, want)
	}
	if !strings.Contains(in.Documents[0].CommonMarkdown, "\r\n") {
		t.Fatal("caller document mutated")
	}
	_, twice, err := CanonicalPackage(p)
	if err != nil || !bytes.Equal(raw, twice) {
		t.Fatalf("non-idempotent canonicalization: %v", err)
	}
	human, _ := Render(p.Documents[0], "human")
	agent, _ := Render(p.Documents[0], "agent")
	if strings.Contains(human, "<agent>") || !strings.HasSuffix(agent, "\n\n<agent>") {
		t.Fatal("audience isolation failed")
	}
	if _, err := Render(p.Documents[0], "typo"); err == nil {
		t.Fatal("invalid audience accepted")
	}
}

func TestCanonicalRejectsAmbiguousOrOversizedPackages(t *testing.T) {
	for name, mutate := range map[string]func(*Package){
		"duplicate":          func(p *Package) { p.Documents = append(p.Documents, p.Documents[0]) },
		"unversioned":        func(p *Package) { p.FormatVersion = 2 },
		"invalid utf8":       func(p *Package) { p.Documents[0].CommonMarkdown = string([]byte{0xff}) },
		"too many":           func(p *Package) { p.Documents = make([]Document, 65) },
		"too large":          func(p *Package) { p.Documents[0].CommonMarkdown = strings.Repeat("x", MaxPackageBytes) },
		"traversal":          func(p *Package) { p.Documents[0].DocumentKey = "../secret" },
		"missing block key":  func(p *Package) { p.Documents[0].Kind = "blockGuide" },
		"overview block key": func(p *Package) { p.Documents[0].Kind = "overview"; p.Documents[0].BlockKey = "send" },
		"unknown kind":       func(p *Package) { p.Documents[0].Kind = "automatic" },
		"duplicate dependency": func(p *Package) {
			p.Documents[0].Requires = []Contract{{"platform/code", "harness/1"}, {"platform/code", "harness/2"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := fixture()
			mutate(&p)
			if _, _, err := CanonicalPackage(p); err == nil {
				t.Fatal("accepted invalid package")
			}
		})
	}
}

func TestCanonicalOrderingAndDigestCoverage(t *testing.T) {
	p := fixture()
	d := p.Documents[0]
	d.DocumentKey = "blocks/z"
	p.Documents = append([]Document{d}, p.Documents...)
	p.Documents[1].RelatedTopics = []string{"z", "a"}
	canonical, raw, err := CanonicalPackage(p)
	if err != nil {
		t.Fatal(err)
	}
	if canonical.Documents[0].DocumentKey != "blocks/action" || canonical.Documents[0].RelatedTopics[0] != "a" || p.Documents[1].RelatedTopics[0] != "z" {
		t.Fatal("unstable order or caller mutation")
	}
	before := DocumentSHA256(canonical.Documents[0])
	canonical.Documents[0].Summary = "changed"
	if before == DocumentSHA256(canonical.Documents[0]) {
		t.Fatal("document metadata absent from hash")
	}
	var decoded Package
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	_, again, _ := CanonicalPackage(decoded)
	if SHA256(raw) != SHA256(again) {
		t.Fatal("digest changed on round trip")
	}
}
