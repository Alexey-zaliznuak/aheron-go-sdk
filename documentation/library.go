package docs

import (
	"context"
	"errors"
	"reflect"
	"time"
)

// LibraryProfile describes observed runtime contracts, not the caller's rights.
type LibraryProfile struct {
	Locale         string     `json:"locale"`
	Contracts      []Contract `json:"contracts"`
	IntegrationIDs []string   `json:"integrationIds,omitempty"`
}

// LibraryRef is a tagged union. Only the fields for Source may be populated.
// Article refs point at an exact current publication; changed publications return 409.
type LibraryRef struct {
	Source    string       `json:"source"` // article or package
	ArticleID string       `json:"articleId,omitempty"`
	Revision  int64        `json:"revision,omitempty"`
	Package   *DocumentRef `json:"package,omitempty"`
}

type LibraryDocument struct {
	Ref            LibraryRef `json:"ref"`
	ProviderKey    string     `json:"providerKey"`
	TopicKey       string     `json:"topicKey"`
	Locale         string     `json:"locale"`
	Title          string     `json:"title"`
	Summary        string     `json:"summary"`
	ContentSHA256  string     `json:"contentSha256"` // canonical complete document, not rendered text
	Requires       []Contract `json:"requires"`      // includes a package's own runtime contract
	SourceRevision string     `json:"sourceRevision,omitempty"`
	IntegrationID  string     `json:"integrationId,omitempty"`
	Kind           string     `json:"kind,omitempty"`
	BlockKey       string     `json:"blockKey,omitempty"`
}

type LibraryIntegrationManifestRequest struct {
	Profile LibraryProfile `json:"profile"`
}
type LibraryIntegrationManifestResult struct {
	SnapshotSHA256 string                       `json:"snapshotSha256"`
	Integrations   []LibraryIntegrationManifest `json:"integrations"`
}
type LibraryIntegrationManifest struct {
	IntegrationID           string              `json:"integrationId"`
	Overview                *LibraryRef         `json:"overview,omitempty"`
	OverviewStatus          string              `json:"overviewStatus"`
	Documents               []LibraryDocument   `json:"documents"`
	Blocks                  []LibraryBlockGuide `json:"blocks"`
	MissingTechnicalPackage bool                `json:"missingTechnicalPackage"`
}
type LibraryBlockGuide struct {
	BlockKey    string       `json:"blockKey"`
	GuideRef    *LibraryRef  `json:"guideRef,omitempty"`
	Status      string       `json:"status"`
	RelatedRefs []LibraryRef `json:"relatedRefs"`
}

func (c *Client) LibraryIntegrationManifests(ctx context.Context, in LibraryIntegrationManifestRequest) (LibraryIntegrationManifestResult, error) {
	var out LibraryIntegrationManifestResult
	err := c.request(ctx, "POST", "/public/knowledge/library/integrations/manifest", in, &out, false)
	return out, err
}

type LibraryCatalogRequest struct {
	Profile  LibraryProfile `json:"profile"`
	Source   string         `json:"source,omitempty"`
	TopicKey string         `json:"topicKey,omitempty"`
	Limit    int            `json:"limit,omitempty"`
	Cursor   string         `json:"cursor,omitempty"`
}

type LibraryCatalog struct {
	SnapshotSHA256    string            `json:"snapshotSha256"`
	Documents         []LibraryDocument `json:"documents"`
	MissingContracts  []Contract        `json:"missingContracts"`
	ExcludedDocuments int               `json:"excludedDocuments"`
	NextCursor        string            `json:"nextCursor,omitempty"`
}

type LibraryReadRequest struct {
	Profile  LibraryProfile `json:"profile"`
	Ref      LibraryRef     `json:"ref"`
	Audience string         `json:"audience"`
}

type LibraryReadResult struct {
	Document      LibraryDocument `json:"document"`
	Audience      string          `json:"audience"`
	Markdown      string          `json:"markdown"`
	ContentSHA256 string          `json:"contentSha256"`
	Fallback      bool            `json:"fallback"`
}

type LibrarySearchRequest struct {
	Profile LibraryProfile `json:"profile"`
	Query   string         `json:"query"`
	Limit   int            `json:"limit,omitempty"`
}

type LibrarySearchResult struct {
	SnapshotSHA256   string            `json:"snapshotSha256"`
	IndexedAt        time.Time         `json:"indexedAt"`
	Documents        []LibraryDocument `json:"documents"`
	MissingContracts []Contract        `json:"missingContracts"`
	Truncated        bool              `json:"truncated"`
}

type LibraryBootstrapRequest struct {
	Profile  LibraryProfile `json:"profile"`
	Audience string         `json:"audience"`
}

type LibraryBootstrapResult struct {
	ManifestVersion string              `json:"manifestVersion"`
	SnapshotSHA256  string              `json:"snapshotSha256"`
	Documents       []LibraryReadResult `json:"documents"`
	Catalog         LibraryCatalog      `json:"catalog"`
	MissingTopics   []string            `json:"missingTopics"`
	Deferred        []LibraryRef        `json:"deferred"` // complete documents omitted by the byte budget; read separately
	Incomplete      bool                `json:"incomplete"`
}

func (c *Client) LibraryCatalog(ctx context.Context, in LibraryCatalogRequest) (LibraryCatalog, error) {
	var out LibraryCatalog
	err := c.request(ctx, "POST", "/public/knowledge/library/catalog", in, &out, false)
	return out, err
}

func (c *Client) LibraryRead(ctx context.Context, in LibraryReadRequest) (LibraryReadResult, error) {
	var out LibraryReadResult
	err := c.request(ctx, "POST", "/public/knowledge/library/read", in, &out, false)
	if err == nil && (!reflect.DeepEqual(out.Document.Ref, in.Ref) || out.Audience != in.Audience || out.ContentSHA256 != SHA256([]byte(out.Markdown))) {
		err = errors.New("documentation: library response identity or content hash mismatch")
	}
	return out, err
}

func (c *Client) LibrarySearch(ctx context.Context, in LibrarySearchRequest) (LibrarySearchResult, error) {
	var out LibrarySearchResult
	err := c.request(ctx, "POST", "/public/knowledge/library/search", in, &out, false)
	return out, err
}

func (c *Client) LibraryBootstrap(ctx context.Context, in LibraryBootstrapRequest) (LibraryBootstrapResult, error) {
	var out LibraryBootstrapResult
	err := c.request(ctx, "POST", "/public/knowledge/library/bootstrap", in, &out, false)
	if err == nil {
		for _, d := range out.Documents {
			if d.Audience != in.Audience || d.ContentSHA256 != SHA256([]byte(d.Markdown)) {
				return LibraryBootstrapResult{}, errors.New("documentation: bootstrap content hash or audience mismatch")
			}
		}
	}
	return out, err
}
