# Copy callbacks (v0.28.0)

Register three independent POST handlers on the paths declared in the manifest:

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
block key and original settings. It returns explicit settings and `issues`.
The exporter must still run copy rules on those settings before publication;
prepared settings can contain source IDs and are not a portable document.

`ValidateCopySettings` receives target project, pinned version, block key and
restored settings. It returns `ValidationResult`. The SDK checks consistency:
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
