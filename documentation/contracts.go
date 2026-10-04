// Package docs defines the versioned knowledge exchange contract.
// Domain documentation and executable examples remain owned by their services.
package docs

import "time"

const (
	FormatVersion    = 1
	MaxDocuments     = 64
	MaxPackageBytes  = 2 << 20
	MaxResponseBytes = 3 << 20
)

type Contract struct {
	ProviderKey      string `json:"providerKey"`
	ContractRevision string `json:"contractRevision"`
}

type Document struct {
	DocumentKey           string     `json:"documentKey"`
	TopicKey              string     `json:"topicKey"`
	Locale                string     `json:"locale"`
	Title                 string     `json:"title"`
	Summary               string     `json:"summary"`
	CommonMarkdown        string     `json:"commonMarkdown"`
	HumanAppendixMarkdown string     `json:"humanAppendixMarkdown"`
	AgentAppendixMarkdown string     `json:"agentAppendixMarkdown"`
	RelatedTopics         []string   `json:"relatedTopics"`
	Requires              []Contract `json:"requires"`
}

type Package struct {
	FormatVersion    int        `json:"formatVersion"`
	ProviderKey      string     `json:"providerKey"`
	ContractRevision string     `json:"contractRevision"`
	SourceRevision   string     `json:"sourceRevision"`
	Documents        []Document `json:"documents"`
}

type DocumentRef struct {
	ProviderKey   string `json:"providerKey"`
	PackageDigest string `json:"packageDigest"`
	DocumentKey   string `json:"documentKey"`
	Locale        string `json:"locale"`
}

type DocumentSummary struct {
	Ref           DocumentRef `json:"ref"`
	TopicKey      string      `json:"topicKey"`
	Title         string      `json:"title"`
	Summary       string      `json:"summary"`
	ContentSHA256 string      `json:"contentSha256"`
	RelatedTopics []string    `json:"relatedTopics"`
	Requires      []Contract  `json:"requires"`
}

type Channel struct {
	ProviderKey      string    `json:"providerKey"`
	ContractRevision string    `json:"contractRevision"`
	PackageDigest    string    `json:"packageDigest"`
	Revision         int64     `json:"revision"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

type CatalogRequest struct {
	ProviderKey      string `json:"providerKey"`
	ContractRevision string `json:"contractRevision"`
	Locale           string `json:"locale"`
	Cursor           string `json:"cursor,omitempty"`
	Limit            int    `json:"limit,omitempty"`
}

type Catalog struct {
	Channel        Channel           `json:"channel"`
	SourceRevision string            `json:"sourceRevision"`
	Documents      []DocumentSummary `json:"documents"`
	NextCursor     string            `json:"nextCursor,omitempty"`
}

type ReadRequest struct {
	Ref      DocumentRef `json:"ref"`
	Audience string      `json:"audience"` // human or agent; never silently defaults
}

type ReadResult struct {
	Document         DocumentSummary `json:"document"`
	ContractRevision string          `json:"contractRevision"`
	SourceRevision   string          `json:"sourceRevision"`
	Audience         string          `json:"audience"`
	Markdown         string          `json:"markdown"`
	ContentSHA256    string          `json:"contentSha256"` // hash of this rendered audience's UTF-8 bytes
}

type UploadResult struct {
	ProviderKey      string `json:"providerKey"`
	PackageDigest    string `json:"packageDigest"`
	ContractRevision string `json:"contractRevision"`
}

type ActivateRequest struct {
	PackageDigest    string `json:"packageDigest"`
	ContractRevision string `json:"contractRevision"`
	ExpectedRevision int64  `json:"expectedRevision"`
	OperationID      string `json:"operationId"`
}

type Receipt struct {
	OperationID string  `json:"operationId"`
	Channel     Channel `json:"channel"`
}
