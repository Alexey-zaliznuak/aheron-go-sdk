# Documentation packages

`github.com/Alexey-zaliznuak/aheron-go-sdk/documentation` (Go package `docs`)
defines immutable technical knowledge and its bounded HTTP client in the existing
SDK module. Human/editorial articles keep their own API and lifecycle.

`Package` identifies the domain owner (`providerKey`), the explicit runtime
contract (`contractRevision`) and the checkout (`sourceRevision`, full Git SHA).
Documents contain common Markdown plus independent human and agent appendices.
They can declare related topics and exact required contracts. A SHA is attribution,
not a runtime compatibility check. The consumer must satisfy `requires` before
using a document for authoring.

V1 canonicalization is implemented once in `CanonicalPackage`:

- UTF-8 only, CRLF/CR become LF; code indentation and other whitespace survive.
- Ordered Go DTO fields serialized by compact `encoding/json`, with HTML escaping.
- Documents sorted by `(documentKey, locale)`; related topics and required
  providers sorted; empty collections encoded as `[]`.
- Duplicate identities, dependencies or topics are rejected.
- At most 64 documents and 2 MiB of canonical JSON. No trailing newline is hashed.
- `packageDigest` is SHA-256 of those bytes. Each catalog document hash covers
  its complete canonical document, including metadata and both appendices.
- A read hash covers the rendered audience's exact Markdown bytes. The client
  verifies this hash, reference and audience before returning the response.

Test vectors pin serialization; changing it requires a new format version.

```go
client, err := docs.New(docs.Config{
    BaseURL: "https://docs.aheron.pro/api/documentation",
    PublisherToken: func(ctx context.Context) (string, error) {
        return os.Getenv("DOCUMENTATION_ID_TOKEN"), nil
    },
})
// Handle err. Public Catalog/Read never send publisher credentials.
uploaded, err := client.Upload(ctx, exportedPackage)
// On success activate with an explicit expected channel revision and a stable
// operation ID. Never silently retry a CAS conflict with the newer revision.
receipt, err := client.Activate(ctx, docs.ActivateRequest{
    PackageDigest: uploaded.PackageDigest,
    ContractRevision: uploaded.ContractRevision,
    ExpectedRevision: expectedRevision,
    OperationID: operationID,
})
// If the activation outcome is unknown, call client.Receipt(ctx, operationID).
```

The API prefix includes `/api` (or the deployment's `/api/documentation`). HTTPS
is required; loopback HTTP is opt-in for local development. Every request has
a context deadline; responses are limited to 3 MiB. Redirects are rejected,
including redirects to the same host. `APIError` exposes HTTP status and the
server's stable error code without copying arbitrary response bodies into logs.

Catalog pages expose summaries, exact references and a channel snapshot. A
cursor from a replaced channel yields a conflict: restart catalog enumeration.
Reading a previously published package remains possible until it is revoked;
an uploaded but unactivated package is not public. There is no implicit `latest`.
# Публикация из CI

`Client.Publish(ctx, package, operationID)` выполняет загрузку и одну CAS-активацию.
Сначала восстанавливает квитанцию предыдущего запуска; после активации проверяет,
что канал всё ещё указывает на этот выпуск. Сетевой сбой активации разрешается
одним чтением квитанции. Конфликт не повторяется с новой `expectedRevision`.
При неизвестном исходе повторяют тот же пакет и operationID. Отозванный канал
требует явного восстановления оператором, а не подстановки ревизии 0.

CLI входит в этот же модуль, отдельный SDK устанавливать не нужно:

```sh
go run github.com/Alexey-zaliznuak/aheron-go-sdk/cmd/knowledge-publish \
  --base-url "$DOCUMENTATION_API_URL" \
  --package documentation-package.json \
  --operation-id "pipeline-$CI_PIPELINE_ID"
```

Запускать из модуля сервиса, зафиксировав версию SDK в go.mod. CI передаёт
`DOCUMENTATION_ID_TOKEN` через environment, а не аргументы. CLI требует HTTPS,
ограничивает размер входа 2 MiB, время всей операции одной минутой
(`--timeout`) и печатает только JSON-квитанцию. Не использовать job ID в operationID:
он меняется при повторном запуске job. Для нескольких выпусков одного provider
в одном pipeline добавлять стабильный суффикс контракта.

Проверка актуальности канала — точечная проверка, не блокировка последующего
deploy. CI должен отдельно сериализовать production deploy и отклонять устаревшие
deployment jobs. Экспортёры и тесты примеров принадлежат репозиториям исполнителей.
