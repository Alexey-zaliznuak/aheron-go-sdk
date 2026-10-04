# Runtime contracts and authoring fences

`Schemes.RuntimeContracts(ctx, projectID)` observes currently responding native
and Python executors. `unavailableProviders` is explicit: never replace a missing
observation with a build-time constant or latest documentation. This is not a
lease over a deployment or proof that all replicas/Kafka workers agree.

Native catalog entries expose `providerKey`, `contractRevision` and
`documentationTopic`; preparation returns `contractRevision` too. Existing calls
remain valid. Use `PrepareNativeBlockAtContract(..., expectedRevision)` to reject
a changed runtime or an old server that cannot confirm the revision. Carry the
observed revision in `GraphCommand.ExpectedNativeContractRevision` when saving.
This is independent of the graph's `baseRevision`. Versioned creates require valid
settings; legacy editor calls can still save incomplete drafts.

An HTTP 409 with `contract_revision_mismatch` satisfies both `ErrConflict` and
`ErrContractRevisionMismatch`. No write is retried with a newer contract. Reread
the catalog, prepare again and issue a new reviewed command. For an uncertain
write, reuse the exact original command/operationId: backend recovers its receipt
before checking the currently deployed runtime. Rollout: execution endpoints,
then backend, then version-aware clients. Old servers must not silently accept
an unconfirmed contract. A missing provider is unavailable, not compatible.
