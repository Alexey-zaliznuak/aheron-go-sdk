# Dynamic copy protocol v2

The block manifest opts in with `copyRules: {"version":2,"mode":"callback"}`.
The concrete settings are described by the preparation response. Both `prepareCopyUrl` and `validateCopySettingsUrl` are mandatory.
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
fields and version mismatches. Source
catalog checks and domain ownership checks remain required on the platform.

The platform hydrates the private source values, resolves native definitions and
integration identities, then creates its immutable sanitized template with local
resource refs and bindings. Source values and the private plan must not leak into
the public document or logs. On import, mappings change references only. Text,
array shape and ordinary content are edited later in the normal block editor.

Final validation is mandatory and read-only. It checks the actual target settings,
including provider-dependent options. An incomplete copy stays inactive and can
be configured later; completing setup validates the current settings, including
removal of optional attachments. The integration owns the semantic completeness
of its plan and the correctness of source and target settings. The platform owns
resource resolution, binding integrity and provenance of the immutable document.

Deploy backend and execution-service support before integration manifests using
this protocol. Requests and responses must match the negotiated version.

Physical copying of file bytes requires template-owned asset snapshots and target
integration file registration. `ResourceList(path, "files")` means remapping a
resource list; it does **not** copy bytes. `File(path, sourceKey, mediaFileID)`
marks a hosted file for the separate [file-transfer protocol](copy-files.md).
The builder performs no I/O. Deploy platform asset orchestration and integration
registration before enabling these markers. Never publish source storage URLs.
