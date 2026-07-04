# Aheron Go SDK (интеграции)

SDK для построения бэкендов интеграций Aheron на Go. Закрывает **обе** стороны
модели доверия платформы:

- **Входящее** (платформа → интеграция): `Verifier` проверяет Ed25519-подпись
  платформы (по JWKS, выбор ключа по `kid`) и свежесть timestamp, а `Mux` /
  `HandleAction` отдают в хендлер уже типизированный payload.
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

	// Один HTTP-роут на тип блока. Хендлер получает проверенный типизированный
	// запрос и резолвит шаг через выбранный выход.
	http.Handle("/blocks/send_message", verifier.HandleAction(
		func(ctx context.Context, req integration.ActionRequest) error {
			// req.Payload.Subject.ID, req.Payload.Project.ID, req.Payload.Vars ...
			return client.Steps.Resolve(ctx, req.Resolve("ok", map[string]any{
				"lastMessageId": "42", // subject-переменная по ключу
			}))
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
| `ExecutionURL`                                             | origin платформы; эндпоинты интеграций под `/api/integrations/...`   | `https://aheron.pro`         |
| `CRMURL`                                                   | база CRM с префиксом шлюза `/api/crm`; вызовы бьют в `/projects/...` | `https://aheron.pro/api/crm` |
| `Timeout` / `RetryCount` / `RetryWaitMin` / `RetryWaitMax` | транспорт                                                            | 30s / 2 / 0.5s / 5s          |
| `Logger`                                                   | реализация `integration.Logger`                                      | no-op                        |

Ретраятся только идемпотентные вызовы (GET, а также resolve/активация — платформа
их дедуплицирует по `executionContextId`+версии) на сетевых ошибках и 502/503/504.

## Возможности

**Исходящее** (`Client`, подписано ключом интеграции):

- `client.Steps.Resolve(ctx, params)` — резолв припаркованного `integrationAction`.
  Обычно строится через `req.Resolve(output, vars)` из входящего запроса, тогда
  вызов уходит на абсолютный `resolve.url`, который передала платформа.
- `client.Triggers.Activate(ctx, params)` — активация триггера по внутреннему
  `SubjectID` **или** по внешней идентичности (`IntegrationSubjectID` [+ `Type`]).
- `client.Triggers.List(ctx, projectID, blockKey)` — список инстансов триггера.

**Данные** (`client.CRM`, по project API key):

- `UpsertSubject`, `GetSubject`, `ListSubjectVariables`, `SetSubjectVariables`.

**Входящее** (`Verifier`):

- `verifier.Verify(next)` — middleware `net/http`: проверка подписи + timestamp.
- `verifier.HandleAction(fn)` — готовый хендлер: проверка + декод `block.action` + вызов `fn`.
- `integration.NewMux().OnAction(fn)` — диспетчер по типу конверта для одного эндпоинта.

## Логирование

SDK не тянет конкретный логгер: передайте свою реализацию `integration.Logger`
или используйте готовый zap-адаптер `github.com/Alexey-zaliznuak/aheron-go-sdk/integration/zaplog`. По
умолчанию — молчание (no-op).

## Замечания по деплою

- `Resolve` предпочитает абсолютный `resolve.url` из входящего payload, поэтому
  не зависит от конфигурации базовых URL. `Activate`/`List` используют
  `ExecutionURL`.
- CRM ходит через префикс шлюза `/api/crm`. Если ваш деплой отдаёт CRM по
  другому адресу — задайте `CRMURL` соответственно.
