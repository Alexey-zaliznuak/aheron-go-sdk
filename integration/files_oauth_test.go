package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
)

const filesOAuthProject = "11111111-1111-1111-1111-111111111111"
const filesOAuthFile = "22222222-2222-2222-2222-222222222222"
const filesOAuthInstall = "33333333-3333-3333-3333-333333333333"
const filesOAuthUploadKey = "tmp/" + filesOAuthProject + "/" + filesOAuthFile

func filesOAuthFixture(t *testing.T, handler http.HandlerFunc, upload *http.Client) *FilesClient {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := integrationoauth.NewProvider(integrationoauth.Config{ClientID: filesOAuthInstall, KeyID: "key", PrivateKey: key, TokenEndpoint: server.URL + "/oauth/token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(Config{MediaURL: server.URL + "/api/media", APIKey: "ahr_proj_must_not_be_sent", FilesOAuth: &FilesOAuthConfig{Provider: provider, ProjectID: filesOAuthProject, InstallationID: filesOAuthInstall, HTTPClient: server.Client(), UploadHTTPClient: upload}})
	if err != nil {
		t.Fatal(err)
	}
	return c.Files.WithAPIKey("ahr_proj_other_project")
}

func filesOAuthIssue(t *testing.T, w http.ResponseWriter, r *http.Request, n int, scope string) string {
	t.Helper()
	if err := r.ParseForm(); err != nil {
		t.Error(err)
	}
	if r.Form.Get("audience") != "media" || r.Form.Get("projectId") != filesOAuthProject || r.Form.Get("installationId") != filesOAuthInstall || r.Form.Get("scope") != scope {
		t.Error("wrong media token binding or scope")
	}
	raw := make([]byte, 32)
	raw[0] = byte(n)
	token := "aho_" + base64.RawURLEncoding.EncodeToString(raw)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 300, "scope": scope})
	return token
}

func filesResultError[T any](_ T, err error) error { return err }

func TestFilesOAuthMetadataMethods(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, method, path, scope, body string
		call                            func(*FilesClient) error
	}{
		{"list", "GET", "/files", "files.read", `{"files":[]}`, func(c *FilesClient) error {
			return filesResultError(c.List(ctx, ListParams{Namespace: "integration", Limit: 10}))
		}},
		{"get", "GET", "/files/" + filesOAuthFile, "files.read", `{"id":"` + filesOAuthFile + `"}`, func(c *FilesClient) error { return filesResultError(c.Get(ctx, filesOAuthFile)) }},
		{"rename", "PATCH", "/files/" + filesOAuthFile, "files.write", `{"id":"` + filesOAuthFile + `"}`, func(c *FilesClient) error { return filesResultError(c.Rename(ctx, filesOAuthFile, "new.txt")) }},
		{"delete", "DELETE", "/files/" + filesOAuthFile, "files.write", `{"status":"ok"}`, func(c *FilesClient) error { return c.Delete(ctx, filesOAuthFile) }},
		{"purge", "POST", "/files/purge", "files.write", `{"deleted":2}`, func(c *FilesClient) error { return filesResultError(c.PurgeNamespace(ctx, "integration", nil)) }},
		{"usage", "GET", "/usage", "files.read", `{"projectId":"` + filesOAuthProject + `","storedBytes":123,"namespaces":[]}`, func(c *FilesClient) error { return filesResultError(c.Usage(ctx)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var issues, calls atomic.Int32
			c := filesOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/oauth/token" {
					filesOAuthIssue(t, w, r, int(issues.Add(1)), tc.scope)
					return
				}
				calls.Add(1)
				if r.Method != tc.method || r.URL.Path != "/api/media"+tc.path || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer aho_") || r.Header.Get("X-Integration-Signature") != "" {
					t.Error("wrong media request")
				}
				if tc.name == "list" && (r.URL.Query().Get("namespace") != "integration" || r.URL.Query().Get("limit") != "10") {
					t.Error("lost list filters")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}, nil)
			if err := tc.call(c); err != nil {
				t.Fatal(err)
			}
			if issues.Load() != 1 || calls.Load() != 1 {
				t.Fatal("unexpected retry or fallback")
			}
		})
	}
}

func TestFilesOAuthUploadLifecycle(t *testing.T) {
	for _, name := range []string{"upload", "namespace", "replace", "finalizeRevoked", "storageError", "storageRedirect"} {
		t.Run(name, func(t *testing.T) {
			var puts, finalizes, issues, targets atomic.Int32
			store := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				puts.Add(1)
				if r.URL.Path != "/object" || r.Method != "PUT" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Integration-Signature") != "" {
					t.Error("upload leaked credentials or followed redirect")
				}
				body, _ := io.ReadAll(r.Body)
				if string(body) != "content" || r.Header.Get("Content-MD5") != md5Base64(body) {
					t.Error("corrupt upload")
				}
				if name == "storageError" {
					w.WriteHeader(500)
					_, _ = w.Write([]byte("secret-upload-signature"))
					return
				}
				if name == "storageRedirect" {
					w.Header().Set("Location", "/must-not-follow")
					w.WriteHeader(307)
					return
				}
				w.WriteHeader(200)
			}))
			defer store.Close()
			upload := store.Client()
			jar, _ := cookiejar.New(nil)
			storeURL, _ := url.Parse(store.URL)
			jar.SetCookies(storeURL, []*http.Cookie{{Name: "secret", Value: "secret-cookie"}})
			upload.Jar = jar
			c := filesOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/oauth/token" {
					filesOAuthIssue(t, w, r, int(issues.Add(1)), "files.write")
					return
				}
				if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer aho_") {
					t.Error("missing OAuth")
				}
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/media/files/upload-url" {
					targets.Add(1)
					_ = json.NewEncoder(w).Encode(uploadURLResponse{UploadKey: filesOAuthUploadKey, URL: store.URL + "/object?signature=secret-upload-signature", Method: "PUT"})
					return
				}
				finalizes.Add(1)
				if r.Method != "POST" {
					t.Error("wrong finalize method")
				}
				if puts.Load() != 1 {
					t.Error("finalized before upload")
				}
				if name == "finalizeRevoked" {
					w.WriteHeader(401)
					_, _ = w.Write([]byte(`{"error":"secret"}`))
					return
				}
				if name == "replace" {
					if r.URL.Path != "/api/media/files/"+filesOAuthFile+"/content" {
						t.Error("wrong replacement")
					}
				} else {
					if r.URL.Path != "/api/media/files" {
						t.Error("wrong finalize")
					}
					var body finalizeUploadBody
					if json.NewDecoder(r.Body).Decode(&body) != nil || body.UploadKey != filesOAuthUploadKey || name == "namespace" && body.Namespace != "integration" {
						t.Error("lost finalize inputs")
					}
					w.WriteHeader(201)
				}
				_, _ = w.Write([]byte(`{"id":"` + filesOAuthFile + `"}`))
			}, upload)
			var err error
			switch name {
			case "replace":
				_, err = c.Replace(context.Background(), filesOAuthFile, "text/plain", []byte("content"))
			case "namespace":
				_, err = c.UploadToNamespace(context.Background(), "integration", "file.txt", "text/plain", []byte("content"))
			default:
				_, err = c.Upload(context.Background(), "file.txt", "text/plain", []byte("content"))
			}
			failed := name == "finalizeRevoked" || strings.HasPrefix(name, "storage")
			if (err != nil) != failed || err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsafe or unexpected error: %v", err)
			}
			if name == "finalizeRevoked" && StatusCode(err) != 401 {
				t.Fatal("lost authorization status")
			}
			wantFinal := int32(1)
			if strings.HasPrefix(name, "storage") {
				wantFinal = 0
			}
			if issues.Load() != 1 || targets.Load() != 1 || puts.Load() != 1 || finalizes.Load() != wantFinal {
				t.Fatalf("unexpected replay issues=%d targets=%d puts=%d finalizes=%d", issues.Load(), targets.Load(), puts.Load(), finalizes.Load())
			}
			if upload.Jar != jar {
				t.Fatal("mutated caller HTTP client")
			}
		})
	}
}

func TestFilesOAuthRejectsUnsafeUploadTarget(t *testing.T) {
	for _, name := range []string{"http", "userinfo", "fragment", "method", "foreignKey", "traversalKey"} {
		t.Run(name, func(t *testing.T) {
			var puts, finalizes atomic.Int32
			store := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { puts.Add(1) }))
			defer store.Close()
			target := uploadURLResponse{URL: store.URL + "/object?signature=secret", UploadKey: filesOAuthUploadKey, Method: "PUT"}
			switch name {
			case "http":
				target.URL = strings.Replace(target.URL, "https:", "http:", 1)
			case "userinfo":
				target.URL = strings.Replace(target.URL, "https://", "https://secret@", 1)
			case "fragment":
				target.URL += "#secret"
			case "method":
				target.Method = "POST"
			case "foreignKey":
				target.UploadKey = "tmp/" + filesOAuthInstall + "/" + filesOAuthFile
			case "traversalKey":
				target.UploadKey += "/../secret"
			}
			c := filesOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/oauth/token" {
					filesOAuthIssue(t, w, r, 1, "files.write")
					return
				}
				if r.URL.Path != "/api/media/files/upload-url" {
					finalizes.Add(1)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(target)
			}, store.Client())
			_, err := c.Upload(context.Background(), "file", "", []byte("content"))
			if !errors.Is(err, errFilesOAuthResponse) || puts.Load() != 0 || finalizes.Load() != 0 {
				t.Fatalf("unsafe target accepted: %v", err)
			}
		})
	}
}

func TestFilesOAuthRetriesAndFailures(t *testing.T) {
	for _, name := range []string{"read401", "write401", "write503", "read403", "tokenError", "null", "oversize", "decodeSecret", "foreignUsage", "metadataRedirect"} {
		t.Run(name, func(t *testing.T) {
			var issues, calls atomic.Int32
			scope := "files.read"
			if strings.HasPrefix(name, "write") {
				scope = "files.write"
			}
			c := filesOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/oauth/token" {
					n := issues.Add(1)
					if name == "tokenError" {
						w.WriteHeader(503)
						_, _ = w.Write([]byte("secret"))
						return
					}
					filesOAuthIssue(t, w, r, int(n), scope)
					return
				}
				n := calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				switch name {
				case "read401":
					if n == 1 {
						w.WriteHeader(401)
						return
					}
				case "write401":
					w.WriteHeader(401)
					return
				case "write503":
					w.WriteHeader(503)
					return
				case "read403":
					w.WriteHeader(403)
					_, _ = w.Write([]byte("secret"))
					return
				case "null":
					_, _ = w.Write([]byte("null"))
					return
				case "oversize":
					_, _ = w.Write([]byte(strings.Repeat(" ", (1<<20)+1)))
					return
				case "decodeSecret":
					_, _ = w.Write([]byte(`{"createdAt":"secret-reflected-bearer"}`))
					return
				case "foreignUsage":
					_, _ = w.Write([]byte(`{"projectId":"` + filesOAuthInstall + `"}`))
					return
				case "metadataRedirect":
					w.Header().Set("Location", "/api/media/redirect")
					w.WriteHeader(307)
					return
				}
				_, _ = w.Write([]byte(`{"id":"` + filesOAuthFile + `"}`))
			}, nil)
			var err error
			if strings.HasPrefix(name, "write") {
				err = c.Delete(context.Background(), filesOAuthFile)
			} else if name == "foreignUsage" {
				_, err = c.Usage(context.Background())
			} else {
				_, err = c.Get(context.Background(), filesOAuthFile)
			}
			wantIssues, wantCalls := int32(1), int32(1)
			if name == "read401" {
				wantIssues, wantCalls = 2, 2
			}
			if name == "tokenError" {
				wantCalls = 0
			}
			if (err == nil) != (name == "read401") || err != nil && strings.Contains(err.Error(), "secret") || issues.Load() != wantIssues || calls.Load() != wantCalls {
				t.Fatalf("error=%v issues=%d calls=%d", err, issues.Load(), calls.Load())
			}
		})
	}
}

func TestFilesOAuthRejectsPathEscapeBeforeUpload(t *testing.T) {
	var calls atomic.Int32
	c := filesOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }, nil)
	for _, id := range []string{"../usage", filesOAuthFile + "?secret=x", "%2e%2e", filesOAuthFile + "/content"} {
		for _, call := range []func() error{
			func() error { return filesResultError(c.Get(context.Background(), id)) },
			func() error { return filesResultError(c.Rename(context.Background(), id, "name")) },
			func() error { return c.Delete(context.Background(), id) },
			func() error {
				return filesResultError(c.Replace(context.Background(), id, "text/plain", []byte("content")))
			},
		} {
			if err := call(); !errors.Is(err, integrationoauth.ErrRequest) {
				t.Fatalf("error=%v", err)
			}
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid object ID caused I/O")
	}
}
