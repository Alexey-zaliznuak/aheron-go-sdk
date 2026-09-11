# File transfer for scheme copies

This is an SDK contract and transport. Backend must orchestrate snapshot capture,
revision ownership and import; an integration must implement persistent library
registration. Adding a marker alone does not enable automatic file copying.

## Source preparation

An integration library entry and a media-service file have different identities.
The integration reads the entry in the source project and resolves its media ID:

```go
b := schemetransfer.NewCopyBuilder(preparedSettings)
b.File("/fileIds/0", "files", mediaFileID)
b.File("/fileIds/1", "files", anotherMediaFileID)
b.Template("/text")
return b.Build()
```

`File` accepts a path to one existing string value, including an array element.
The native library ID at that path becomes `value` in a private reference and
the setting becomes null:

```json
{
  "path": "/fileIds/0",
  "resource": {"kind": "integrationResource", "sourceKey": "files"},
  "value": "library-entry-id",
  "readAs": "value",
  "writeAs": "value",
  "file": {"mediaFileId": "33333333-3333-3333-3333-333333333333"}
}
```

The platform still identifies/deduplicates the source library entity through its
catalog. The same entity in several blocks has one mapping and one imported
library entry. Identical media IDs can share one snapshot asset even when two
different library entities reference them. Source identity, media ID and this
private callback response never belong in the public template. Backend must
reject conflicting media IDs for one canonical source entity across blocks;
the SDK checks conflicting exact values within one plan.

Only mark hosted content that actually represents the attachment. An external
video whose hosted file is a poster is not a copyable video. Do not export a
provider's cached upload ID, a token, or an arbitrary URL as a media file.
Uncopyable attachments can remain ordinary `Resource` references for deferred
setup. Text and list count/order are edited after copying, in the block editor.
`ResourceList` continues to mean remapping, without snapshot capture.

## Integration manifest

The file capability belongs to one resource source, not to every block field:

```go
manifest.ImportCopyFilePath = "/copy/files/import"
manifest.ResourceSources["files"] = schemetransfer.ResourceSource{
    ValueType: "string",
    IdentityScope: "project",
    Supports: []string{"search", "resolve", "identify"},
    FileImport: &schemetransfer.FileImport{Namespace: "messengers"},
}
```

The resolved manifest publishes `importCopyFileUrl` and
`resourceSources.files.fileImport.namespace`. Namespace is the target storage
owner/purpose, read from the pinned manifest, never a user-entered URL.
File import sources require string values, project scope, identify/resolve and
no parent parameters. `ValidateFileImportDeclaration` checks the endpoint;
`CopyPlan.ValidateSources` rejects file markers on undeclared file sources.

## Target registration

After media-service returns a target file, backend calls the pinned integration:

```json
{
  "protocolVersion": 2,
  "projectId": "11111111-1111-1111-1111-111111111111",
  "integrationVersion": 7,
  "importId": "22222222-2222-2222-2222-222222222222",
  "resourceKey": "r0",
  "sourceKey": "files",
  "mediaFileId": "44444444-4444-4444-4444-444444444444"
}
```

```go
mux.Handle("/copy/files/import",
    verifier.HandleImportCopyFile(guard, service.ImportCopyFile))
```

The signed transport validates shape/version before invoking domain code. It
does not implement persistence. The service must:

1. Check the current target installation and the pinned source using
   `ValidateFileImportSource`.
2. Check the receipt `(projectId, importId, resourceKey)`. A different sourceKey
   or mediaFileId returns `ErrCopyConflict`. A replay returns the current library
   entry, preserving renamed/replaced content; a deleted result returns
   `ErrCopyNotFound`, without recreating it.
3. For a new receipt, call `Files.WithAPIKey(targetKey).Get(ctx, mediaFileID)` and
   verify namespace, availability and domain restrictions. Read metadata from
   the target media service, not from source settings or caller-provided URLs.
4. Atomically create the library entry and its receipt, rechecking installation
   state. Concurrent retries return one entry. External calls stay outside DB
   retry callbacks. Retain receipts for the import lifecycle, including after
   manual deletion; cleanup must not reopen old receipt addresses.
5. Return the integration's resource identity and settings value:

```json
{"item":{"id":"target-library-id","value":"target-library-id","title":"Document.pdf","selectable":true}}
```

The receiver must retain enough provenance to check replays after restart.
This callback registers metadata only. It does not send messages, activate
triggers, or populate provider upload caches. Backend validates the returned
resource against the target source and then runs normal block validation.

## Platform-only media client

`transfermedia.New(internalURL, internalToken, httpClient)` is for backend, not
integration/browser code. It never follows redirects and bounds request time
and response size. Every method makes one HTTP attempt, returns sanitized typed
errors, and requires the caller to authorize the owning revision and projects.

| Method | Internal media-service operation |
| --- | --- |
| `Capture(ctx, snapshotID, assetID, CaptureRequest)` | PUT snapshot asset from source project/file |
| `GetAsset(ctx, snapshotID, assetID)` | GET ready asset metadata |
| `RetireSnapshot(ctx, snapshotID)` | DELETE snapshot, permanent fence and asynchronous cleanup |
| `Import(ctx, projectID, importID, resourceKey, ImportRequest)` | PUT target file from snapshot/asset and namespace |
| `GetImport(ctx, projectID, importID, resourceKey)` | GET current imported file |

Backend allocates and persists stable snapshot/asset/import addresses before
external writes. Lost responses retry the same address. The service returns
the same target file ID, including current manual edits; a deleted result is
not recreated. The client checks returned asset identities and target namespace,
rejects duplicate/unknown/trailing fields, and never accepts arbitrary source URLs.
`ErrConflict` can mean a changed request or an incomplete capture/import; callers
must inspect their durable operation state rather than allocate a new ID.

Snapshot retirement requires the media-service retirement endpoint and migration
00013. First revoke revision access and persist the retirement intent in backend;
then retry RetireSnapshot using that same ID until acknowledged. Only an empty
204 is acknowledgement, not 200/202. The client does not retry by itself or follow
redirects. Retirement also fences a not-yet-created snapshot. Never reuse this ID.
New/pending captures and imports are blocked; completed target files remain.
Success means durable acceptance, not that every S3 object has already disappeared.
Media inventory recovers COPYs finishing after the first deletion.

The client does not decide retention, revoke links, store retirement intents or
implement backend recovery. A 409 during capture/import can also mean retirement;
do not bypass it by creating another operation.

## Rollout and unfinished orchestration

Publish this SDK before updating consumers. First deploy backend support for
the new manifest fields, private file markers, snapshot ownership/retirement,
limits and resumable import stages. Then deploy integration registration with
durable receipts and enable `File` markers in its new catalog version. Finally
enable automatic file mappings in the import flow. An older backend rejects
these new fields; do not publish enabled file manifests against it.

Media-service capture/import and storage generations are already implemented in
the platform. This SDK release does not itself add backend orchestration,
Messengers receipts, a snapshot retention policy, or copy-wizard controls.
