# Signed copy callbacks

Register the read-only POST handlers on the paths declared in the manifest:

```go
guard := func(ctx context.Context, version int) error {
    // Look up versions whose settings this deployed integration understands.
    // Populate this registry from catalog registration / compatibility data.
    if !supportedVersions.Contains(version) {
        return integration.ErrCopyUnsupportedVersion
    }
    return nil
}
mux.Handle("/resource-values", verifier.HandleResourceValues(guard,
    integration.ResourceValuesHandlers{
        Lookup: service.ResourceValues,   // search and resolve
        Identify: service.IdentifyResources,
    }))
mux.Handle("/prepare-copy", verifier.HandlePrepareCopy(guard, service.PrepareCopy))
mux.Handle("/validate-copy-settings", verifier.HandleValidateCopySettings(guard, service.ValidateCopySettings))
```

This is a wiring example: the version registry and service callbacks belong to
the integration. A nil guard fails with 503; an unknown version fails with 409.
Do not accept every version or silently switch to the latest published version.
The SDK does not install handlers or opt blocks into copying automatically.

All handlers verify the existing platform signature over the exact request body
through JWKS. They reject duplicate keys, unknown fields, malformed/trailing
JSON, unsupported protocol versions and bodies over 1 MiB. The shared verifier
also rejects oversized requests rather than verifying a truncated signed prefix.

## Lookup

`Lookup(ctx, schemetransfer.LookupRequest)` returns `LookupResponse`; `Identify`
returns `IdentifyResponse`. `mode` explicitly selects search, resolve or identify.
An empty resolve/identify batch is invalid; it never becomes search. Batches and
pages contain at most 100 items. An omitted search limit allows at most 100 items;
the caller may choose a smaller default. Resolve items must have distinct IDs
from the requested batch. Missing IDs are omitted. Pagination is search-only.
Identify must return exactly one match for every input index, with status found,
missing or ambiguous; results may arrive in a different order.

`ResourceItem.id` is the selection identity. `value` is the trusted value to put
into settings; it can be a string, number or composite JSON. They need not be
equal. A resource with `selectable:false` must have a nonempty `reason`.
The receiving service must verify ownership by `projectId`, the declared source,
parent parameters, constraints and availability. SDK shape validation does not
grant access. Never trust an ID merely because it came from a selector.

## Preparation and validation

`PrepareCopy` receives source project, scheme, pinned integration version,
block key, original settings and `protocolVersion: 2`. It returns the matching
protocol version, prepared settings, a private `plan` and `issues`. The SDK
[builder](copy-callback-v2.md) records concrete references, template fields and
implicit outputs. Reference values are separated from settings into the private
plan. The exporter resolves them and creates portable resources/bindings; the
callback response itself must never be published as the template.

`ValidateCopySettings` receives target project, pinned version, block key and
restored settings with `protocolVersion: 2`. It returns `ValidationResult`. The SDK checks consistency:
any error issue means `blocked`, otherwise a review issue means `needsReview`,
otherwise `passed`. Empty issues are `[]`, not null. Domain incompatibility is
a successful HTTP 200 result and is not retried as a transport failure.

These callbacks must only read. Do not reuse execution handlers that send
messages, create payments, save settings or activate triggers. Dynamic settings
and provider capabilities remain the integration's domain responsibility.

## Transport failures

After authentication, errors have `{ "code": "...", "error": "safe message" }`:

- 400 `invalidRequest`: invalid wire body or `ErrCopyInvalidRequest`.
- 404 `unknownSource` / `unknownBlock`: matching SDK sentinel error.
- 409 `unsupportedVersion`: `ErrCopyUnsupportedVersion`.
- 409 `copyConflict`: `ErrCopyConflict` (a file receipt with different input).
- 404 `resourceNotFound`: `ErrCopyNotFound` (including a deleted file result).
- 503 `unavailable`: `ErrCopyUnavailable` or unconfigured handler/guard.
- 500 `callbackFailed` / `invalidResponse`: callback error or invalid response.

Wrapped sentinel errors are supported. Arbitrary callback error text and
response settings are neither returned nor logged by these wrappers. Signature
failures keep the existing verifier's 401 response. Integration-owned logs must
also avoid settings and credentials.

`schemetransfer.ValidateCallbackRequest` and `ValidateCallbackResponse` are the
same validation used on the platform side; the latter binds a response to the
original request. No callbacks, requests or resource writes happen in these
pure functions. Existing `HandleVariableValues` filters remain compatible.

## Catalog and source checks

`Catalog.StartSyncWithObserver(ctx, manifest, observer)` retains startup retry and
logging behavior and reports the successful SyncResult once. The observer must
check `Published` and positive `Version`; an unpublished draft is also reported.
An integration can atomically remember this version for its CopyVersionGuard.
Until successful publication, copy callbacks remain unavailable; ordinary actions
and service readiness do not depend on it. This conservative policy accepts only
the version registered by the running deployment. Supporting older contracts
requires an explicit compatibility policy; never accept arbitrary versions.

`schemetransfer.ValidateSourceLookup` validates supported modes, known/required
parent parameters, their declared value types and source constraints. It does not
resolve parent IDs or check ownership. Callers assemble parent values only after
resolving their identities through the appropriate source in the target project.
`ValidateResourceValue` validates returned values, including exact integer types.
Constraints schemas cannot load external resources.

## File registration

File-capable sources additionally use `HandleImportCopyFile` on
`Manifest.ImportCopyFilePath`. It verifies the same signature, version guard and
strict JSON contract, but the domain callback **writes** a target library entry.
It must persist a receipt and must not execute a block or upload to a messenger.
See [file markers, receipt behavior and media client](copy-files.md).
