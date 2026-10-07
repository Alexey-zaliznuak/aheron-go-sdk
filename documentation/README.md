# Documentation and the public platform library

This package owns exchange DTOs and HTTP transport. Services still own technical
text, runtime schemas, examples and publication permissions.

`Catalog`/`Read` address one technical package. `LibraryCatalog`, `LibraryRead`,
`LibrarySearch`, and `LibraryBootstrap` address the unified platform library:
published manual articles plus packages matching an observed `LibraryProfile`.
The library uses `/public/knowledge/library/*`; existing endpoints remain unchanged.

```go
client, err := docs.New(docs.Config{BaseURL: "https://docs.aheron.pro/api/documentation"})
if err != nil { return err }
profile := docs.LibraryProfile{Locale: "ru", Contracts: []docs.Contract{
    {ProviderKey: "platform/execution", ContractRevision: "native-blocks/1"},
}}
catalog, err := client.LibraryCatalog(ctx, docs.LibraryCatalogRequest{Profile: profile})
if err != nil { return err }
for _, document := range catalog.Documents {
    content, err := client.LibraryRead(ctx, docs.LibraryReadRequest{
        Profile: profile, Ref: document.Ref, Audience: "agent",
    })
    if err != nil { return err }
    _ = content // Preserve reference, audience and contentSha256 in the run's provenance.
}
```

Obtain contracts from the actual runtime; the example does not prescribe a current
version. A profile filters documentation and grants no API permissions. Library
calls never attach a publisher/user token. HTTP timeout, caller cancellation,
redirect rejection and bounded JSON handling are shared with the existing client.
`LibraryRead` checks returned identity, audience and rendered SHA-256; bootstrap
checks every included text hash and audience.

`LibraryRef` is a union: `source=article` with `articleId` and `revision`, or
`source=package` with a `package` reference. Article refs stop resolving when the
publication changes (409), or is withdrawn (404). Refresh discovery instead of
silently replacing a pinned text. Published package refs remain readable after
channel changes, but revoked packages return 410 and must be evicted from caches.

The catalog returns at most 50 metadata records per page. Its opaque cursor is
bound to a snapshot, profile and filters; a changed snapshot returns 409.
Missing contracts and excluded incompatible documents are explicit. Search is
lexical over title, summary and topic, with `indexedAt` and `truncated`; it does
not claim full-text coverage. Authoritative reads recheck publication state.

Bootstrap returns complete documents, a catalog page, missing topics and deferred
references. `incomplete=true` must not be interpreted as full platform knowledge.
Technical `ReadResult.fallback` is additive and indicates the absence of the
requested appendix. The complete-document hash and rendered-text hash differ.

Use `task test:documentation`. Publishing remains available through the existing
publisher methods and `knowledge-publish` CLI.

## Staging documentation with a runtime deployment

CI can separate immutable upload from publication. Run both against the same
exported artifact (do not regenerate it between jobs):

```sh
knowledge-publish --phase upload --base-url "$DOCUMENTATION_API_URL" --package documentation-package.json --release-sequence "$CI_PIPELINE_ID"
# Deploy the exact sourceRevision image and verify its readiness here.
knowledge-publish --phase activate --base-url "$DOCUMENTATION_API_URL" --package documentation-package.json --operation-id "pipeline-$CI_PIPELINE_ID" --release-sequence "$CI_PIPELINE_ID"
```

Both phases use `DOCUMENTATION_API_KEY`, a personal docs key with the owner's
current permissions. Store it as a masked/protected CI secret; never put it in an
artifact or a command argument. `--release-sequence` must increase for each new
release of one provider and stay unchanged across retries. GitLab pipelines can
use `CI_PIPELINE_ID`; other callers supply their own monotonic release number.
The package supplies provider and source SHA. These are publisher assertions,
not an attestation by the CI vendor. Docs authenticates the key and authorizes
its owner independently on every request.

Go callers set `Config.Publication = &PublicationContext{ProviderKey: ...,
SourceRevision: ..., ReleaseSequence: ...}` and supply the key through
`PublisherToken`. The SDK binds uploads and high-level activation to this
context and sends its headers only to publishing endpoints. Public reads never
receive the key or release metadata. A configured context is copied on creation.

Existing OIDC callers may still use `DOCUMENTATION_ID_TOKEN` instead, without
`--release-sequence`; verified token claims provide their metadata. Setting both
credential variables is an error. The default Go config remains compatible.
Upload validates and stores the package but does not expose it publicly. The
activate phase calls `ActivatePackage`, never uploads, uses the observed channel
revision once, and verifies both the recovered receipt and the active channel.
Retry an interrupted activation with the same operation ID; a concurrent or
superseding release is a conflict, not an automatic overwrite. CI must serialize
deployment and activation and prevent older jobs from deploying over a newer one.

`--phase publish` (the default) retains the existing combined `Client.Publish`
behavior for callers that explicitly want upload and immediate activation. These
helpers operate on the package's exact contract channel. Integration publishers
use `--current` on activation/publication, or the typed `PublishCurrent` and
`ActivateCurrentPackage` methods. They accept only `integration/<UUID>`, read
independent revisions for both channels, atomically promote them, and verify both
current and exact channel receipts. Upload with `--current` still only stages the
package. Existing `Publish`/`ActivatePackage` never implicitly promote current.
The lower-level `Activate` API also exposes `expectedCurrentRevision` for callers
managing their own publication flow.

## User-authorized article editing

The same SDK exposes `aheron.Client.Documentation` (or
`platform.NewDocumentation`) for the existing editorial API. Set
`aheron.Config.DocumentationURL` to the complete API prefix; the default is
`https://docs.aheron.pro/api/documentation`. Bind `UserTokenProvider` to the
current user. Project keys and integration OAuth do not grant editorial access.
The server checks platform-administrator or integration-owner permissions on
every call. No roles, owner identities or arbitrary URLs are accepted by methods.

Methods: `ListFolders`, `CreateFolder`, `ListProvisionableIntegrations`,
`ProvisionIntegrationFolder`, `ListArticles`, `GetArticle`, `CreateArticle`,
`UpdateArticle`, `PublishArticle`, `UnpublishArticle`. Folders expose `canManage`.
Integration provisioning is idempotent and obtains its owner/slug from backend.
DTOs are shared in this package; user transport stays in `platform`, separate
from the public reader and CI publisher credentials.

`CreateArticle` saves a draft. Read the current revision before updating;
`UpdateArticleParams` supplies complete title/slug/body/summary with
`expectedRevision`. A nil summary clears it. Omitted/nil knowledge preserves
the previous metadata and appendices; an empty knowledge object clears them.
Publication is a separate call at the reviewed revision. Both human and agent
appendices become public when published. Technical packages cannot be changed
through the editorial client.

Calls do not retry writes or follow redirects. A timeout/5xx after a write
requires reading current state before another attempt. A 409 requires rereading
and reconciling the edit. The shared transport bounds request JSON to 2 MiB;
the editorial client bounds response JSON to 16 MiB by default. Values are never
silently truncated. Public library requests still send no user token.
