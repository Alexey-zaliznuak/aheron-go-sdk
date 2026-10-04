package main

import (
	"strings"
	"testing"

	docs "github.com/Alexey-zaliznuak/aheron-go-sdk/documentation"
)

func TestReadPackageRejectsAmbiguousOrOversizeInput(t *testing.T) {
	for _, raw := range []string{`{}`, `{"unknown":1}`, `{} {}`, `null`, strings.Repeat(" ", docs.MaxPackageBytes+1)} {
		if _, err := readPackage(strings.NewReader(raw)); err == nil {
			t.Fatal("accepted invalid package")
		}
	}
	p := docs.Package{FormatVersion: docs.FormatVersion, ProviderKey: "platform/test", ContractRevision: "test/1", SourceRevision: strings.Repeat("a", 40), Documents: []docs.Document{{DocumentKey: "test", TopicKey: "test", Locale: "ru", Title: "Тест", Summary: "Пример", CommonMarkdown: "Текст"}}}
	_, raw, err := docs.CanonicalPackage(p)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := readPackage(strings.NewReader(string(raw))); err != nil || got.SourceRevision != p.SourceRevision {
		t.Fatalf("%+v %v", got, err)
	}
}
