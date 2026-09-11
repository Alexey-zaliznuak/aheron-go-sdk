package transfermedia

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const projectID = "11111111-1111-1111-1111-111111111111"
const operationID = "22222222-2222-2222-2222-222222222222"
const assetID = "33333333-3333-3333-3333-333333333333"
const fileID = "44444444-4444-4444-4444-444444444444"
const assetJSON = `{"snapshotId":"22222222-2222-2222-2222-222222222222","assetId":"33333333-3333-3333-3333-333333333333","fileName":"document.pdf","mimeType":"application/pdf","contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","kind":"file","sizeBytes":100}`
const fileJSON = `{"id":"44444444-4444-4444-4444-444444444444","namespace":"messengers","fileName":"renamed.pdf","mimeType":"application/pdf","sizeBytes":123,"kind":"file","url":"https://media.test/project/file","createdAt":"2026-09-11T10:00:00Z"}`

func TestCaptureAndImportUseStableAddressesAndCurrentFile(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		if r.Header.Get("X-Internal-Token") != "private-token" || r.Header.Get("Authorization") != "" {
			t.Error("wrong auth")
		}
		if r.Method == http.MethodPut {
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			if strings.Contains(r.URL.Path, "snapshots") {
				if body["sourceProjectId"] != projectID || body["sourceFileId"] != fileID || len(body) != 2 {
					t.Errorf("capture body: %s", raw)
				}
			} else {
				if body["snapshotId"] != operationID || body["assetId"] != assetID || body["namespace"] != "messengers" || len(body) != 3 {
					t.Errorf("import body: %s", raw)
				}
			}
		}
		if strings.Contains(r.URL.Path, "snapshots") {
			_, _ = io.WriteString(w, assetJSON)
		} else {
			_, _ = io.WriteString(w, fileJSON)
		}
	}))
	defer srv.Close()
	c, err := New(srv.URL, "private-token", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := c.Capture(context.Background(), operationID, assetID, CaptureRequest{projectID, fileID}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.GetAsset(context.Background(), operationID, assetID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		f, err := c.Import(context.Background(), projectID, operationID, "r0", ImportRequest{operationID, assetID, "messengers"})
		if err != nil || f.ID != fileID || f.FileName != "renamed.pdf" || f.SizeBytes != 123 {
			t.Fatalf("lost current file: %+v %v", f, err)
		}
	}
	if _, err := c.GetImport(context.Background(), projectID, operationID, "r0"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 6 || seen[0] != seen[1] || seen[3] != seen[4] {
		t.Fatalf("unstable receipt: %v", seen)
	}
	if seen[0] != "PUT /internal/template-file-snapshots/"+operationID+"/assets/"+assetID || seen[3] != "PUT /internal/projects/"+projectID+"/template-file-imports/"+operationID+"/resources/r0" {
		t.Fatalf("wrong routes: %v", seen)
	}
}

func TestClientRejectsInvalidInputBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer srv.Close()
	c, _ := New(srv.URL, "token", srv.Client())
	for _, id := range []string{"", "../secret", "00000000-0000-0000-0000-000000000000", "https://foreign.test"} {
		if _, err := c.Capture(context.Background(), id, assetID, CaptureRequest{projectID, fileID}); !errors.Is(err, ErrInvalidRequest) {
			t.Fatal(err)
		}
		if _, err := c.Import(context.Background(), projectID, operationID, "r0", ImportRequest{id, assetID, "messengers"}); !errors.Is(err, ErrInvalidRequest) {
			t.Fatal(err)
		}
	}
	for _, namespace := range []string{"../x", "with space", "a/secret", "Upper", "a-"} {
		if _, err := c.Import(context.Background(), projectID, operationID, "r0", ImportRequest{operationID, assetID, namespace}); !errors.Is(err, ErrInvalidRequest) {
			t.Fatal(err)
		}
	}
	if _, err := c.GetImport(context.Background(), projectID, operationID, "../r0"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input reached HTTP")
	}
	for _, base := range []string{"ftp://host", "https://user:pass@host", "https://host?x=y", "https://host/#fragment"} {
		if _, err := New(base, "token", srv.Client()); err == nil {
			t.Fatal("invalid base URL")
		}
	}
}

func TestClientDoesNotFollowRedirectOrLeakErrors(t *testing.T) {
	var leaked atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Add(1) }))
	defer foreign.Close()
	for _, tc := range []struct {
		status int
		want   error
	}{{302, ErrUnavailable}, {400, ErrInvalidRequest}, {404, ErrNotFound}, {409, ErrConflict}, {413, ErrInvalidRequest}, {500, ErrUnavailable}, {503, ErrUnavailable}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", foreign.URL)
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, "secret-token-and-source-url")
		}))
		original := srv.Client()
		c, _ := New(srv.URL, "secret-token", original)
		if original.CheckRedirect != nil || original.Timeout != 0 {
			t.Fatal("mutated caller client")
		}
		_, err := c.GetImport(context.Background(), projectID, operationID, "r0")
		if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe error: %v", err)
		}
		srv.Close()
	}
	if leaked.Load() != 0 {
		t.Fatal("followed redirect with internal credential")
	}
}

func TestClientRejectsMalformedOrMismatchedSuccess(t *testing.T) {
	for _, body := range []string{
		strings.Replace(assetJSON, operationID, projectID, 1),
		strings.TrimSuffix(assetJSON, "}") + `,"sourceFileId":"private"}`,
		strings.TrimSuffix(assetJSON, "}") + `,"sizeBytes":101}`,
		assetJSON + ` {}`,
		strings.Repeat(" ", maxResponseBytes) + assetJSON,
		strings.Replace(assetJSON, `"sizeBytes":100`, `"sizeBytes":-1`, 1),
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
		c, _ := New(srv.URL, "token", srv.Client())
		if _, err := c.GetAsset(context.Background(), operationID, assetID); !errors.Is(err, ErrUnavailable) {
			t.Fatal("accepted malformed asset")
		}
		srv.Close()
	}
	for _, body := range []string{
		strings.Replace(fileJSON, `"namespace":"messengers"`, `"namespace":"library"`, 1),
		strings.Replace(fileJSON, `"id":"`+fileID+`"`, `"id":""`, 1),
		strings.Replace(fileJSON, `https://media.test/project/file`, `javascript:alert(1)`, 1),
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
		c, _ := New(srv.URL, "token", srv.Client())
		if _, err := c.Import(context.Background(), projectID, operationID, "r0", ImportRequest{operationID, assetID, "messengers"}); !errors.Is(err, ErrUnavailable) {
			t.Fatal("accepted malformed file")
		}
		srv.Close()
	}
}

func TestClientCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("cancelled request reached server") }))
	defer srv.Close()
	c, _ := New(srv.URL, "token", srv.Client())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.GetAsset(ctx, operationID, assetID); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}
