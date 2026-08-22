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

Ретраятся только идемпотентные вызовы (GET, а также resolve/активация — платформа
их дедуплицирует по `executionContextId`+версии) на сетевых ошибках и 502/503/504.

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

`{{vars}}` — **не плоская карта**. Платформа присылает три пространства имён:

```json
{
  "project":      { "<ключ>": "значение" },
  "subject":      { "<ключ>": "значение" },
  "integrations": { "<slug>": { "<ключ>": "значение" } }
}
```

Поэтому интеграция, читающая тело как `ключ → значение`, не найдёт ничего по
голому ключу и молча подставит пустые строки туда, где автор ждал переменную.
Не разбирайте эту структуру руками — декодируйте её через `integration.Vars`:

```go
var body struct {
	integration.ExecutionContext                 // {{context}}
	ActionKey                    string          `json:"actionKey"`
	Settings                     json.RawMessage `json:"blockSettings"`
	Vars                         json.RawMessage `json:"vars"`
}
if err := integration.DecodeBody(r, &body); err != nil {
	return err
}

vars := integration.ParseVars(body.Vars)
text := vars.Substitute("Заказ {{orderId}} от {{name}}")
```

- `vars.Substitute(template)` — подставляет каждый `{{ключ}}`; неизвестный ключ
  превращается в пустую строку, а не утекает плейсхолдером в результат.
- `vars.SubstituteFunc(template, escape)` — то же с экранированием подставляемых
  значений: для шаблонов со своим синтаксисом (например HTML-текст сообщения),
  где значение не должно протащить разметку.
- `vars.Lookup(key)` — одно значение как `any` плюс признак «нашлось».
- `integration.VarString(value)` — строковая форма значения: число без хвостовых
  нулей (в JSON нет целых, `1024` приходит как float), объект — компактным JSON.

Голый ключ — это сначала переменная проекта, потом субъекта (так работают ключи,
которые предлагают редакторы блоков). Ключ с точкой выбирает пространство имён:
`project.<ключ>`, `subject.<ключ>`, а любой другой префикс — slug интеграции в
каталоге, то есть способ прочитать значения **другой** интеграции. Если префикс
не назвал пространство имён, путь применяется к самому значению, так что
переменная-объект доступна как `{{payment.amount}}`.

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
Тело фиксированное — `UninstallRequest{ProjectID}`. Сбросьте всё состояние по
проекту, в первую очередь сохранённый на install project API key, чтобы больше
не действовать от имени проекта. Ошибка `fn` → 500, платформа повторит доставку.

```go
http.Handle("/uninstall", verifier.HandleUninstall(
	func(ctx context.Context, req integration.UninstallRequest) error {
		return forgetProject(req.ProjectID) // удалить project API key и локальные данные
	},
))
```

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
`exp`/`nbf`), после чего можно доверять `ProjectID`. Любая ошибка оборачивает
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

Что происходит на стороне платформы:

- Манифест — **полное желаемое состояние**. Блок, которого в нём нет, из каталога
  удаляется; эндпоинт, который не объявлен, обнуляется.
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
relay := outbox.NewRelay(store, outbox.PublisherFunc(
    func(ctx context.Context, ev outbox.Event) error {
        return writer.WriteMessages(ctx, kafka.Message{
            Topic: topic,
            Key:   []byte(ev.PartitionKey),
            Value: ev.Payload,
        })
    }),
    outbox.Config{Logger: zaplog.New(log)},
)
go relay.Run(ctx)
```

Готовый `Store` на YDB — в подмодуле [`ydb`](ydb/README.md) вместе с DDL схемы.
На PostgreSQL два метода пишутся своим SQL: `ClaimBuckets` — условный `UPDATE`
по таблице аренд, `PublishBucket` — чтение головы бакета с удалением строки
после успешной публикации.

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
