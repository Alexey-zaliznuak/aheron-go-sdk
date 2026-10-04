package platform

import (
	"context"
	"net/http"
	"strings"

	docs "github.com/Alexey-zaliznuak/aheron-go-sdk/documentation"
)

const DefaultDocumentationURL = "https://docs.aheron.pro/api/documentation"

// DocumentationClient uses ordinary user credentials and the shared bounded,
// non-retrying transport. The documentation service authorizes every request.
// This client cannot publish or replace technical CI packages.
type DocumentationClient struct{ client *Client }

func NewDocumentation(cfg Config) (*DocumentationClient, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultDocumentationURL
	}
	if cfg.MaxResponseBytes == 0 {
		cfg.MaxResponseBytes = 16 << 20 // Bounded escaped Markdown + metadata.
	}
	c, err := New(cfg)
	if err != nil {
		return nil, err
	}
	return &DocumentationClient{client: c}, nil
}

func (s *DocumentationClient) ListFolders(ctx context.Context) ([]docs.Folder, error) {
	var out []docs.Folder
	err := s.client.get(ctx, "list documentation folders", "/folders", &out)
	return out, err
}

func (s *DocumentationClient) CreateFolder(ctx context.Context, p docs.CreateFolderParams) (docs.Folder, error) {
	if strings.TrimSpace(p.Name) == "" || p.ParentID != nil && (!resourceID.MatchString(*p.ParentID) || p.Path != "") {
		return docs.Folder{}, ErrInvalidInput
	}
	var out docs.Folder
	err := s.client.requestJSON(ctx, "create documentation folder", http.MethodPost, "/folders", nil, p, &out)
	if err == nil && !resourceID.MatchString(out.ID) {
		err = ErrResponse
	}
	return out, err
}

func (s *DocumentationClient) ListProvisionableIntegrations(ctx context.Context) ([]docs.IntegrationOption, error) {
	var out []docs.IntegrationOption
	err := s.client.get(ctx, "list documentation integrations", "/integration-folders", &out)
	return out, err
}

func (s *DocumentationClient) ProvisionIntegrationFolder(ctx context.Context, integrationID string) (docs.ProvisionIntegrationFolderResult, error) {
	if !resourceID.MatchString(integrationID) {
		return docs.ProvisionIntegrationFolderResult{}, ErrInvalidInput
	}
	var out docs.ProvisionIntegrationFolderResult
	in := struct {
		IntegrationID string `json:"integrationId"`
	}{integrationID}
	err := s.client.requestJSON(ctx, "provision documentation integration folder", http.MethodPost, "/integration-folders", nil, in, &out)
	if err == nil && (!resourceID.MatchString(out.Folder.ID) || out.Folder.Kind != "integration" || out.Folder.OwnerRef == nil || !strings.EqualFold(*out.Folder.OwnerRef, integrationID)) {
		err = ErrResponse
	}
	return out, err
}

func (s *DocumentationClient) ListArticles(ctx context.Context, folderID string) ([]docs.Article, error) {
	if !resourceID.MatchString(folderID) {
		return nil, ErrInvalidInput
	}
	var out []docs.Article
	err := s.client.get(ctx, "list documentation articles", "/folders/"+folderID+"/articles", &out)
	if err == nil {
		for _, article := range out {
			if !resourceID.MatchString(article.ID) || !strings.EqualFold(article.FolderID, folderID) {
				return nil, ErrResponse
			}
		}
	}
	return out, err
}

func (s *DocumentationClient) GetArticle(ctx context.Context, articleID string) (docs.ArticleDocument, error) {
	if !resourceID.MatchString(articleID) {
		return docs.ArticleDocument{}, ErrInvalidInput
	}
	var out docs.ArticleDocument
	err := s.client.get(ctx, "get documentation article", "/articles/"+articleID, &out)
	if err == nil && (!strings.EqualFold(out.Article.ID, articleID) || !resourceID.MatchString(out.Folder.ID) || !strings.EqualFold(out.Article.FolderID, out.Folder.ID) || out.Article.CurrentRevision < 1) {
		err = ErrResponse
	}
	return out, err
}

func (s *DocumentationClient) CreateArticle(ctx context.Context, folderID string, p docs.CreateArticleParams) (docs.Article, error) {
	if !resourceID.MatchString(folderID) || strings.TrimSpace(p.Title) == "" {
		return docs.Article{}, ErrInvalidInput
	}
	var out docs.Article
	err := s.client.requestJSON(ctx, "create documentation article", http.MethodPost, "/folders/"+folderID+"/articles", nil, p, &out)
	if err == nil && (!resourceID.MatchString(out.ID) || !strings.EqualFold(out.FolderID, folderID) || out.CurrentRevision != 1 || out.PublishedRevision != nil) {
		err = ErrResponse
	}
	return out, err
}

func (s *DocumentationClient) UpdateArticle(ctx context.Context, articleID string, p docs.UpdateArticleParams) (docs.Article, error) {
	if !resourceID.MatchString(articleID) || p.ExpectedRevision < 1 || strings.TrimSpace(p.Title) == "" || strings.TrimSpace(p.Slug) == "" {
		return docs.Article{}, ErrInvalidInput
	}
	var out docs.Article
	err := s.client.requestJSON(ctx, "update documentation article", http.MethodPut, "/articles/"+articleID, nil, p, &out)
	if err == nil && (!strings.EqualFold(out.ID, articleID) || out.CurrentRevision != p.ExpectedRevision+1) {
		err = ErrResponse
	}
	return out, err
}

func (s *DocumentationClient) PublishArticle(ctx context.Context, articleID string, expectedRevision int64) (docs.Article, error) {
	return s.changePublication(ctx, articleID, expectedRevision, "publish")
}

func (s *DocumentationClient) UnpublishArticle(ctx context.Context, articleID string, expectedRevision int64) (docs.Article, error) {
	return s.changePublication(ctx, articleID, expectedRevision, "unpublish")
}

func (s *DocumentationClient) changePublication(ctx context.Context, id string, revision int64, action string) (docs.Article, error) {
	if !resourceID.MatchString(id) || revision < 1 {
		return docs.Article{}, ErrInvalidInput
	}
	in := struct {
		ExpectedRevision int64 `json:"expectedRevision"`
	}{revision}
	var out docs.Article
	err := s.client.requestJSON(ctx, action+" documentation article", http.MethodPost, "/articles/"+id+"/"+action, nil, in, &out)
	if err == nil && (!strings.EqualFold(out.ID, id) || out.CurrentRevision != revision || (action == "publish" && (out.PublishedRevision == nil || *out.PublishedRevision != revision)) || (action == "unpublish" && out.PublishedRevision != nil)) {
		err = ErrResponse
	}
	return out, err
}
