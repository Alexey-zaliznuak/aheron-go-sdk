package schemetransfer

import "encoding/json"

// CopyFile attaches a private media-service source to an integration reference.
// The reference's value still identifies the integration's library entry, not
// the media file. Only the platform may capture bytes and publish an asset ref.
type CopyFile struct {
	MediaFileID string `json:"mediaFileId"`
}

// FileImport declares that a resource source can register an already copied
// media file in its own library. Namespace is fixed by the pinned manifest.
type FileImport struct {
	Namespace string `json:"namespace"`
}

// File marks one existing scalar or array element. The integration resolves its
// own file ID to a media-service ID before calling this method. It must not mark
// external links, thumbnails standing in for videos, or provider upload caches.
// Like Resource, this builder only prepares a plan; it performs no network I/O.
func (b *CopyBuilder) File(path, sourceKey, mediaFileID string) *CopyBuilder {
	b.Resource(path, sourceKey)
	if b.err == nil {
		b.plan.References[len(b.plan.References)-1].File = &CopyFile{MediaFileID: mediaFileID}
	}
	return b
}

// ImportCopyFileRequest is a signed, mutating callback, separate from read-only
// preparation/lookup/validation. The platform has already copied the bytes into
// the target project. No source URL, credentials or file metadata are accepted.
// The receiver checks the installation and reads MediaFileID through Files.Get
// with the target project's key, verifying the manifest's namespace.
//
// (ProjectID, ImportID, ResourceKey) is a durable receipt address. Concurrent
// retries return one library entry; changing SourceKey or MediaFileID conflicts.
// Replays preserve manual edits and never revive a deleted library entry.
type ImportCopyFileRequest struct {
	ProtocolVersion    int    `json:"protocolVersion"`
	ProjectID          string `json:"projectId"`
	IntegrationVersion int    `json:"integrationVersion"`
	ImportID           string `json:"importId"`
	ResourceKey        string `json:"resourceKey"`
	SourceKey          string `json:"sourceKey"`
	MediaFileID        string `json:"mediaFileId"`
}

type ImportCopyFileResponse struct {
	Item ResourceItem `json:"item"`
}

func (r ImportCopyFileRequest) Validate() error { return validateTyped("importCopyFileRequest", r) }

func (r ImportCopyFileResponse) ValidateFor(req ImportCopyFileRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	if err := validateTyped("importCopyFileResponse", r); err != nil {
		return err
	}
	var value string
	if json.Unmarshal(r.Item.Value, &value) != nil || value == "" {
		return invalid("invalidCopyFileValue", "/item/value")
	}
	return validateResourceItem(r.Item, "/item")
}

// ValidateFileImportDeclaration supplements ValidateDeclaration. Existing
// integrations need no new endpoint until a source opts into file import.
func ValidateFileImportDeclaration(sources map[string]ResourceSource, importURL string) error {
	if _, err := validateResourceSources(sources); err != nil {
		return err
	}
	for key, source := range sources {
		if source.FileImport != nil && importURL == "" {
			return invalid("fileImportEndpointRequired", Pointer("/resourceSources", key))
		}
	}
	return nil
}

// ValidateFileImportSource binds a callback to the pinned catalog. The receiver
// must still check target project ownership and persist the receipt atomically.
func ValidateFileImportSource(sources map[string]ResourceSource, req ImportCopyFileRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	if _, err := validateResourceSources(sources); err != nil {
		return err
	}
	source, found := sources[req.SourceKey]
	if !found || source.FileImport == nil {
		return invalid("fileImportSourceRequired", "/sourceKey")
	}
	return nil
}

func validateCopyFile(ref CopyReference) error {
	if ref.File == nil {
		return nil
	}
	if ref.Resource.Kind != "integrationResource" || ref.ReadAs != "value" || ref.WriteAs != "value" || ref.Access != "" {
		return invalid("invalidCopyFileReference", ref.Path)
	}
	// File-library identities use strings. Do not coerce numbers or lists into IDs.
	var id string
	if json.Unmarshal(ref.Value, &id) != nil || id == "" {
		return invalid("invalidCopyFileValue", ref.Path)
	}
	return nil
}
