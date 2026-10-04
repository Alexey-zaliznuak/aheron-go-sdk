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
