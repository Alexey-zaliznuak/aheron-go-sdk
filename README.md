# Aheron Go SDK (интеграции)

SDK для построения бэкендов интеграций Aheron на Go. Закрывает **обе** стороны
модели доверия платформы:

- **Входящее** (платформа → интеграция): `Verifier` проверяет Ed25519-подпись
  платформы (по JWKS, выбор ключа по `kid`) и свежесть timestamp. `Handle`
  оборачивает эндпоинт `action_url` (тело проектирует автор — читается через
  `DecodeBody` в свою структуру), а `HandleInstall` — эндпоинт `install_url`
  (фиксированное тело `{projectId, projectApiKey}`). `HandleVariableValues`
  обслуживает typed contract динамических значений переменных.
- **Исходящее** (интеграция → платформа): `Client` дёргает подписанные эндпоинты
  платформы (resolve шага, активация/список триггеров) — каждый вызов
  подписывается приватным ключом интеграции — и содержит `CRM`-клиент для
  чтения/записи данных субъекта по project API key.

Помимо этого в SDK есть `outbox` — relay транзакционного outbox, не зависящий
ни от какой БД (см. ниже).

Модуль: `github.com/Alexey-zaliznuak/aheron-go-sdk`. Требует Go 1.25+.

## Установка

```bash
go get github.com/Alexey-zaliznuak/aheron-go-sdk/integration
```

В репозитории два модуля. Корневой — тот, что нужен любой интеграции. Отдельно
лежит `aheron-go-sdk/ydb` с YDB-спецификой; он вынесен в подмодуль, чтобы
`ydb-go-sdk` вместе с деревом gRPC не попадал в `go.sum` интеграций на
PostgreSQL. Ставится и версионируется независимо, см. [`ydb/README.md`](ydb/README.md).

## Модель доверия

Асимметричная, без общих секретов:

- Платформа подписывает исходящие запросы своим приватным ключом и шлёт
  `X-Aheron-Timestamp` / `X-Aheron-Signature` / `X-Aheron-Key-Id`. Интеграция
  проверяет их публичным ключом из JWKS
  (`GET {origin}/.well-known/aheron-integration-jwks.json`).
- Интеграция подписывает свои callback'и (resolve, активация, список) **своим**
  приватным ключом и шлёт `X-Integration-Id` / `X-Integration-Timestamp` /
  `X-Integration-Signature`. Платформа проверяет их зарегистрированным публичным
  ключом интеграции.

Канон подписи одинаковый в обе стороны: Ed25519 над `"<timestamp>.<body>"`.

## Быстрый старт

```go
package main

import (
	"context"
	"net/http"
	"os"

	"github.com/Alexey-zaliznuak/aheron-go-sdk/integration"
	"github.com/Alexey-zaliznuak/aheron-go-sdk/integration/zaplog"
	"go.uber.org/zap"
)

func main() {
	logger, _ := zap.NewProduction()

	client, _ := integration.New(integration.Config{
		IntegrationID: os.Getenv("INTEGRATION_ID"),
		PrivateKey:    os.Getenv("INTEGRATION_KEY"), // base64 seed(32) или полный ключ(64)
		APIKey:        os.Getenv("AHERON_PROJECT_KEY"), // для CRM, опционально
		Logger:        zaplog.New(logger),
	})

	verifier, _ := integration.NewVerifier(integration.VerifierConfig{
		JWKSURL: os.Getenv("JWKS_URL"),
		Logger:  zaplog.New(logger),
	})

	// Установка: платформа шлёт фиксированное {projectId, projectApiKey} —
	// сохраните ключ, чтобы ходить в CRM от имени проекта.
	http.Handle("/install", verifier.HandleInstall(
		func(ctx context.Context, req integration.InstallRequest) error {
			saveAPIKey(req.ProjectID, req.ProjectAPIKey)
			return nil
		},
	))

	// Единый action-эндпоинт (action_url версии). Тело вы проектируете сами в
	// action_request_template; здесь декодируете его в свою структуру. В неё
	// встройте integration.ExecutionContext там, где шаблон содержит {{context}},
	// чтобы затем резолвить шаг.
	http.Handle("/blocks/action", verifier.Handle(
		func(ctx context.Context, r *http.Request) error {
			var body struct {
				integration.ExecutionContext             // {{context}}
				ActionKey                    string      `json:"actionKey"`
				Vars                         any         `json:"vars"`
			}
			if err := integration.DecodeBody(r, &body); err != nil {
				return err
			}
			return client.Steps.Resolve(ctx, body.ExecutionContext, "ok", map[string]any{
				"lastMessageId": "42", // subject-переменная по ключу
			})
		},
	))

	http.ListenAndServe(":8090", nil)
}
```

Полный минимальный пример — в `examples/echo`.

## Конфигурация клиента

`integration.Config`:

| Поле                                                       | Назначение                                                           | Дефолт                       |
| ---------------------------------------------------------- | -------------------------------------------------------------------- | ---------------------------- |
| `IntegrationID`                                            | id интеграции (uuid), уходит в `X-Integration-Id`                    | —                            |
| `PrivateKey`                                               | Ed25519 приватный ключ интеграции, base64 (seed 32б или полный 64б)  | —                            |
| `APIKey`                                                   | project API key (`ahr_proj_...`) для CRM                             | —                            |
| `ExecutionURL`                                             | база execution-service с префиксом шлюза `/api/execution`; эндпоинты интеграций под `/integrations/...` | `https://aheron.pro/api/execution` |
| `CRMURL`                                                   | база CRM с префиксом шлюза `/api/crm`; вызовы бьют в `/projects/...` | `https://aheron.pro/api/crm` |
| `MediaURL`                                                 | база media-service с префиксом шлюза `/api/media` (файлы проекта)    | `https://aheron.pro/api/media` |
| `CatalogURL`                                               | база backend с префиксом шлюза `/api` (каталог интеграций)           | `https://aheron.pro/api`     |
| `PublicBaseURL`                                            | свой внешний адрес без слеша на конце; относительные пути манифеста резолвятся по нему | —          |
| `Timeout` / `RetryCount` / `RetryWaitMin` / `RetryWaitMax` | транспорт                                                            | 30s / 2 / 0.5s / 5s          |
| `Logger`                                                   | реализация `integration.Logger`                                      | no-op                        |

Ретраятся только идемпотентные вызовы (GET, а также resolve/активация) на сетевых
ошибках и 502/503/504. Для resolve/reactivate, который вызывающий код может
повторить после потери ответа, передавайте стабильный durable-ключ через методы
`ResolveWithOptions` / `ReactivateWithOptions` и
`integration.ResolveOptions{IdempotencyKey: ...}`. Тот же ключ с тем же запросом
вернёт сохранённый accepted outcome, а с другим запросом — HTTP 409. Старые
`Resolve` / `Reactivate` остаются совместимыми и не посылают ключ. В текущем
keyed-протоколе `variables` должны быть `nil`/пустыми, а ключ — не длиннее 256
байт: SDK проверяет это до HTTP-запроса, потому что внешнюю запись CRM нельзя
атомарно объединить с YDB receipt.

## Возможности

**Исходящее** (`Client`, подписано ключом интеграции):

- `client.Steps.Resolve(ctx, execCtx, output, vars)` — резолв припаркованного
  `integrationAction`. `execCtx` (`integration.ExecutionContext`: `ID`, `Version`,
  `InputKey`, `StepID`) берётся из тела action-запроса (плейсхолдер `{{context}}`);
  вызов уходит на `ExecutionURL` + путь resolve.
- `client.Steps.Reactivate(ctx, execCtx, output, vars)` — повторная активация:
  прогоняет субъекта через выход шага, даже если контекст давно ушёл дальше
  («нажал старую кнопку ещё раз»). Корреляции по версии нет — платформа
  проверяет только владение шагом (`execCtx.StepID` обязателен) и перезаписывает
  позицию субъекта на ветке выхода, как активация триггера. Сохраняйте `StepID`
  вместе с `ID`, если интеграция поддерживает поздние активации.
- `client.Steps.ResolveWithOptions(...)` и `ReactivateWithOptions(...)` — те же
  операции с опциональным `idempotencyKey`. Ключ берите из долговечной identity
  действия/задачи и переиспользуйте на каждом retry; не генерируйте новый ключ
  на попытку. Для keyed-вызова передавайте `nil`/пустые `vars`.
- `client.Triggers.Activate(ctx, params)` — активация триггера по внутреннему
  `SubjectID` **или** по внешней идентичности (`IntegrationSubjectID` [+ `Type`]).
- `client.Triggers.List(ctx, projectID, blockKey)` — список инстансов триггера.
  Каждый `TriggerInstance` несёт `Settings` — сырой JSON настроек шага, как их
  сохранил редактор блока. По нему интеграция может построить собственный реестр
  правил (например, паттерны матчинга входящих сообщений) без отдельного канала
  синхронизации.
- `client.Triggers.ListTriggers(ctx, projectID, blockKey)` — то же, но
  возвращает `TriggerListing{ConfigVersion, Triggers}`: помимо инстансов отдаёт
  `configVersion` — счётчик конфигурации триггеров для пары (проект, интеграция),
  которым нужно защищать локальный снапшот правил (см. ниже про `trigger_sync`).
  `List` — обёртка над `ListTriggers`, отбрасывающая версию (`0`, если платформа
  старая и версию не присылает).

**Каталог** (`client.Catalog`, подписано ключом интеграции):

- `client.Catalog.Sync(ctx, manifest)` — публикация собственной декларации:
  блоки и URL-контракт версии. Подробнее — раздел «Манифест каталога».
- `client.Catalog.StartSync(ctx, manifest)` — то же на старте сервиса: джиттер,
  ретраи, результат в лог, ошибку наружу не отдаёт. Зовите горутиной.

**Данные** (`client.CRM`, по project API key):

- `UpsertSubject`, `GetSubject`, `ListSubjectVariables`, `SetSubjectVariables`.
- `CreateVariableDefinition`, `EnsureVariableDefinition` — объявление subject-переменных
  проекта. `Ensure` идемпотентен (конфликт `409` = «уже есть»), поэтому его удобно
  звать один раз на install/старт, чтобы гарантировать переменную перед upsert'ом
  субъектов по её ключу.
- `ListVariableDefinitions(ctx, projectID, params)` — список определений
  subject-переменных проекта (опционально фильтр `OwnerType`/`IntegrationID`).
  Удобен, чтобы в настройках блока предлагать выбор существующей переменной
  (например «сохранить ответ в переменную X») вместо свободного ввода ключа.
- `client.CRM.WithAPIKey(projectKey)` — дешёвая копия клиента с другим project API
  key поверх общего транспорта. Нужна, когда один процесс интеграции работает от
  имени многих проектов (у каждого свой ключ, выданный на install): держите один
  базовый клиент без ключа и деривируйте `WithAPIKey(...)` на каждый вызов.

Ветвление по ответу CRM: `integration.IsUnauthorized(err)` (401/403) и
`integration.StatusCode(err)` (точный статус `*APIError`, напр. `409`).

**Файлы** (`client.Files`, по project API key, платформенный media-service):

- `Upload(ctx, fileName, mimeType, content)` — сохранить файл в пользовательскую
  медиатеку (`library`) и получить `File` (в т.ч. `Namespace` и `URL` — стабильную
  публичную ссылку). Байты **не идут через media-service**:
  SDK сам получает presigned-ссылку, PUT'ит контент напрямую в объектное хранилище
  (с `Content-MD5` для целостности), затем финализирует; хэш контента сервис берёт
  из S3 ETag. Дедуп по содержимому в пределах неймспейса проекта.
- `UploadToNamespace(ctx, namespace, fileName, mimeType, content)` — то же, но в
  явный неймспейс: интеграция кладёт машинно-управляемые файлы (например, вложения
  диалогов) в собственный слаг, чтобы они не попадали в медиатеку пользователя и не
  могли быть оттуда удалены.
- `Replace(ctx, fileID, mimeType, content)` — заменить содержимое файла, сохранив его id
  (и его неймспейс).
- `List(ctx, ListParams{Namespace, Before, Limit})` (пустой `Namespace` — все
  неймспейсы), `Get(ctx, fileID)`, `Rename(ctx, fileID, name)`,
  `Delete(ctx, fileID)` (soft-delete), `Usage(ctx)` — хранимый объём проекта
  (итог + разбивка по неймспейсам).
- `PurgeNamespace(ctx, namespace, lastUsedBefore)` — bulk-очистка своего неймспейса:
  мягко удаляет файлы, не использовавшиеся с указанного момента (`nil` — все),
  возвращает число удалённых. Так интеграция сама ограничивает рост своего хранилища.
- `client.Files.WithAPIKey(projectKey)` — как и у CRM, дешёвая копия под другой
  project API key (мульти-проектный процесс).

**Входящее** (`Verifier`):

- `verifier.Verify(next)` — middleware `net/http`: проверка подписи + timestamp.
- `verifier.Handle(fn)` — хендлер `action_url`: проверка подписи + вызов `fn(ctx, r)`;
  тело читается через `integration.DecodeBody(r, &dst)`.
- `verifier.HandleInstall(fn)` — хендлер `install_url`: проверка + декод фиксированного
  `InstallRequest{ProjectID, ProjectAPIKey}` + вызов `fn`.
- `verifier.HandleUninstall(fn)` — хендлер `uninstall_url`: проверка + декод
  `UninstallRequest{ProjectID}` + вызов `fn` (удалите сохранённый project API key).
- `verifier.HandleTriggerSync(fn)` — хендлер `trigger_sync_url`: проверка + декод
  `TriggerSyncRequest{ProjectID, BlockKey, ConfigVersion}` + вызов `fn`
  (пересинхронизируйте локальные правила по версии).
- `verifier.HandleVariableValues(fn)` — typed endpoint динамических значений:
  проверка подписи, декод и валидация `VariableValuesRequest`, вызов `fn`, затем
  валидация и JSON-кодирование `VariableValuesResponse`.
- `integration.DecodeBody(r, &dst)` / `integration.VerifiedBody(r)` — доступ к проверенному телу.
- `integration.ParseVars(raw)` — разбор плейсхолдера `{{vars}}` из тела
  action-запроса (см. ниже).

`JWKSURL` в `VerifierConfig` можно не задавать — пустое значение подставит
`DefaultJWKSURL` (`https://aheron.pro/.well-known/aheron-integration-jwks.json`).
Задавайте его только для нестандартного деплоя платформы.

### Переменные в настройках блока (`{{vars}}`)

Общий пакет `variables` реализует `aheronVarsV1` для исполнения и переноса схем.
`integration.Vars` использует тот же parser и resolver. `ParseVars`, `Substitute`
и `SubstituteFunc` возвращают ошибки: потребитель должен обработать их до внешнего
действия блока. Это изменение API и смысла bare keys по сравнению с прежним SDK.

```json
{
  "projectId": "target-project",
  "project": { "course": "Go" },
  "subject": { "name": "Иван" },
  "integrations": { "payments": { "payment_url": "https://example.test/pay" } }
}
```

- `{{foo}}` и `{{subject.foo}}` читают только пользовательскую subject variable.
- `{{project.foo}}` читает project variable. `{{project.id}}` — системный ID из
  `projectId` конверта, а не значение переменной с ключом `id`.
- `{{payments.foo}}` читает definition интеграции payments. Установка/удаление
  интеграции не меняет интерпретацию: нет fallback к объекту subject или project.
- `{{subject.order.items.0.title}}` читает поле объекта/элемент массива. `order.items`
  без `subject.` означает integration slug `order`, а не subject object.
- `context.*` и `integrationState.*` зарезервированы для runtime; платформа
  передаёт их только в соответствующем контексте. Эти ссылки не являются definitions.
- Корень адресуется key либо ID, если владелец загрузил ID как alias. Parser сам
  не ищет definitions. Путь состоит из разделённых точкой непустых сегментов
  (буквы, цифры, `_`, `-`); JSON-ключи с пробелами/точками и bracket notation пока
  не поддержаны. Массивы используют индексы `0`, `1`, …, без знака/ведущих нулей.

```go
vars, err := integration.ParseVars(body.Vars)
if err != nil {
    return err // map to the integration's invalid-input error
}
text, err := vars.Substitute("Курс {{project.course}} для {{subject.name}}")
if err != nil {
    return err
}
```

Отсутствующий/null конверт — пустая область. Некорректный или плоский конверт
отклоняется. JSON-числа сохраняются через `json.Number`, включая целые больше
2^53; экспонента раскрывается без float64 и с ограничением размера результата.
Неизвестная ссылка в runtime-тексте даёт пустую строку. Незакрытый или некорректный
placeholder даёт ошибку с позицией, без частично обработанного текста.
`SubstituteFunc(template, escape)` экранирует только значения и ровно один раз.
Подставленное значение не разбирается как новый шаблон.

Для экспорта: `variables.ParseV1(text)` → `Template.Parts()` → разрешение каждой
`Reference` через каталог definitions → общий resourceRef. `Scope`, `Namespace`,
`Key` и `AccessPath` доступны отдельно от runtime-значений. `project.id` и другие
runtime-ссылки сохраняются как runtimeRef. `Template.Rewrite` восстанавливает явные
ссылки после сопоставления; это не мигратор старого синтаксиса и не importer графа.
Для собственного renderer используйте `Template.Render` с `Resolver`; по умолчанию
отсутствующая ссылка — ошибка (`MissingError`), `MissingEmpty` включается явно.
Загрузка CRM, сопоставление definitions, права, математика и HTML остаются у владельцев.

### Динамические значения переменных

Платформа вызывает endpoint интеграции в одном из двух режимов:

- поиск: `query`, `cursor` и `limit` (до 200 результатов);
- resolve: `values` (до 100 сохранённых значений), чтобы получить актуальные
  заголовки и иконки.

Поля поиска и `values` нельзя передавать вместе. `projectId` и `variableKey`
обязательны. Каждый элемент ответа должен содержать непустые `value` и `title`;
`nextCursor` допустим только для поиска.

```go
http.Handle("/variable-values", verifier.HandleVariableValues(
	func(ctx context.Context, req integration.VariableValuesRequest) (integration.VariableValuesResponse, error) {
		if req.Values != nil {
			return resolveStoredValues(ctx, req.ProjectID, req.VariableKey, req.Values)
		}
		return searchValues(ctx, req.ProjectID, req.VariableKey, req.Query, req.Cursor, req.Limit)
	},
))
```

### Uninstall

Платформа шлёт `POST` на `uninstall_url` при удалении интеграции из проекта.
Тело фиксированное — `UninstallRequest{ProjectID}`. Очистите сохранённый на install
project API key и остановите работу установки от имени проекта. Пользовательские
подключения, аккаунты и историю сохраняйте. Ошибка `fn` → 500, платформа повторит
доставку. Этот legacy-контракт не различает переустановки; новый описан ниже.

```go
http.Handle("/uninstall", verifier.HandleUninstall(
	func(ctx context.Context, req integration.UninstallRequest) error {
		return forgetProject(req.ProjectID) // очистить установочный credential
	},
))
```

### Lifecycle с защитой переустановки

Новый контракт `aheron.installation-lifecycle.v1` реализован в SDK отдельно от
legacy `HandleInstall`/`HandleUninstall`. Он ещё не включён в платформе и интеграциях.
Объявлять поддержку можно только после реализации постоянного состояния и
транзакционного изменения установочного ключа у получателя.

`LifecycleRequest` содержит protocol, eventId, integrationId, projectId,
installationId, sequence, action (`install`/`uninstall`) и необязательный
projectApiKey только для install. UUID записываются канонически в нижнем регистре.
Sequence — постоянный счётчик пары (проект, интеграция) в backend, который
**не сбрасывается при переустановке**. AccessVersion одной установки для этого
не подходит. Повтор доставки сохраняет eventId, sequence и всё тело.

Digest v1 — SHA-256 от `json.Marshal` валидного LifecycleRequest в порядке полей
объявленной Go-структуры, без пробелов, с JSON escaping Go (включая HTML escaping)
и пропуском пустого projectApiKey. Обе стороны вызывают метод Digest; независимый
golden fixture фиксирует этот wire-инвариант. В JSON-парсере повторные поля,
неверный регистр имён, null и неизвестные поля отклоняются.

`verifier.HandleLifecycle(integrationID, svc.ApplyLifecycle)` возвращает
`(http.Handler, error)` для отдельного POST URL. Он проверяет подпись по сырым
байтам, адресата, строгую структуру JSON и результат обработчика; ответы имеют
no-store. Не регистрируйте его по legacy install/uninstall URL. Старый получатель
может проигнорировать новые поля и успеть изменить данные даже при непригодном
ответе, поэтому fallback к старому endpoint запрещён.

Подпись lifecycle — Ed25519 от байтов
`aheron.installation-lifecycle.v1.<timestamp>.<rawBody>`. Префикс стоит **перед**
timestamp и задаётся обработчиком, не выбирается из тела. JWKS и X-Aheron-*
заголовки сохраняются, но обычная подпись `<timestamp>.<body>` не подходит.
Это не даёт подписанному запросу блока с произвольным JSON стать lifecycle-командой.
Обратная подмена также отклоняется: lifecycle-подпись не принимается legacy endpoint.

Сервис/репозиторий получателя выполняет `DecideLifecycle(current, request)`
**внутри serializable-транзакции**:

- `applied`: атомарно сохраняет State и устанавливает/очищает ключ; install без
  projectApiKey тоже очищает прежний ключ. Uninstall сохраняет запись без ключа.
- `duplicate`: тот же номер и digest, без повторных эффектов.
- `superseded`: уже применён больший номер, без изменения новой установки.
- Тот же номер с другим телом — `ErrLifecycleConflict`; повреждённое сохранённое
  состояние — `ErrLifecycleState`, его нельзя считать пустым.

State хранится постоянно, включая запись об удалении: иначе поздний install
оживит отозванный ключ. Не удаляйте вместе с uninstall пользовательские аккаунты,
платежи, подключения и историю проекта. Все legacy/manual writers ключа должны
отказывать для проекта, переведённого на этот контракт. Побочные эффекты вне БД
требуют отдельной надёжной обработки; возвращать receipt до завершения требуемой
очистки нельзя. Сам `DecideLifecycle` не сохраняет данные и не выдаёт прав.

Платформа использует `NewLifecycleSender` с собственным Ed25519-ключом и `Deliver`.
Одна попытка отправляет подписанное неизменное тело и принимает только HTTP 200
с точной `LifecycleReceipt`: совпадают все IDs, sequence, digest, outcome и
observedSequence. HTTP 202, пустой 200, другая квитанция и редиректы — ошибки.
HTTPS обязателен по умолчанию; AllowHTTP включается явно для локального контура.
URL берётся из принятого и проверенного контракта установки, не из текущего
изменяемого каталога. Отправитель не выбирает адрес, не создаёт очередь и не
повторяет запрос сам: это ответственность постоянного задания backend.

Обработчик возвращает 400 для неверного сообщения, 401 для неверной подписи,
403 для другого адресата, 405 для другого метода, 409 для конфликта и 503 при
ошибке обработки/неверной квитанции. Ошибки доставки не содержат тела, ключей,
произвольного текста ответа или URL. Токены нельзя писать в аудит/квитанции;
в них хранится только digest. `task test:lifecycle` проверяет все перестановки
install/uninstall, конкурирующие повторы, защиту переустановки и HTTP-контракт.

### Trigger sync

После изменения конфигурации триггер-блоков проекта платформа шлёт `POST` на
`trigger_sync_url`. Это **пинг**, а не сами данные: тело —
`TriggerSyncRequest{ProjectID, BlockKey, ConfigVersion}`. `ConfigVersion` —
счётчик для пары (проект, интеграция), инкрементируемый транзакционно вместе с
изменением. Сравните его с локально сохранённой версией: если пришедшая новее —
подтяните актуальный список через `Triggers.ListTriggers` и атомарно замените
снапшот правил, защитив его той же версией. Так дубли и доставки не по порядку
не откатят конфигурацию назад. `ListTriggers` тоже возвращает `configVersion`,
поэтому TTL-ресинки по таймеру используют ровно тот же guard.

```go
http.Handle("/triggers/sync", verifier.HandleTriggerSync(
	func(ctx context.Context, req integration.TriggerSyncRequest) error {
		if req.ConfigVersion <= localVersion(req.ProjectID, req.BlockKey) {
			return nil // устаревший или повторный пинг — игнорируем
		}
		listing, err := client.Triggers.ListTriggers(ctx, req.ProjectID, req.BlockKey)
		if err != nil {
			return err // 500 → платформа повторит
		}
		// Атомарно заменить снапшот, только если версия действительно новее.
		applyRules(req.ProjectID, req.BlockKey, listing.ConfigVersion, listing.Triggers)
		return nil
	},
))
```

### Console view-token

Iframe консоли (`integrations.console_url`) открывается внутри проекта и не
получает auth-токены платформы. Вместо этого платформа передаёт ему короткоживущий
подписанный view-token (через `postMessage`), а iframe шлёт его на бэкенд
интеграции. `ConsoleVerifier` проверяет EdDSA-подпись токена по тому же JWKS
платформы (переиспользует общий загрузчик ключей) и claims (`iss`/`aud`/`purpose`/
`exp`/`nbf`), после чего можно доверять `ProjectID` и `Permissions`. Платформа
выдаёт `console.read` любому участнику проекта, а `console.write` — только
владельцу и администраторам. Перед console-мутацией проверяйте
`claims.HasPermission(integration.ConsolePermissionWrite)`: сохранённый project
API key интеграции намеренно шире прав человека, открывшего iframe. Любая ошибка оборачивает
`ErrConsoleTokenInvalid` — отвечайте `401`, не раскрывая причину.

```go
consoleV, _ := integration.NewConsoleVerifier(integration.ConsoleVerifierConfig{
	IntegrationID: os.Getenv("INTEGRATION_ID"), // обязателен; JWKSURL пуст → DefaultJWKSURL
})

http.HandleFunc("/console/data", func(w http.ResponseWriter, r *http.Request) {
	claims, err := consoleV.Verify(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !claims.HasPermission(integration.ConsolePermissionWrite) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	// claims.ProjectID теперь доверенный — отдать данные консоли по проекту.
	_ = claims
})
```

## Манифест каталога

Интеграция объявляет свои блоки и URL-контракт версии **в коде**, а не руками в
UI платформы, и сама публикует декларацию через `client.Catalog.Sync`. Так
источник правды один: переименованный выход блока или новый блок доезжают до
каталога тем же деплоем, что и код, который их обслуживает.

```go
func Manifest() integration.Manifest {
	return integration.Manifest{
		ConsolePath:        "/console",
		InstallPath:        "/install",
		UninstallPath:      "/uninstall",
		ActionPath:         "/api/actions",
		TriggerSyncPath:    "/trigger-sync",
		VariableValuesPath: "/variable-values",
		VariableValueSources: map[string]integration.VariableValueSource{
			"spreadsheetId": integration.RemoteVariableValues(),
		},
		Blocks: []integration.Block{
			{
				Key:     "create-row",
				Kind:    integration.KindAction,
				Name:    "Создать строку",
				Outputs: []string{"created", "failed"},
			},
		},
	}
}
```

Эндпоинты задаются **путями, а не URL**: адрес деплоя приезжает из
`Config.PublicBaseURL`, поэтому один и тот же манифест работает в dev и в проде.
Абсолютный `http(s)`-путь тоже принимается — для вебхука, живущего на другом
хосте. `IframePath` блока пустой означает `/blocks/<Key>` — соглашение, которому
следуют все интеграции, так что обычно его не задают.

`Manifest.BlockKeys()` отдаёт объявленные ключи в порядке декларации: по нему
удобно регистрировать iframe-страницы, чтобы роутер и каталог строились из одного
списка и не могли разъехаться.

### Страницы консоли в боковой панели проекта

`ConsolePages` объявляет разделы консоли, которые платформа показывает в боковой
панели проекта. Это сокращает ежедневный путь: без них до раздела вроде диалогов
надо зайти в проект, в список интеграций, в интеграцию, в консоль и только там
переключить вкладку.

```go
ConsolePages: []integration.ConsolePage{
	{
		Key:   "dialogs",
		Label: "Диалоги",
		Path:  "/console/dialogs",
		Icon:  mustIcon("assets/icons/dialogs.svg"),
	},
},
```

- `Key` — идентичность пункта: смена ключа заменяет один пункт другим.
- `Path` — страница, которую интеграция уже отдаёт. Открывается она так же, как
  консоль: внутри платформы, в iframe, под коротким console view-token'ом.
- `Icon` необязательна — без неё платформа рисует пункт со своим fallback, так что
  страницу можно объявить сейчас, а картинку добавить позже.

Иконка едет **байтами внутри манифеста**, а не ссылкой на хост интеграции:
платформа кладёт её в свой бакет и оттуда отдаёт браузеру. Боковая панель проекта
не должна ломаться, когда интеграция лежит, а ходить по URL, которым управляет
интеграция, платформа не станет. Читать файл удобно из того же `go:embed`, в
котором лежат web-ассеты:

```go
//go:embed all:assets/dist
var distFS embed.FS

icon, err := integration.IconFromFS(distFS, "assets/dist/icons/dialogs.svg")
```

Ограничения: до 8 страниц, иконка до 32 КиБ, тип `image/svg+xml`, `image/png` или
`image/webp`. Манифест целиком, вместе с иконками, едет одним подписанным запросом,
и платформа ограничивает его тело 1 МиБ.

Иконка перезаливается только когда меняется её содержимое: платформа хранит хеш и
на совпадении не трогает ни бакет, ни запись. Поэтому `Sync` на каждом старте
реплики остаётся дешёвым.

Что происходит на стороне платформы:

- Манифест — **полное желаемое состояние**. Блок, которого в нём нет, из каталога
  удаляется; эндпоинт, который не объявлен, обнуляется. Так же и со страницами
  консоли: пропавшая из декларации страница уходит из боковой панели (согласия,
  как на удаление блока, тут не требуется — на пункт меню не ссылается ни одна
  схема).
- Вызов **идемпотентен**: если манифест совпадает с опубликованной версией,
  платформа не пишет ничего и возвращает `Changed=false`. Поэтому звать `Sync` на
  каждом старте каждой реплики безопасно — реплики, гоняющиеся за публикацией
  одного и того же манифеста, сходятся к одной новой версии.
- Удаление блока требует явного согласия: ключ, который был в опубликованной
  версии и исчез из манифеста, должен быть перечислен в `Retired`, иначе
  платформа откажет. Пропавший блок ломает схемы, которые его уже используют, а
  случайное удаление в коде на проводе выглядит точно так же, как намеренное.
- `Published=false` с непустым `Reason` — платформа подготовила черновик, но
  опубликовать не смогла. Сегодня это только subflow-блок, под-схему которого ещё
  не собрал человек в UI. Это не ошибка: логируйте предупреждение и работайте
  дальше.

Вне манифеста остаются `slug`, имя и описание в каталоге, публичный ключ и само
создание интеграции — это делает человек через UI платформы. `Sync` только
обновляет уже существующую запись.

### Когда отправлять

На старте сервиса, отдельной горутиной:

```go
if cfg.CatalogSyncEnabled {
	go client.Catalog.StartSync(ctx, catalog.Manifest())
}
```

`StartSync` ждёт небольшой джиттер (он разводит реплики, которые в rolling update
стартуют почти одновременно), затем несколько раз пробует `Sync` с backoff и
пишет исход в лог. Ошибку наружу не отдаёт **намеренно**: sync не должен
блокировать старт и readiness — каталог может быть недоступен, а обрабатывать
входящие действия сервис обязан. Худший случай — новые декларации доедут на
следующем старте.

## Транзакционный outbox (`outbox`)

Пакет `aheron-go-sdk/outbox` публикует в брокер строки, записанные в одной
транзакции с изменением, которое они описывают. Он **не зависит от базы**: всё
его касание хранилища — интерфейс из двух методов, поэтому под ним одинаково
работают PostgreSQL и YDB. Тяжёлых зависимостей у пакета нет вовсе.

```go
type Store interface {
    ClaimBuckets(ctx context.Context, owner string, lease time.Duration) ([]int, error)
    PublishBucket(ctx context.Context, bucket int, owner string, limit int,
        publish func(context.Context, Event) error) (published int, err error)
}
```

Очередь шардирована на бакеты, и инстанс публикует только те, на которые держит
аренду. Инстансы делят очередь между собой вместо гонки за отдельные строки —
именно это заменяет `SELECT ... FOR UPDATE SKIP LOCKED` там, где его нет. Один
инстанс просто забирает все бакеты. Бакет, `partition_key` и партиция брокера
связаны одним хешем, поэтому порядок событий одного ключа держится сквозным.

```go
relay := outbox.NewRelayWithOptions(store, outbox.PublisherFunc(
    func(ctx context.Context, ev outbox.Event) error {
        err := writer.WriteMessages(ctx, kafka.Message{
            Topic: topic,
            Key:   []byte(ev.PartitionKey),
            Value: ev.Payload,
        })
        switch {
        case err == nil:
            return nil
        case isSharedBrokerFailure(err):
            // Network, broker/auth outage, 429 and similar dependency-wide
            // failures do not consume this event's poison-message budget.
            return outbox.Transient(err, retryAfter(err))
        case isEventSpecificRejection(err):
            // Retrying the same payload cannot help; unblock the bucket by
            // moving precisely this row to dead letters.
            return outbox.Permanent(err)
        default:
            // Untyped failures retain the Store's bounded legacy budget.
            return err
        }
    }),
    outbox.Config{Logger: zaplog.New(log)},
    outbox.WithObserver(metrics),
)
go relay.Run(ctx)
```

После нетерминальной ошибки relay включает для конкретного бакета exponential
full-jitter backoff (по умолчанию от 500 ms до 30 s). Положительный
`RetryAfter` у `outbox.Transient` имеет приоритет. Остальные бакеты продолжают
работать. Это одновременно не даёт недоступному брокеру получать запрос на
каждом 200-ms poll и не превращает общую аварию в сотни poison-событий.

`outbox.Observer` — независимый от Prometheus/OpenTelemetry metrics seam. Он
отдаёт результаты publish без event ID/payload/partition key и bucket backoff;
из него строятся counters по `ErrorClass`, latency histogram и backoff gauges.
Observer вызывается одним bounded async worker: медленный callback не блокирует
publish, переполненная очередь дропает observations, panic изолируется. Счётчики
дропов и panic доступны через `Relay.DroppedObservations()` и
`Relay.ObserverPanics()`. Размер очереди меняется через
`outbox.WithObserverQueue`; backoff — через `outbox.WithRetryBackoff`. Новые
настройки намеренно не добавлены полями в `outbox.Config`, чтобы minor-релиз не
сломал существующие unkeyed literals.

Готовый `Store` на YDB — в подмодуле [`ydb`](ydb/README.md) вместе с DDL схемы.
На PostgreSQL два метода пишутся своим SQL: `ClaimBuckets` — условный `UPDATE`
по таблице аренд, `PublishBucket` — чтение головы бакета с удалением строки
после успешной публикации.

Operator-функции вынесены в отдельный optional-интерфейс
`outbox.DeadLetterStore`: keyset-list, точечный get, идемпотентный targeted
replay и snapshot `pending/dead count + oldest timestamp`. Поэтому добавление
этих возможностей не ломает существующие реализации горячего `Store`.

`outbox.BucketOf` обязан оставаться неизменным: бакет входит в первичный ключ, и
смена хеша оставит уже записанные строки в бакетах, которые никто не опрашивает.
На это есть тест с эталонными значениями.

## Логирование

SDK не тянет конкретный логгер: передайте свою реализацию `integration.Logger`
или используйте готовый zap-адаптер `github.com/Alexey-zaliznuak/aheron-go-sdk/integration/zaplog`. По
умолчанию — молчание (no-op). `outbox` пишет в тот же интерфейс
(`outbox.Logger` — псевдоним того же типа).

## Замечания по деплою

- `Resolve`, `Reactivate`, `Activate` и `List` используют `ExecutionURL` — базу с префиксом
  шлюза `/api/execution` (эндпоинты под `{ExecutionURL}/integrations/...`). Если
  ваш деплой отдаёт execution-service по другому адресу — задайте `ExecutionURL`
  соответственно.
- CRM ходит через префикс шлюза `/api/crm`. Если ваш деплой отдаёт CRM по
  другому адресу — задайте `CRMURL` соответственно.
- Файлы (`client.Files`) ходят через префикс шлюза `/api/media`. Если media-service
  отдаётся по другому адресу — задайте `MediaURL` соответственно.

### Default favorite blocks

Set `FavoriteByDefault: true` on an `integration.Block` to append it to members'
personal project favorites when the integration is installed:

```go
integration.Block{Key: "send-message", Kind: integration.KindAction,
    Name: "Send message", FavoriteByDefault: true}
```

The default is false. The platform snapshots flagged keys on installation and seeds
each member once (including future members). User removals/reordering are preserved.
Manifest updates do not change defaults of existing installations; reinstalling takes
a fresh snapshot. The wire property is `favoriteByDefault`.

## Short links and link callbacks
Client.Links provides Create, Get, List, Disable, RegisterCallback, Deliveries and Replay. Config.LinksURL defaults to https://link.aheron.pro/api. IDs always have 16 base62 characters. Create requires a stable Idempotency-Key per outgoing message/action; it is retained for automatic retries. Signed requests bind method, path/query, body and the idempotency key. Gateways must preserve the path.
RegisterCallback declares an HTTPS URL and enabled state for an endpointKey. CreateLinkRequest.Callback optionally carries that key and arbitrary JSON object Data. LinkVerifier (NewLinkVerifier) verifies the separate link-service JWKS and signed event metadata. Its Handle callback receives LinkEvent with ID, Data, EventID and OccurredAt. Atomically deduplicate EventID with the business effect for at least 31 days from OccurredAt. GET events can be generated by previews/bots and do not authenticate a human. Return nil only after durable acceptance; an error requests retry. This SDK change must be released before integrations can consume it.


## Scheme transfer contracts

### Target resource provisioning

`schemetransfer.ProvisionRequest` / `ProvisionResult` define the internal CRM
contract for one native subject definition, project variable or tag. The request
selects `reuse` (ID + expected key/type) or `create` (key/type/name/description,
and an explicitly supplied value for a new project variable). Subject values,
defaults and integration-owned definitions are excluded. Compatible exact-key
resources appearing concurrently may be reused; existing values are never updated.

`transfercrm.New(baseURL, internalToken, httpClient)` creates the platform client.
`ProvisionResource(ctx, projectID, importID, resourceRef, request)` sends a PUT to
`/internal/projects/{projectId}/scheme-imports/{importId}/resources/{resourceRef}`.
The same address and request digest return the stored receipt. Different payloads
conflict. Persist these addresses in the importing service before sending requests.
The receipt contains only ID, kind, key, type and whether this import created it.

The client validates bounded requests and responses, preserves JSON number tokens,
does not follow redirects, and never propagates response bodies or credentials in
errors. Pass a bounded context and a client with timeouts. This client uses the
platform's internal credential; it must not be exposed to integration/browser code.

### Integration preparation and portable bindings

Each transferable integration block declares
`copyRules: {"version":2,"mode":"callback"}`. The integration-level manifest
provides `ResourceSources`, `ResourceValuesPath`, `PrepareCopyPath` and
`ValidateCopySettingsPath`. Paths resolve against `PublicBaseURL` into the pinned
catalog version. A block without copy rules does not support transfer.

`sourceKey` identifies a shared resource source inside the integration; no
`resourceType` is needed. `VariableValueSource` can connect a filter selector to
that source with `SourceKey` and `ValueSemantics: "resource"` together.
`ValidateDeclaration` checks source dependencies, referenced keys, callback
endpoints and constraints schemas.

`prepareCopy` validates the concrete source configuration, makes defaults
explicit and uses `schemetransfer.NewCopyBuilder` to mark resource references,
actual template expressions and fixed output definitions. It returns
`protocolVersion: 2`, prepared `settings`, a private `plan` and `issues`.
Unmarked prepared values are literal. The platform groups dependencies across
the scheme and creates an immutable template with local resources and bindings.
The private plan and original resource values are not published.

See [dynamic copy preparation](docs/copy-callback-v2.md) for the builder contract
and [signed handlers and lookup](docs/copy-callbacks.md) for HTTP wiring.
Both preparation and final `validateCopySettings` are mandatory and read-only.
The integration owns settings semantics, including provider-dependent options;
SDK validation does not grant project access or prove correct field marking.

The `schemetransfer` package embeds JSON Schema in `Schema()`. `Validate` accepts
definition names such as `copyRules`, `resourceSources`, `lookupRequest`,
`validationResult`, `prepareCopyRequest`, `prepareCopyResponse`,
`validateCopySettingsRequest` and `importPlan`. It rejects unknown/duplicate
fields, unsupported versions and oversized/deep documents. Errors contain a
code/path, not values. Schema compilation never loads external URLs or files.
Callback request/response validation also verifies the negotiated version.

An import mapping may use `{"action":"defer"}` for an integration resource,
without `id` or `definition`. The platform leaves its fields empty and stores an
inactive graph with per-step setup requirements. Target-dependent validation can
wait until editing; installed integration contracts and structural checks remain
mandatory. Completing setup validates the current block, allowing optional
attachments to be removed. Activation requires all required setup and validation.
Native definitions and fixed integration-owned outputs cannot be deferred.

Text and list membership are edited after copying in the normal block editor.
`ResourceList(path, "files")` remaps library references; it does not copy bytes.
`File(path, sourceKey, mediaFileID)` adds a private hosted-file marker to a
resource occurrence. A file source declares `FileImport.Namespace`, and the
manifest supplies `ImportCopyFilePath` for signed, idempotent target-library
registration through `HandleImportCopyFile`. The platform-only `transfermedia`
client captures and imports media-service snapshots. See the [file-transfer
contract and rollout](docs/copy-files.md). These SDK APIs require orchestration
in backend and persistent receipts in the integration before user-facing
automatic copying can be enabled.

`transfermedia.Client.RetireSnapshot` installs a permanent snapshot fence and
accepts asynchronous cleanup. Revoke access to the owning revision first and
retain the snapshot ID for retries. Completed target files are unaffected.
Requires media-service's snapshot retirement endpoint and migration 00013.

Private import plans require authorization, revision/digest, expiration and
lifecycle checks in their owning service; schema validation alone grants none.

### Integration callback protocol and native rules

SDK v0.32.0 accepts only integration copy protocol 2. `CopyRules` contains only
`version` and `mode`; prepare responses always contain `protocolVersion` and
`plan`. Protocol 1 requests and static integration declarations are rejected.
Platform-owned native descriptors use the distinct `NativeCopyRules` type and
`ParseNativeCopyRules`; their version is independent of integration callbacks.

For networks without working IPv6 TLS, SDK publication tasks accept
`GIT_NETWORK_FLAGS=--ipv4`, for example `task release:minor GIT_NETWORK_FLAGS=--ipv4`.
Certificate verification remains enabled.

### Lifecycle catalog declaration

`Manifest.InstallationLifecyclePath` resolves to `installationLifecycleUrl` in
catalog self-sync. Its presence declares `aheron.installation-lifecycle.v1` with
durable ordering and credential fencing. It must use a dedicated endpoint,
distinct from `InstallPath` and `UninstallPath`; omission preserves the legacy
manifest wire shape. Deploy the catalog field and receiver migrations before
advertising this capability. The backend pins the HTTPS destination in the
accepted permission revision; a declaration does not migrate or authorize any
existing installation by itself.
