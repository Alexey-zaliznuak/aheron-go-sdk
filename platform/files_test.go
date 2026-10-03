package platform

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProjectFilesStayWithinAuthorizedBackendPaths(t *testing.T) {
	rawToken := testToken("files-user")
	count := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if r.Header.Get("Authorization") != "Bearer "+rawToken || !strings.HasPrefix(r.URL.Path, "/projects/"+projectID+"/files") {
			t.Error("wrong user or scope")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "DELETE":
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "usage"):
			fmt.Fprintf(w, `{"projectId":%q,"storedBytes":5,"namespaces":[]}`, projectID)
		case strings.HasSuffix(r.URL.Path, otherID):
			fmt.Fprintf(w, `{"id":%q,"fileName":"hello.txt"}`, otherID)
		default:
			fmt.Fprint(w, `{"files":[]}`)
		}
	}))
	defer api.Close()
	c, err := New(Config{BaseURL: api.URL, AllowLoopbackHTTP: true, TokenProvider: provider(t, rawToken)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = c.Files.List(ctx, projectID, ListFilesParams{Limit: 30}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Files.Get(ctx, projectID, otherID); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Files.Rename(ctx, projectID, otherID, "hello.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Files.Usage(ctx, projectID); err != nil {
		t.Fatal(err)
	}
	if err = c.Files.Delete(ctx, projectID, otherID); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Files.Get(ctx, "../escape", otherID); err == nil || count != 5 {
		t.Fatalf("invalid scope reached API: %v", err)
	}
}
