# OAuth интеграций: установки и приложение

Пакет `integrationoauth` реализует профиль auth-service: Client Credentials,
Ed25519 `private_key_jwt`, opaque access token на срок до 5 минут. Он получает
токены только для уже зарегистрированного клиента и разрешённого grant.
Регистрацией клиента/публичного ключа и переносом согласий занимается платформа.

`integration.New` принимает опциональный `ExecutionOAuth` для Steps/Triggers
`CRMOAuth` для CRM, `FilesOAuth` для Files и `LinksOAuth` для проектных Links-методов.
Catalog и глобальный RegisterCallback используют отдельный ApplicationOAuth. Steps, Triggers, Catalog и RegisterCallback требуют OAuth; альтернативный вход через подпись удалён.

## Steps и Triggers

Создайте клиент для
конкретной установки (Provider переиспользуется между клиентами):

```go
client, err := integration.New(integration.Config{
    ExecutionURL: executionURL, // доверенный HTTPS адрес с префиксом API
    ExecutionOAuth: &integration.ExecutionOAuthConfig{
        Provider: provider,
        ProjectID: projectID,
        InstallationID: installationID,
    },
})
```

Audience фиксирован: `execution`. Resolve/Reactivate запрашивают
`integration.resolve`, а с непустыми variables дополнительно `variables.write`.
Activate/List/ListTriggers запрашивают `triggers.activate` (отдельного scope
чтения триггеров сейчас нет). Эти scopes должны входить в разрешённый набор
audience auth-service и принятые обязательные права установки.

ProjectID в Activate/List должен совпадать с конфигурацией. ProjectID контекста
Resolve/Reactivate проверяется, если присутствует; на сервере всегда проверяется
проект реально сохранённого execution context. Нельзя брать installationId из
недоверенного запроса. SDK не регистрирует установку и не выдаёт ей права.

Методы Steps и Triggers используют ExecutionOAuth;
ошибка токена/HTTP не переключает их обратно. GET может повториться один раз
после 401. POST повторяется после 401 только для ResolveWithOptions /
ReactivateWithOptions с непустым устойчивым IdempotencyKey; variables вместе с
ключом по контракту запрещены. Activate и обычный Resolve/Reactivate не
повторяются автоматически. 403/5xx/неопределённый сетевой сбой возвращаются
вызывающему. Настройки RetryCount транспорта не влияют на OAuth.

Ошибка ресурсного HTTP сохраняет APIError/StatusCode/IsUnauthorized, но не тело
ответа; upstream может отразить секрет. Ответы списка/активации ограничены 1 MiB.
OAuth проверяет разрешение команды при приёме HTTP. Уже принятая команда в Kafka
продолжает штатное выполнение; истечение токена не отменяет принятую команду.

## CRM

Включение CRM независимо от ExecutionOAuth; обе настройки могут использовать
один Provider и одну доверенную привязку установки:

```go
client, err := integration.New(integration.Config{
    CRMURL: crmURL,
    CRMOAuth: &integration.CRMOAuthConfig{
        Provider: provider,
        ProjectID: projectID,
        InstallationID: installationID,
    },
})
```

Audience фиксирован: `crm`. Все существующие методы CRMClient, включая Ensure,
получают OAuth-токены: чтение — `crm.read`, subjects/tags — `crm.write`, изменение
переменных и их определений — `variables.write`. Upsert запрашивает дополнительно
`variables.write`, если create/update содержат поля переменных; built-in
displayName/description этого дополнительного scope не требуют.

ProjectID каждого вызова должен точно совпадать с конфигурацией; object IDs в
путях — канонические UUID. Изменять project/installation через WithAPIKey нельзя:
копия сохраняет OAuth. Для другого проекта создаётся клиент с его CRMOAuth
(Provider общий); для project API key нужен отдельный клиент без CRMOAuth.

GET повторяется после 401 не более одного раза. Все CRM-записи, включая PUT,
PATCH, DELETE и Ensure, автоматически не повторяются: у сервера нет durable
idempotency receipts. Ensure по-прежнему трактует 409 как уже существующее
определение. Ошибка выдачи токена, 403/5xx/сбой транспорта не включает API-key fallback.

Ответы ограничены 1 MiB и ожидаемыми HTTP-статусами. APIError сохраняет статус,
но OAuth не возвращает body/сырые ошибки декодирования из upstream. Сервер CRM
проверяет scopes/проект/принадлежность переменных; клиент не выдаёт себе права
через integrationId. Контрактные HTTPS-тесты всех методов входят в test:oauth.

## Подключение

Приложение загружает настройки своим существующим механизмом конфигурации.
Пакет не читает переменные окружения и не выбирает production endpoint сам.
`clientId`, `keyId`, точный HTTPS token endpoint и Ed25519 private key берутся
из доверенной конфигурации интеграции. `projectId`/`installationId` берутся из
постоянной привязки установки; это не произвольные поля внешнего запроса.

```go
import (
    "context"
    "crypto/ed25519"
    "fmt"
    "io"
    "net/http"

    "github.com/Alexey-zaliznuak/aheron-go-sdk/integrationoauth"
)

// Создать один раз на клиент/ключ/окружение и переиспользовать между установками.
func newProvider(clientID, keyID, tokenEndpoint string, privateKey ed25519.PrivateKey) (*integrationoauth.Provider, error) {
    return integrationoauth.NewProvider(integrationoauth.Config{
        ClientID: clientID,
        KeyID: keyID,
        PrivateKey: privateKey, // полный 64-байтный Ed25519 key
        TokenEndpoint: tokenEndpoint,
        MaxEntries: 1024,
    })
}

// Пример запроса к ресурсу после включения OAuth в этом ресурсном API.
func readResource(ctx context.Context, provider *integrationoauth.Provider,
    baseURL, requestURL, projectID, installationID string) ([]byte, error) {
    client, err := integrationoauth.NewClient(integrationoauth.ClientConfig{
        Provider: provider,
        BaseURL: baseURL, // доверенный HTTPS origin и префикс API
        TokenRequest: integrationoauth.Request{
            ProjectID: projectID,
            InstallationID: installationID,
            Audience: "crm",
            Scopes: []string{"crm.read"},
        },
    })
    if err != nil { return nil, err }
    req, err := http.NewRequest(http.MethodGet, requestURL, nil)
    if err != nil { return nil, err }
    response, err := client.Do(ctx, req)
    if err != nil { return nil, err }
    defer response.Body.Close()
    if response.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("resource API returned HTTP %d", response.StatusCode)
    }
    // Декодировать контракт конкретного API и выбрать подходящий лимит ответа.
    return io.ReadAll(io.LimitReader(response.Body, 1<<20))
}
```

При существующем 32-байтном seed полный ключ получается через
`ed25519.NewKeyFromSeed(seed)` после проверки длины. Constructor проверяет полную
пару public/private и сохраняет копию ключа. Встроенного `client_secret` нет.
Audience токена и точный token endpoint — разные настройки: endpoint становится
`aud` подписанного assertion, resource audience передаётся отдельным form-полем.

Для ротации платформа сначала регистрирует новый `keyId`/public key, затем
приложение создаёт новый Provider и переводит на него свои ресурсные клиенты.
После обновления всех реплик старый ключ можно отозвать. Изменение исходного
Config или slice приватного ключа не меняет уже созданный Provider.

## Выдача и кеш

`provider.Token(ctx, request)` возвращает `Token`: `Bearer()` явно раскрывает
секрет для заголовка, `ExpiresAt()` и `Scope()` возвращают метаданные. Обычные
`fmt`/JSON представления Token не раскрывают bearer. Нельзя логировать результат
`Bearer()`, Authorization или тела token endpoint.

- Provider закреплён за clientId/keyId/endpoint; его кеш дополнительно разделён
  по projectId, installationId, audience и отсортированному точному набору scopes.
  Подмножества и надмножества не переиспользуют один токен.
- Обновление происходит при следующем обращении за 10–15% срока до истечения,
  со случайным разбросом: для 300 секунд это 30–45 секунд. Фоновых циклов нет.
  Срок считается от начала HTTP-запроса, поэтому задержка ответа его не увеличивает.
- Одновременные запросы одного набора ждут одну выдачу. Отмена одного ожидания
  не отменяет остальные; уход последнего ожидающего отменяет HTTP-запрос.
- `MaxEntries` ограничивает размер кеша и количество одновременных различных
  выдач: default 1024, максимум 10000. Кеш вытесняет давно не использованные записи;
  при заполнении всех мест активных выдач возвращается `ErrCapacity`.
- Ошибка обновления возвращается вызывающему, без старого токена и без другого credential
  fallback. Ошибки выдачи не кешируются. `Invalidate(request, token)` удаляет
  только указанный токен: запоздалый 401 не вытесняет более новый.

Каждая попытка выдачи подписывает новый случайный `jti`, `iat/exp` — целые секунды,
срок assertion 60 секунд. Часы должны быть синхронизированы с auth-service.
Token POST не повторяется этим пакетом автоматически: после 503/неизвестного
результата последующий вызов Token использует новый assertion. Длительность
выдачи ограничена 30 секундами по умолчанию; `Config.HTTPClient.Timeout` может
задать меньшее положительное значение, но не больше 30 секунд.

Ответ token endpoint ограничен 16 KiB и проверяется строго: JSON, точные поля,
без дублей/NULL/case aliases, Bearer формата `aho_`, целый TTL 1–300 и ровно
запрошенный набор scopes. Неизвестные OAuth extensions игнорируются. Стандартные
поля OAuth сохраняют snake_case по согласованному протоколу; `projectId` и
`installationId` используют camelCase.

## Ресурсные запросы и повтор после 401

`Client` принимает абсолютные URL только внутри настроенных HTTPS origin и
префикса пути. Иные получатели, Host override, неоднозначные dot/escaped пути
и смешивание OAuth с Authorization/cookie/signature/API-key headers
отклоняются до получения токена. Redirects не выполняются ни на выдаче токена,
ни при ресурсном запросе. Переданный HTTPClient копируется; его cookie jar
не используется, настройки самого вызывающего не меняются. Пользовательский
RoundTripper остаётся доверенным кодом: он должен соблюдать context, не
перенаправлять credentials и не логировать секреты.

`Do` обновляет отклонённый токен и повторяет запрос один раз после 401 только
для GET/HEAD. Обычный POST/PATCH/DELETE возвращает первоначальный 401 без повтора.
`DoIdempotent` позволяет такой же однократный повтор записи, только если вызывающий
знает, что ресурсный API гарантирует безопасный replay, например через постоянную
дедупликацию. Один Idempotency-Key без серверной гарантии недостаточен.
Непустое тело должно иметь `Request.GetBody`; streaming body автоматически не
повторяется. `http.NewRequest` устанавливает GetBody для bytes.Reader,
bytes.Buffer и strings.Reader.

Второй 401 возвращается наружу и тоже инвалидирует отклонённый токен. После 403,
5xx или транспортной ошибки пакет повтор не добавляет. Сетевую политику базового
RoundTripper, включая стандартное восстановление соединения Go для идемпотентных
запросов, определяет сам transport. Возвращённый response.Body закрывает вызывающий;
request.Body закрывается HTTP-клиентом либо при отказе до отправки.

Resource API по-прежнему обязан делать online introspection, проверять audience,
scope и владельца объекта. Кеш SDK — только кеш credential, он не кеширует
решение о доступе и не гарантирует актуальность прав между запросами.

## Files: media API и загрузка байтов

`integration.Config.FilesOAuth` включает OAuth для всех существующих методов Files.
Один client привязан к projectId/installationId. `WithAPIKey` сохраняет эту привязку;
для project API key создайте отдельный client. Общий Provider можно использовать
для разных установок — его кеш разделён по installation/project/audience/scopes.

```go
client, err := integration.New(integration.Config{
    FilesOAuth: &integration.FilesOAuthConfig{
        Provider: provider,
        ProjectID: projectID,
        InstallationID: installationID,
    },
})
if err != nil { return err }
file, err := client.Files.Upload(ctx, "report.csv", "text/csv", content)
```

Audience фиксирован: `media`. List/Get/Usage запрашивают `files.read`,
Upload/UploadToNamespace/Replace/Rename/Delete/PurgeNamespace — `files.write`.
Права проверяет media-service; namespaces в v1 не ограничивают grant.
OAuth API требует доверенный HTTPS MediaURL (по умолчанию /api/media).

Upload сначала получает presigned PUT URL через OAuth API, затем отправляет
байты отдельным HTTP client в object storage и делает finalize через OAuth API.
Storage PUT не содержит Bearer/API key/signature/Cookie; перенаправления запрещены,
таймаут ограничен 5 минутами, тело защищено Content-MD5. При необходимости
настройте отдельный доверенный `UploadHTTPClient`; `HTTPClient` обслуживает только
media API. Client копирует настройки, убирает cookie jar и запрещает redirects,
не меняя исходный http.Client. URL загрузки должен быть HTTPS без userinfo/fragment,
метод PUT, uploadKey — временным UUID-ключом проекта этой установки.

GET допускает один повтор после 401 с новым токеном. Все write requests, включая
выдачу upload URL, storage PUT и finalize, автоматически не повторяются. Отзыв
во время загрузки приводит к отказу finalize; повтор Upload вручную может создать
другой файл. Файловые APIError сохраняют статус, но не upstream body; ошибки
декодирования и storage не раскрывают presigned URL, подпись или ответ сервера.
Публичные URL чтения байтов и TTL уже выданных S3 PUT URL OAuth не изменяет.

## Links: операции проекта и настройки приложения

`integration.Config.LinksOAuth` задаётся независимо от FilesOAuth/CRMOAuth:
Provider, ProjectID, InstallationID и опциональный HTTPClient. Audience `links`;
Get/List/Deliveries используют `links.read`, Create/Disable/Replay — `links.write`.
Каждый project аргумент должен совпадать с конфигурацией до HTTP-запросов.
WithAPIKey сохраняет OAuth-привязку; API key не служит fallback.
LinksURL должен быть доверенным HTTPS URL (по умолчанию https://link.aheron.pro/api).

Create передаёт стабильный Idempotency-Key и может один раз повторить 401 с тем же
телом/ключом: link-service сохраняет результат по project/principal/key на 24 часа.
GET также допускает один 401 retry. Disable и Replay автоматически не повторяются;
403/5xx/transport errors не вызывают повторов. Ошибки не содержат upstream body.

RegisterCallback меняет общий endpoint интеграции сразу для всех проектов и не
авторизуется правами установки. При LinksOAuth без ApplicationOAuth этот метод
возвращает ErrRequest до I/O. При обеих настройках RegisterCallback использует
ApplicationOAuth, а проектные методы сохраняют LinksOAuth.
Проектный client продолжает создавать ссылки с уже зарегистрированным EndpointKey.
Публичный redirect и исходящие подписи callback этим шагом не изменяются.

## Catalog и общие callback endpoints

```go
client, err := integration.New(integration.Config{
    CatalogURL: catalogURL, // доверенный HTTPS URL, включая /api
    LinksURL: linksURL,
    ApplicationOAuth: &integration.ApplicationOAuthConfig{
        Provider: provider,
    },
})
```

Provider может быть общим с установочными клиентами. Для Catalog.Sync запрашивается
audience catalog / catalog.write; для Links.RegisterCallback — links /
links.callbacks.write. Эти права платформа выдаёт клиенту отдельно от согласий
проектов. IntegrationID в этой конфигурации не нужен:
владельца каталога/endpoint определяет сервер по проверенному токену.

Низкоуровневый API: Provider.ApplicationToken(ctx, ApplicationRequest{Audience,
Scopes}), InvalidateApplication и NewApplicationClient(ApplicationClientConfig).
ApplicationRequest не содержит projectId/installationId. В token form передаётся
tokenKind=application, идентификаторы проекта/установки отсутствуют, включая пустые.
Application token имеет префикс aho_app_; ответ с установочным aho_ отвергается
и наоборот. Кэш/singleflight различают профиль, audience и точный набор scopes.

Оба management-метода объявляют желаемое состояние и допускают один повтор после
401 с новым токеном и тем же телом. 403/5xx/ошибка транспорта возвращаются без
повтора и без подписи/API key. Catalog.StartSync сохраняет свой отдельный
ограниченный цикл повторов декларации при старте. WithAPIKey не отключает OAuth.
Ошибки не содержат ответ upstream; успешный JSON ограничен 1 MiB.

Перед включением интеграции нужны application grant в auth-service и
INTEGRATION_APPLICATION_OAUTH_ENABLED=true в backend/link-service. Их ресурсные
флаги независимы от установочного OAuth; в auth-service application-флаг требует
включённого базового OAuth. Клиенты доступны начиная с SDK v0.37.0.

## Проверки

`task test:oauth -- -race -v` проверяет реальные HTTPS-запросы и Ed25519 assertions,
изоляцию/срок/ограничение кеша, singleflight и отмену, строгие ответы, ошибки,
редиректы, cookie jar, однократные повторы и повторяемость тела. Auth-service
эмулируется контрактным обработчиком; production окружение не вызывается.
`task test`, `task vet`, `task build` проверяют оба SDK-модуля; существующие
YDB-тесты второго модуля используют только локальную базу.

OAuth installation settings are delivered by the durable lifecycle endpoint and
validated against the current installation lifetime. Migration state is not part
of the SDK runtime contract.
