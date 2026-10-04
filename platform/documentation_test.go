package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	docs "github.com/Alexey-zaliznuak/aheron-go-sdk/documentation"
)

func TestDocumentationDraftLifecycleContract(t *testing.T) {
	ctx := context.Background()
	user := testToken("editor")
	revision := int64(1)
	managed := true
	folder := docs.Folder{ID: projectID, Kind: "integration", OwnerRef: ptr(otherID), CanManage: &managed}
	article := docs.Article{ID: schemeID, FolderID: projectID, Title: "Guide", Slug: "guide", CurrentRevision: revision, BodyMarkdown: "Common", Knowledge: &docs.ArticleKnowledge{TopicKey: "demo/guide", Locale: "ru", AgentAppendixMarkdown: "Agent"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+user || r.Header.Get("Cookie") != "" {
			t.Error("wrong editor credential")
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /docs/folders":
			jsonReply(w, []docs.Folder{folder})
		case "POST /docs/folders":
			var in docs.CreateFolderParams
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.ParentID == nil || *in.ParentID != projectID || in.Name != "Child" {
				t.Error("folder contract")
			}
			jsonReply(w, folder)
		case "GET /docs/integration-folders":
			jsonReply(w, []docs.IntegrationOption{{ID: otherID, Slug: "demo"}})
		case "POST /docs/integration-folders":
			var in map[string]string
			if json.NewDecoder(r.Body).Decode(&in) != nil || in["integrationId"] != otherID || len(in) != 1 {
				t.Error("provision contract")
			}
			jsonReply(w, docs.ProvisionIntegrationFolderResult{Folder: folder})
		case "GET /docs/folders/" + projectID + "/articles":
			jsonReply(w, []docs.Article{article})
		case "GET /docs/articles/" + schemeID:
			jsonReply(w, docs.ArticleDocument{Folder: folder, Article: article})
		case "POST /docs/folders/" + projectID + "/articles":
			var in docs.CreateArticleParams
			if json.NewDecoder(r.Body).Decode(&in) != nil || in.BodyMarkdown != "Common" || in.Knowledge == nil || in.Knowledge.AgentAppendixMarkdown != "Agent" {
				t.Error("create article contract")
			}
			jsonReply(w, article)
		case "PUT /docs/articles/" + schemeID:
			var in map[string]json.RawMessage
			if json.NewDecoder(r.Body).Decode(&in) != nil || string(in["expectedRevision"]) != "1" || string(in["summary"]) != "null" || string(in["knowledge"]) != "{}" {
				t.Errorf("update contract: %v", in)
			}
			article.CurrentRevision++
			jsonReply(w, article)
		case "POST /docs/articles/" + schemeID + "/publish", "POST /docs/articles/" + schemeID + "/unpublish":
			var in map[string]int64
			if json.NewDecoder(r.Body).Decode(&in) != nil || in["expectedRevision"] != 2 || len(in) != 1 {
				t.Error("publication revision missing")
			}
			article.PublishedRevision = nil
			if strings.HasSuffix(r.URL.Path, "/publish") {
				article.PublishedRevision = &article.CurrentRevision
			}
			jsonReply(w, article)
		default:
			t.Error("unexpected route", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c, err := NewDocumentation(Config{BaseURL: server.URL + "/docs", TokenProvider: provider(t, user), AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ListFolders(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = c.CreateFolder(ctx, docs.CreateFolderParams{ParentID: ptr(projectID), Name: "Child"}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ListProvisionableIntegrations(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ProvisionIntegrationFolder(ctx, otherID); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ListArticles(ctx, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err = c.GetArticle(ctx, schemeID); err != nil {
		t.Fatal(err)
	}
	if _, err = c.CreateArticle(ctx, projectID, docs.CreateArticleParams{Title: "Guide", BodyMarkdown: "Common", Knowledge: article.Knowledge}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.UpdateArticle(ctx, schemeID, docs.UpdateArticleParams{Title: "Next", Slug: "guide", ExpectedRevision: 1, Knowledge: &docs.ArticleKnowledge{}}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.PublishArticle(ctx, schemeID, 2); err != nil {
		t.Fatal(err)
	}
	if _, err = c.UnpublishArticle(ctx, schemeID, 2); err != nil {
		t.Fatal(err)
	}
}

func TestDocumentationBoundariesAndNoReplay(t *testing.T) {
	var calls atomic.Int32
	status := http.StatusConflict
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", "/other")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"code":"SECRET","message":"SECRET"}}`))
	}))
	defer server.Close()
	c, err := NewDocumentation(Config{BaseURL: server.URL, TokenProvider: provider(t, testToken("editor")), AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, id := range []string{"../publishing", "?token=secret", "not-id"} {
		if _, err := c.GetArticle(ctx, id); !errors.Is(err, ErrInvalidInput) {
			t.Fatal(err)
		}
		if _, err := c.CreateArticle(ctx, id, docs.CreateArticleParams{Title: "Draft"}); !errors.Is(err, ErrInvalidInput) {
			t.Fatal(err)
		}
	}
	if _, err := c.PublishArticle(ctx, schemeID, 0); !errors.Is(err, ErrInvalidInput) {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input reached upstream")
	}
	for _, code := range []int{409, 401, 403, 503, 307} {
		status = code
		before := calls.Load()
		_, err = c.PublishArticle(ctx, schemeID, 1)
		var api *APIError
		if !errors.As(err, &api) || api.StatusCode != code || strings.Contains(err.Error(), "SECRET") || calls.Load() != before+1 {
			t.Fatalf("status, redaction or no-replay boundary: %v", err)
		}
	}
}

func ptr(value string) *string { return &value }
