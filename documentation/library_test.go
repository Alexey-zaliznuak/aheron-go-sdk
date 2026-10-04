package docs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLibraryClientIdentityHashAndPublicTransport(t *testing.T) {
	in := LibraryReadRequest{Ref: LibraryRef{Source: "article", ArticleID: "article", Revision: 7}, Profile: LibraryProfile{Locale: "ru"}, Audience: "agent"}
	out := LibraryReadResult{Document: LibraryDocument{Ref: in.Ref}, Audience: "agent", Markdown: "return vars", ContentSHA256: SHA256([]byte("return vars"))}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Authorization") != "" {
			t.Error("library used non-public transport")
		}
		if r.URL.Path != "/public/knowledge/library/read" {
			t.Errorf("path: %s", r.URL.Path)
		}
		var got LibraryReadRequest
		if json.NewDecoder(r.Body).Decode(&got) != nil || got.Ref != in.Ref {
			t.Error("reference was not sent")
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, AllowLoopbackHTTP: true, PublisherToken: func(context.Context) (string, error) { t.Fatal("library requested publisher token"); return "", nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.LibraryRead(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	out.Document.Ref.Revision++
	if _, err = client.LibraryRead(context.Background(), in); err == nil {
		t.Fatal("substituted revision accepted")
	}
	out.Document.Ref = in.Ref
	out.Markdown = "changed"
	if _, err = client.LibraryRead(context.Background(), in); err == nil {
		t.Fatal("invalid content hash accepted")
	}
}
