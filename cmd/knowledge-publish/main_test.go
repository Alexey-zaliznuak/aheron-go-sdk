package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	docs "github.com/Alexey-zaliznuak/aheron-go-sdk/documentation"
)

func TestUploadPhaseDoesNotActivateOrReadPublication(t *testing.T) {
	p := docs.Package{FormatVersion: docs.FormatVersion, ProviderKey: "platform/test", ContractRevision: "test/1", SourceRevision: strings.Repeat("a", 40), Documents: []docs.Document{{DocumentKey: "test", TopicKey: "test", Locale: "ru", Title: "Тест", Summary: "Пример", CommonMarkdown: "Текст"}}}
	_, raw, err := docs.CanonicalPackage(p)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/publishing/packages" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer ci-token" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(docs.UploadResult{ProviderKey: p.ProviderKey, ContractRevision: p.ContractRevision, PackageDigest: docs.SHA256(raw)})
	}))
	defer s.Close()
	c, err := docs.New(docs.Config{BaseURL: s.URL, AllowLoopbackHTTP: true, PublisherToken: func(context.Context) (string, error) { return "ci-token", nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = publishPhase(context.Background(), c, "upload", p, "", false); err != nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestInvalidPhaseRejectedBeforeCredentialsOrFiles(t *testing.T) {
	for _, phase := range []string{"", "prepare", "Activate"} {
		err := run(context.Background(), []string{"--phase", phase, "--package", "missing-file"}, io.Discard)
		if err == nil || err.Error() != "phase must be upload, activate or publish" {
			t.Fatalf("unexpected error %v", err)
		}
	}
}

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
