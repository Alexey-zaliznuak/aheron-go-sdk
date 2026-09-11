# Dynamic copy protocol v2

The block manifest opts in with `copyRules: {"version":2,"mode":"callback"}`.
No static settings tree, `unknownFields`, `readAs`, or per-field list is published
in that manifest. Both `prepareCopyUrl` and `validateCopySettingsUrl` are mandatory.
Shared `resourceSources` still declare selectors once per integration.

Preparation is read-only and receives the source project, scheme, pinned catalog
version and concrete settings. Validate settings using the integration's domain
logic, reject meaningful unknown options, make defaults explicit and discard only
derived/private data. Pass this audited output to the SDK builder:

```go
builder := schemetransfer.NewCopyBuilder(preparedSettings)
builder.Resource("/channelId", "channels")
builder.Template("/text")
if preparedSettings.WaitReply != nil && preparedSettings.WaitReply.SaveTextTo != "" {
    builder.SubjectVariable("/waitReply/saveTextTo", "string", "write")
}
return builder.Build()
```

The private callback response has `protocolVersion: 2`, `settings`, `plan` and
`issues`. The plan contains `references`, `templates` (JSON Pointers with the
`aheronVarsV1` dialect), and optional `implicitResources` for unavoidable output
variables that are not configurable in settings. Each reference carries a path,
resource selector, original JSON value and read/write encoding. The corresponding
settings value is null. This response is **not a public template**.

`Resource` selects an integration source. `SubjectVariable` marks a variable key,
including write targets that have no `{{...}}`. `Reference` supports project
variables, tags and explicit ID/key encodings. `ResourceList` marks each existing
element of a list, preserving count, order and duplicates. The platform groups
occurrences by canonical resource identity across the whole scheme. Users cannot
map two occurrences of one source entity independently. Unmarked prepared values
are literal; a literal button caption containing braces is not a variable.

`Build` rejects missing/overlapping paths, noncanonical pointers, invalid template
syntax and invalid reference encodings. Wire validation rejects unknown/duplicate
fields and version mismatches. A v2 request never accepts a v1 response. Source
catalog checks and domain ownership checks remain required on the platform.

The platform hydrates the private source values, resolves native definitions and
integration identities, then creates its immutable sanitized template with local
resource refs and bindings. Source values and the private plan must not leak into
the public document or logs. On import, mappings change references only. Text,
array shape and ordinary content are edited later in the normal block editor.

Final validation is mandatory and read-only. It checks the actual target settings,
including provider-dependent options. An incomplete copy stays inactive and can
be configured later; completing setup validates the current settings, including
removal of optional attachments. The static v1 schema audit is not an independent
semantic guarantee for v2: provenance of the server-created template and the
integration's preparation/final validation own that guarantee.

V1 remains available for pinned old catalog versions. New manifests must be
rolled out after platform support. Do not turn old v1 requests into v2 responses.

Physical copying of file bytes requires template-owned asset snapshots and target
integration file registration. `ResourceList(path, "files")` means remapping a
resource list; it does **not** copy bytes. No `File` helper is advertised until that
storage lifecycle is implemented. Never publish source storage URLs as substitutes.
