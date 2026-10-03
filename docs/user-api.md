# Пользовательский API клиент

## Общий клиент существующего SDK

Все API остаются в одном основном модуле. Новая общая точка входа:

```go
import (
    aheron "github.com/Alexey-zaliznuak/aheron-go-sdk"
    "github.com/Alexey-zaliznuak/aheron-go-sdk/platform"
)

credentials, err := platform.StaticToken(accessJWT, expiresAt)
if err != nil { return err }
client, err := aheron.New(aheron.Config{UserTokenProvider: credentials})
if err != nil { return err }
projects, err := client.Projects.List(ctx)
// client.Schemes.List/Get и client.CRM используют тот же пользовательский JWT.
```

Config выбирает ровно один способ: `UserTokenProvider`, `ProjectAPIKey` или
`CRMOAuth`. Одновременно задать два нельзя. Projects/Schemes поддерживают
пользовательский credential; с другим способом вызов завершается локально
`platform.ErrCredential`. CRM — **та же реализация** `integration.CRMClient`,
теперь с дополнительным пользовательским transport. Старые project-key/OAuth
пути и их настройки повторов сохраняются; новый user transport не повторяет
запросы автоматически, не принимает cookies и не следует redirects.

`HTTPClient` и `AllowLoopbackHTTP` общей Config относятся к пользовательскому
transport. Для integration OAuth его HTTPClient задаётся внутри CRMOAuth.
Пользовательские права проверяет CRM API: SDK не приравнивает пользователя к
владельцу интеграции и не обходит проверки отдельных операций.

Вспомогательные пакеты `platform` и `integration` продолжают быть доступны.
Нового go.mod или версии для них нет; отдельным модулем остаётся только
существующий YDB adapter. Секреты и поставщики токенов привязываются к отдельному
клиенту; глобально менять credential между пользователями нельзя.

## Низкоуровневый пользовательский клиент

Пакет `github.com/Alexey-zaliznuak/aheron-go-sdk/platform` вызывает обычные
API Aheron от имени пользователя. Это отдельная ответственность от
`integration`: пользователю не нужны integrationId, установка или project API key.

## Существующий access JWT

```go
credentials, err := platform.StaticToken(accessJWT, expiresAt)
if err != nil {
    return err
}
client, err := platform.New(platform.Config{
    TokenProvider: credentials,
    // BaseURL: "https://aheron.pro/api", // значение по умолчанию
})
if err != nil {
    return err
}
projects, err := client.Projects.List(ctx)
```

`expiresAt` — `time.Time`; нулевое значение допустимо, тогда срок проверяет
конечный API. SDK проверяет только синтаксис JWT для передачи в HTTP, не его
подлинность. Ресурсный сервис обязан проверять подпись, issuer, audience, exp
и действующие права пользователя.

`AccessToken` скрывает значение из обычного fmt/JSON вывода. Не передавайте
credentials модели и не включайте исходную строку в собственные логи.

## Получение актуального токена

```go
credentials := platform.TokenProviderFunc(func(ctx context.Context) (platform.AccessToken, error) {
    raw, expiresAt, err := userSession.AccessToken(ctx)
    if err != nil {
        return platform.AccessToken{}, err
    }
    return platform.NewAccessToken(raw, expiresAt)
})
```

`userSession` здесь — прикладной поставщик токена, а не готовый компонент SDK.
Provider вызывается для каждого запроса и обязан быть безопасным для
конкурентных вызовов и привязанным к одному пользователю. Реализации OAuth
refresh и обмена MCP credential пока отсутствуют: пакет не выдаёт и не обновляет
токены сам. Ответ 401 возвращается вызывающему без скрытого повторного запроса.

Для другого пользователя создайте новый Client или `client.WithTokenProvider`.
Этот метод возвращает независимую копию credential binding, переиспользуя
HTTP-транспорт; исходный клиент не изменяется. Никогда не переключайте provider
общего клиента между пользователями изменением общей переменной.

## Реализованные методы

| Метод | Текущий API backend |
| --- | --- |
| `Projects.List(ctx)` | `GET /api/projects` |
| `Projects.Get(ctx, projectID)` | `GET /api/projects/{id}` |
| `Schemes.List(ctx, projectID)` | `GET /api/projects/{id}/schemes` |
| `Schemes.Get(ctx, projectID, schemeID)` | `GET /api/projects/{id}/schemes/{schemeId}` |
| `Schemes.GetGraph(ctx, projectID, schemeID)` | `GET /api/projects/{id}/schemes/{schemeId}/graph` |
| `Schemes.GetStep(ctx, projectID, schemeID, stepID)` | `GET /api/projects/{id}/schemes/{schemeId}/steps/{stepId}` |

ID проверяются до обращения за токеном. Полученная схема должна принадлежать
запрошенному проекту. Контракт текущих списков не содержит pagination; слишком
большой ответ возвращает явную ошибку, а не обрезанную выдачу.

`GetGraph` читает согласованный сохранённый граф одной `revision`: `steps`
(включая `settings`, position и integration reference), `branches` и `edges`
(включая from/to step, input/output key и branchId). Это draft graph, не снимок
исполнения. Проверяются принадлежность всех сущностей схеме, уникальность ID и
ссылки рёбер на шаги/ветки внутри ответа. `GetStep` проверяет ID шага и схему;
он читает текущее сохранённое состояние, которое может быть новее прежнего графа.
Настройки сохраняются как JSON без потери вложенных полей; вызывающий адаптер
отвечает за выбор передаваемых модели данных. Методы используют общий user JWT
transport, лимит ответа и текущие серверные права проекта без дополнительных
привилегий и без повторов. Существующие List/Get по-прежнему возвращают метаданные.

`Project.Settings` и `Project.Metadata` — исходные API данные. Адаптер инструмента
должен явно выбирать нужные модели поля, а не пересылать все настройки проекта.
Операции записи и пользовательские клиенты CRM/Media/Links ещё не реализованы.

## Транспорт и ошибки

- HTTPS обязателен; для локальной разработки можно явно включить
  `AllowLoopbackHTTP` и указать loopback URL. Удалённый plaintext HTTP запрещён.
- BaseURL фиксируется при создании клиента. Произвольных URL в методах нет.
  Redirects, cookie jar и автоматические повторы отключены.
- `HTTPClient` можно передать для настройки транспорта, включая TLS тестовых
  серверов. SDK копирует его, не меняя настройки исходного клиента.
- Timeout по умолчанию 30 секунд. `MaxResponseBytes` по умолчанию 4 MiB,
  максимальное настраиваемое значение 64 MiB.
- `*platform.APIError` содержит operation/status, но не тело ответа или URL.
  `errors.Is` распознаёт `ErrUnauthorized`, `ErrForbidden`, `ErrNotFound`,
  `ErrConflict`. Транспортные ошибки также не раскрывают upstream body/credentials.
- Контекст отмены проходит к provider и HTTP-запросу. Срок, известный provider,
  проверяется перед отправкой запроса.

Проектные ключи и integration OAuth продолжают обслуживаться существующими
клиентами SDK. Добавление пользовательского клиента не превращает их в
пользовательские токены и не расширяет серверные разрешения.

## Проверки

```text
task test:platform
task test:platform -- -race
task vet:platform
```

Тесты используют изолированные HTTP-серверы. Они проверяют транспортные контракты,
конкурентную изоляцию пользователей, запрет редиректов/cookies, отзыв через
ответ 401 без повтора, истечение перед отправкой, отмену и ограничение ответов.
Они не заменяют будущий тест пользовательского OAuth и реального MCP-клиента.


## Авторинг схем и файлы проекта

В том же SDK доступны `Schemes.Create/Update/Delete`, `ListOperations`,
`ApplyGraphCommand`, `NativeBlocks` и `PrepareNativeBlock`. Запись графа использует
существующий backend API операций: `baseRevision`, `operationId`, `editorSessionId`,
атомарный список edits. SDK не повторяет POST автоматически. При неопределённом
результате сохраняйте исходный command и operationId; при конфликте сначала читайте
актуальный граф. Create/Update схемы меняют метаданные, не активируют исполнение.

`PrepareNativeBlock` возвращает нормализованные настройки и inputKeys/outputKeys,
используя парсер execution-service через авторизованный backend API. Подготовка не
создаёт блок и не исполняет его. Описание настроек получает SDK из runtime, не
поддерживает собственную копию правил блоков.

`client.Files.List/Get/Rename/Delete/Usage` работают через пользовательские маршруты
`/projects/{projectId}/files`. Это API файлов проекта с проверкой membership в backend;
интеграционный `integration.Files` сохраняет свой контракт установки и namespaces.
Projects, Schemes и Files требуют пользовательский credential. CRM по-прежнему
использует общую реализацию для пользовательской, проектной и интеграционной авторизации.
Для создания SDK-клиента новый модуль не нужен. Все новые методы сохраняют ограничения
origin, запрет redirects/cookies, лимит ответа и отсутствие автоматических повторов.
