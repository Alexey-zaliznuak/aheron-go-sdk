# Aheron Go SDK (интеграции)

SDK для построения бэкендов интеграций Aheron на Go. Закрывает **обе** стороны
модели доверия платформы:

- **Входящее** (платформа → интеграция): `Verifier` проверяет Ed25519-подпись
  платформы (по JWKS, выбор ключа по `kid`) и свежесть timestamp. `Handle`
  оборачивает эндпоинт `action_url` (тело проектирует автор — читается через
  `DecodeBody` в свою структуру), а `HandleInstall` — эндпоинт `install_url`
  (фиксированное тело `{projectId, projectApiKey}`).
- **Исходящее** (интеграция → платформа): `Client` дёргает подписанные эндпоинты
  платформы (resolve шага, активация/список триггеров) — каждый вызов
  подписывается приватным ключом интеграции — и содержит `CRM`-клиент для
  чтения/записи данных субъекта по project API key.

Модуль: `github.com/Alexey-zaliznuak/aheron-go-sdk`. Требует Go 1.25+.

## Установка

```bash
go get github.com/Alexey-zaliznuak/aheron-go-sdk/integration
```

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
- `integration.DecodeBody(r, &dst)` / `integration.VerifiedBody(r)` — доступ к проверенному телу.

## Логирование

SDK не тянет конкретный логгер: передайте свою реализацию `integration.Logger`
или используйте готовый zap-адаптер `github.com/Alexey-zaliznuak/aheron-go-sdk/integration/zaplog`. По
умолчанию — молчание (no-op).

## Замечания по деплою

- `Resolve`, `Reactivate`, `Activate` и `List` используют `ExecutionURL` — базу с префиксом
  шлюза `/api/execution` (эндпоинты под `{ExecutionURL}/integrations/...`). Если
  ваш деплой отдаёт execution-service по другому адресу — задайте `ExecutionURL`
  соответственно.
- CRM ходит через префикс шлюза `/api/crm`. Если ваш деплой отдаёт CRM по
  другому адресу — задайте `CRMURL` соответственно.
- Файлы (`client.Files`) ходят через префикс шлюза `/api/media`. Если media-service
  отдаётся по другому адресу — задайте `MediaURL` соответственно.
