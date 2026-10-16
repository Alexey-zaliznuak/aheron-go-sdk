# Distributed read cache

`readcache` is a best-effort Redis read-through cache shared by platform services.
An empty URL disables caching and returns a nil cache (including no extra revision reads).
All callers get independent JSON-decoded values. Include tenant and representation
version in each key. Cached data is disposable and never replaces authoritative
authorization, transaction, execution-state or deduplication checks.

```go
cache, err := readcache.New(readcache.Config{
    URL: cfg.ReadCacheRedisURL, Namespace: cfg.ReadCacheNamespace,
}, log)
if err != nil { return err }
defer cache.Close()
value, err := readcache.Get(ctx, cache, "service:contract:v1:"+versionID,
    time.Hour, func(ctx context.Context) (Contract, error) {
        return store.ReadPublishedContract(ctx, versionID)
    })
```

For mutable aliases use `GetTagged` with resource/domain tags, followed by
`InvalidateTags` **after a successful DB commit** from every writer. Tags cover
aliases without reverse-index races. Invalidation changes a random generation;
hits and loads verify the generation again before returning. Expired generation
keys receive a new random generation, so old values cannot become visible again.
`Invalidate` removes an exact key and its lease atomically. Neither form can
publish an old in-flight load over a newer value.

The database commit and Redis invalidation are not a distributed transaction.
A process crash or Redis failure between them can leave a cached value until its
TTL. Use short TTLs for bounded staleness; use a **live authoritative revision**
in keys and validate it around the read when immediate freshness is required
(editable graph, live file identity). Never cache current permission decisions.
Published integration content is cacheable only if every writer enforces
immutability in its transaction. Transaction-bound readers must use their own
attempt-local memo; do not capture an active transaction in a shared loader.

Each key uses local singleflight and a Redis lease. The lease renews during a
slow source read; publish, renew and unlock compare the random owner token in
Lua. A late former owner cannot overwrite a successor. A lost/invalidation lease
causes the old loader to retry, not publish. The shared load has its own bounded
context (default 30s); canceling one waiter does not cancel other waiters. Loaders
must honor their supplied context. Redis operation timeout defaults to 500ms,
lease to 5s. Redis failure opens a one-second per-instance circuit and falls back
to the source under local singleflight. Cross-instance deduplication is possible
only while Redis is available; this is not a correctness lock for business writes.

Only successful results are cached. Corrupt/incompatible entries are discarded;
source errors are returned unchanged. Values above 4 MiB or unencodable values
are returned without storage. TTL is capped at 24h with up to 10% subtractive
jitter. `Get` counts source latency and lock wait against that TTL; a slow old
snapshot never gains a fresh TTL when it finally loads. `GetUntil` caps the
value at its own absolute deadline. Tag
generations live for 25h since last use. Key inputs are hashed; never put raw
bearer tokens in source keys, use their cryptographic digest. Logs do not contain
keys, URLs, values or raw Redis errors. `Statistics()` exposes hits, misses,
loads, waits, failures and invalidations for service instrumentation.

Use authenticated TLS (`rediss://`), environment-specific namespaces, and Redis
`maxmemory-policy noeviction` so locks and generations are not evicted separately.
Persistence should be disabled: recovering an old Redis snapshot can resurrect
pre-invalidation data. Cache OOM is handled like an outage. This implementation
uses a single Redis endpoint; the multi-tag script is not a Redis Cluster API.

`task test:readcache` covers concurrent instances, lease ownership/renewal,
invalidation races, alias generations, independent copies, cancellation, TTL,
expiry, corrupt entries, outages and key isolation with a real RESP/Lua test
server. `READCACHE_TEST_REDIS_URL` additionally enables `TestRedisLive` against a
deployment using an isolated temporary namespace; keep the URL out of logs.
