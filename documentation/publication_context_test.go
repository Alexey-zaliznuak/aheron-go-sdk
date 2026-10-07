package docs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicationContextIsBoundAndNeverSentOnPublicReads(t *testing.T) {
	p := fixture()
	metadata := PublicationContext{ProviderKey: p.ProviderKey, SourceRevision: p.SourceRevision, ReleaseSequence: 42}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if strings.HasPrefix(r.URL.Path, "/public/") {
			for _, h := range []string{"Authorization", "X-Documentation-Provider", "X-Documentation-Source-Revision", "X-Documentation-Release-Sequence"} {
				if r.Header.Get(h) != "" {
					t.Errorf("public read leaked %s", h)
				}
			}
			_, _ = w.Write([]byte(`{}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-personal-key" || r.Header.Get("X-Documentation-Provider") != p.ProviderKey || r.Header.Get("X-Documentation-Source-Revision") != p.SourceRevision || r.Header.Get("X-Documentation-Release-Sequence") != "42" {
			t.Error("incorrect publication identity or credential")
		}
		_, raw, _ := CanonicalPackage(p)
		_ = json.NewEncoder(w).Encode(UploadResult{ProviderKey: p.ProviderKey, PackageDigest: SHA256(raw), ContractRevision: p.ContractRevision})
	}))
	defer srv.Close()
	c, err := New(Config{BaseURL: srv.URL, AllowLoopbackHTTP: true, Publication: &metadata, PublisherToken: func(context.Context) (string, error) { return "test-personal-key", nil }})
	if err != nil {
		t.Fatal(err)
	}
	metadata.ProviderKey = "platform/rebound"
	if _, err := c.Upload(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Catalog(context.Background(), CatalogRequest{ProviderKey: p.ProviderKey, ContractRevision: p.ContractRevision}); err != nil {
		t.Fatal(err)
	}
	other := p
	other.SourceRevision = strings.Repeat("f", 40)
	if other.SourceRevision == p.SourceRevision {
		other.SourceRevision = strings.Repeat("e", 40)
	}
	if _, err := c.Upload(context.Background(), other); err == nil {
		t.Fatal("accepted mismatched upload")
	}
	if _, err := c.ActivatePackage(context.Background(), other, "release-42"); err == nil {
		t.Fatal("accepted mismatched activation")
	}
	if calls != 2 {
		t.Fatalf("mismatched package reached network: %d calls", calls)
	}
}

func TestPublicationContextRejectsIncompleteIdentity(t *testing.T) {
	p := fixture()
	for _, in := range []PublicationContext{
		{ProviderKey: p.ProviderKey, SourceRevision: p.SourceRevision},
		{ProviderKey: p.ProviderKey, SourceRevision: p.SourceRevision, ReleaseSequence: -1},
		{ProviderKey: "bad\r\nheader", SourceRevision: p.SourceRevision, ReleaseSequence: 1},
		{ProviderKey: p.ProviderKey, SourceRevision: "short", ReleaseSequence: 1},
	} {
		if _, err := New(Config{BaseURL: "https://docs.example/api", Publication: &in}); err == nil {
			t.Fatal("accepted invalid publication context")
		}
	}
}
