package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/schemetransfer"
)

const copyFileRequest = `{"protocolVersion":2,"projectId":"11111111-1111-1111-1111-111111111111","integrationVersion":7,"importId":"22222222-2222-2222-2222-222222222222","resourceKey":"r0","sourceKey":"files","mediaFileId":"33333333-3333-3333-3333-333333333333"}`

func TestImportCopyFileSignedTransport(t *testing.T) {
	v, priv, kid := newVariableValuesVerifier(t)
	calls := 0
	callback := func(_ context.Context, req schemetransfer.ImportCopyFileRequest) (schemetransfer.ImportCopyFileResponse, error) {
		calls++
		if req.SourceKey != "files" || req.ResourceKey != "r0" || req.MediaFileID != "33333333-3333-3333-3333-333333333333" {
			t.Fatal("lost receipt")
		}
		return schemetransfer.ImportCopyFileResponse{Item: schemetransfer.ResourceItem{ID: "localID", Value: json.RawMessage(`"localID"`), Title: "Photo", Selectable: true}}, nil
	}
	handler := v.HandleImportCopyFile(supportedCopyVersion, callback)
	for range 2 {
		rec := serveVariableValues(t, handler, priv, kid, []byte(copyFileRequest))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"localID"`) {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	}
	// The transport does not suppress a replay: the persistent domain receipt
	// must answer it, including after process restart or manual file edits.
	if calls != 2 {
		t.Fatal("unexpected transport idempotency cache")
	}
	for _, body := range []string{
		strings.Replace(copyFileRequest, `"integrationVersion":7`, `"integrationVersion":8`, 1),
		strings.Replace(copyFileRequest, `"resourceKey":"r0"`, `"resourceKey":"../x"`, 1),
		strings.TrimSuffix(copyFileRequest, "}") + `,"url":"secret"}`,
	} {
		rec := serveVariableValues(t, handler, priv, kid, []byte(body))
		if rec.Code != 400 && rec.Code != 409 {
			t.Fatalf("unsafe request: %d %s", rec.Code, rec.Body)
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/copy/files/import", strings.NewReader(copyFileRequest)))
	if rec.Code != 401 || calls != 2 {
		t.Fatal("unauthenticated or invalid request reached domain")
	}
}

func TestImportCopyFileSanitizedErrors(t *testing.T) {
	v, priv, kid := newVariableValuesVerifier(t)
	for _, tc := range []struct {
		err    error
		status int
	}{
		{ErrCopyConflict, 409}, {ErrCopyNotFound, 404}, {ErrCopyUnavailable, 503}, {errors.New("secret URL or project key"), 500},
	} {
		handler := v.HandleImportCopyFile(supportedCopyVersion, func(context.Context, schemetransfer.ImportCopyFileRequest) (schemetransfer.ImportCopyFileResponse, error) {
			return schemetransfer.ImportCopyFileResponse{}, tc.err
		})
		rec := serveVariableValues(t, handler, priv, kid, []byte(copyFileRequest))
		if rec.Code != tc.status || strings.Contains(rec.Body.String(), "secret") {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	}
}

func TestManifestFileImportEndpoint(t *testing.T) {
	m := Manifest{ResourceValuesPath: "/copy/resources", ResourceSources: map[string]schemetransfer.ResourceSource{"files": {ValueType: "string", IdentityScope: "project", Supports: []string{"identify", "resolve"}, FileImport: &schemetransfer.FileImport{Namespace: "messengers"}}}}
	if _, err := m.resolve("https://example.test"); err == nil {
		t.Fatal("accepted missing endpoint")
	}
	m.ImportCopyFilePath = "/copy/files/import"
	body, err := m.resolve("https://example.test")
	if err != nil {
		t.Fatal(err)
	}
	if body.ImportCopyFileURL != "https://example.test/copy/files/import" {
		t.Fatal("wrong endpoint")
	}
	raw, _ := json.Marshal(body)
	if !strings.Contains(string(raw), `"importCopyFileUrl"`) || !strings.Contains(string(raw), `"fileImport":{"namespace":"messengers"}`) {
		t.Fatalf("missing contract: %s", raw)
	}
}
