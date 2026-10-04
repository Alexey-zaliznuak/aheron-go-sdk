package docs

import "time"

// Editorial DTOs describe the existing user-authorized editor API. Technical
// CI packages have a separate publishing identity and are not editable here.
type Folder struct {
	ID              string    `json:"id"`
	ParentID        *string   `json:"parentId,omitempty"`
	Path            string    `json:"path"`
	Slug            string    `json:"slug,omitempty"`
	Name            string    `json:"name"`
	Description     *string   `json:"description,omitempty"`
	Kind            string    `json:"kind"`
	OwnerRef        *string   `json:"ownerRef,omitempty"`
	Revision        int64     `json:"revision,omitempty"`
	IsRoot          bool      `json:"isRoot,omitempty"`
	IsPinned        bool      `json:"isPinned,omitempty"`
	CanManage       *bool     `json:"canManage,omitempty"`
	CreatedByUserID string    `json:"createdByUserId"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type ArticleKnowledge struct {
	TopicKey              string     `json:"topicKey,omitempty"`
	Locale                string     `json:"locale,omitempty"`
	HumanAppendixMarkdown string     `json:"humanAppendixMarkdown,omitempty"`
	AgentAppendixMarkdown string     `json:"agentAppendixMarkdown,omitempty"`
	AppliesTo             []Contract `json:"appliesTo,omitempty"`
}

type Article struct {
	ID                    string            `json:"id"`
	FolderID              string            `json:"folderId"`
	AuthorUserID          string            `json:"authorUserId"`
	Title                 string            `json:"title"`
	Slug                  string            `json:"slug"`
	Summary               *string           `json:"summary,omitempty"`
	BodyMarkdown          string            `json:"bodyMarkdown,omitempty"`
	Knowledge             *ArticleKnowledge `json:"knowledge,omitempty"`
	ContentSHA256         string            `json:"contentSha256"`
	LocationRevision      int64             `json:"locationRevision,omitempty"`
	CurrentRevision       int64             `json:"currentRevision"`
	PublishedRevision     *int64            `json:"publishedRevision,omitempty"`
	Status                string            `json:"status"`
	HasUnpublishedChanges bool              `json:"hasUnpublishedChanges"`
	PublishedAt           *time.Time        `json:"publishedAt,omitempty"`
	CreatedAt             time.Time         `json:"createdAt"`
	UpdatedAt             time.Time         `json:"updatedAt"`
}

type ArticleDocument struct {
	Folder  Folder  `json:"folder"`
	Article Article `json:"article"`
}

type CreateFolderParams struct {
	ParentID    *string `json:"parentId,omitempty"`
	Path        string  `json:"path,omitempty"` // Legacy mode only; prefer parentId.
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

type IntegrationOption struct {
	ID          string  `json:"id"`
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

type ProvisionIntegrationFolderResult struct {
	Folder  Folder `json:"folder"`
	Created bool   `json:"created"`
}

type CreateArticleParams struct {
	Title        string            `json:"title"`
	Slug         string            `json:"slug,omitempty"`
	Summary      *string           `json:"summary,omitempty"`
	BodyMarkdown string            `json:"bodyMarkdown"`
	Knowledge    *ArticleKnowledge `json:"knowledge,omitempty"`
}

// UpdateArticleParams replaces a complete draft at ExpectedRevision. Summary
// nil clears the summary. Knowledge nil preserves existing metadata/appendices;
// &ArticleKnowledge{} clears them. Published content stays at its old revision.
type UpdateArticleParams struct {
	ExpectedRevision int64             `json:"expectedRevision"`
	Title            string            `json:"title"`
	Slug             string            `json:"slug"`
	Summary          *string           `json:"summary"`
	BodyMarkdown     string            `json:"bodyMarkdown"`
	Knowledge        *ArticleKnowledge `json:"knowledge,omitempty"`
}
