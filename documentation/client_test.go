package docs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientCredentialsAndImmutableRead(t *testing.T) {
	ref := DocumentRef{"platform/execution", strings.Repeat("a", 64), "blocks/action", "ru"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/public/knowledge/read":
			if r.Header.Get("Authorization") != "" {
				t.Error("publisher token sent to public API")
			}
			_ = json.NewEncoder(w).Encode(ReadResult{Document: DocumentSummary{Ref: ref}, Audience: "agent", Markdown: "text", ContentSHA256: SHA256([]byte("text"))})
		case "/publishing/packages":
			if r.Header.Get("Authorization") != "Bearer ci-token" {
				t.Error("publisher credential missing")
			}
			var p Package
			_ = json.NewDecoder(r.Body).Decode(&p)
			_, raw, _ := CanonicalPackage(p)
			_ = json.NewEncoder(w).Encode(UploadResult{ProviderKey: p.ProviderKey, ContractRevision: p.ContractRevision, PackageDigest: SHA256(raw)})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	c, err := New(Config{BaseURL: server.URL, AllowLoopbackHTTP: true, PublisherToken: func(context.Context) (string, error) { return "ci-token", nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Read(context.Background(), ReadRequest{Ref: ref, Audience: "agent"}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Upload(context.Background(), fixture()); err != nil {
		t.Fatal(err)
	}
}

func TestClientRejectsRedirectOversizeAndBadHashes(t *testing.T) {
	for _, mode := range []string{"redirect", "oversize", "hash"} {
		t.Run(mode, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "redirect":
					http.Redirect(w, r, "/redirect-target", http.StatusFound)
				case "oversize":
					_, _ = w.Write([]byte(strings.Repeat("x", MaxResponseBytes+1)))
				default:
					_ = json.NewEncoder(w).Encode(ReadResult{Audience: "agent", Markdown: "tampered", ContentSHA256: SHA256([]byte("original"))})
				}
			}))
			defer s.Close()
			c, _ := New(Config{BaseURL: s.URL, AllowLoopbackHTTP: true})
			if _, err := c.Read(context.Background(), ReadRequest{Audience: "agent"}); err == nil {
				t.Fatal("unsafe response accepted")
			}
		})
	}
}

func TestClientCancellationAndURLPolicy(t *testing.T) {
	for _, base := range []string{"http://example.com/api", "https://user:secret@example.com", "https://example.com?token=x"} {
		if _, err := New(Config{BaseURL: base, AllowLoopbackHTTP: true}); err == nil {
			t.Fatalf("accepted %s", base)
		}
	}
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer s.Close()
	defer close(release)
	c, _ := New(Config{BaseURL: s.URL, AllowLoopbackHTTP: true, Timeout: 20 * time.Millisecond})
	_, err := c.Catalog(context.Background(), CatalogRequest{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
