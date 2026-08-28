# `aheron-go-sdk/ydb`

YDB-половина SDK: подключение к управляемому кластеру и готовая реализация
`outbox.Store` на стандартной схеме outbox.

Это **отдельный Go-модуль**. Большинству интеграций YDB не нужен — они живут на
PostgreSQL, — и вынесение в подмодуль означает, что `ydb-go-sdk` вместе со всем
деревом gRPC не попадает в их `go.sum` вовсе. Цикл relay при этом лежит в
корневом модуле (`aheron-go-sdk/outbox`) и не зависит ни от какой БД, так что на
PostgreSQL он тоже работает.

```bash
go get github.com/Alexey-zaliznuak/aheron-go-sdk/ydb
```

## Подключение

```go
db, err := ydbx.Open(ctx, cfg.YDBDSN, cfg.YDBServiceAccountKeyFile)
if err != nil {
    return err
}
defer db.Close(ctx)
```

`serviceAccountKeyFile` — авторизованный ключ сервисного аккаунта, под которым
работает сервис. Пустой путь означает анонимное подключение — именно этого ждёт
локальная база в Docker. Авторизация по ключу специфична для Yandex Cloud и
живёт вне ядра драйвера, в `ydb-go-yc`; вместе с ней идёт `WithInternalCA`, и
он не опционален против управляемого кластера — его сертификат подписан
собственным CA облака, без которого TLS-хендшейк не проходит.

Пакет называется `ydb`, как и пакет ядра драйвера, поэтому на месте импорта
один из двух придётся назвать явно:

```go
import (
    ydbx "github.com/Alexey-zaliznuak/aheron-go-sdk/ydb"
    "github.com/ydb-platform/ydb-go-sdk/v3"
)
```

## Outbox

### Схема

Стор ожидает три таблицы. Имя основной задаётся конфигурацией
(`platform_outbox` по умолчанию), две другие получаются из него добавлением
`_leases` и `_dead`. В миграциях имена пишутся без префикса пути — директория
приходит из строки подключения или из `TablePathPrefix`.

```sql
CREATE TABLE platform_outbox (
    bucket        Uint8 NOT NULL,
    created_at    Timestamp NOT NULL,
    id            Uuid NOT NULL,
    partition_key Utf8 NOT NULL,
    topic         Utf8,
    payload       Json NOT NULL,
    attempts      Int32 NOT NULL,
    last_error    Utf8,
    PRIMARY KEY (bucket, created_at, id)
);

CREATE TABLE platform_outbox_leases (
    bucket       Uint8 NOT NULL,
    locked_by    Utf8 NOT NULL,
    locked_until Timestamp NOT NULL,
    PRIMARY KEY (bucket)
);

CREATE TABLE platform_outbox_dead (
    bucket        Uint8 NOT NULL,
    created_at    Timestamp NOT NULL,
    id            Uuid NOT NULL,
    partition_key Utf8 NOT NULL,
    topic         Utf8,
    payload       Json NOT NULL,
    attempts      Int32 NOT NULL,
    last_error    Utf8,
    failed_at     Timestamp NOT NULL,
    PRIMARY KEY (bucket, created_at, id)
) WITH (TTL = Interval("P30D") ON failed_at);
```

Каждый `CREATE TABLE` — отдельный файл миграции goose: DDL в YDB не
транзакционен, и многооператорная миграция может застрять применённой наполовину.

Почему ключ такой:

- **`bucket` первой колонкой.** `BIGINT GENERATED ALWAYS AS IDENTITY` из
  Postgres дал бы монотонный ключ, а это значит, что весь поток записи бьёт в
  последнюю партицию. Документация YDB называет это типичной ошибкой и
  рекомендует ровно этот приём — хеш в префиксе ключа, поля сортировки после
  него.
- **`created_at, id` после него.** Внутри бакета строки лежат в том порядке, в
  котором писались, поэтому чтение головы диапазона и есть чтение очереди.
- **Число бакетов менять нельзя.** Оно входит в первичный ключ: изменение
  оставит уже записанные строки в бакетах, которые никто не опрашивает.
  Шестнадцать — значение по умолчанию у hash-sharded index в CockroachDB,
  выбранное по тем же соображениям.

Порядок событий это не ломает: хеш детерминирован, все события одного
`partition_key` попадают в один бакет, а Kafka и так гарантирует порядок только
внутри партиции, которую определяет тот же ключ.

**Опубликованная строка удаляется, а не помечается.** В Postgres растущий хвост
`status='published'` прятался за partial index; в YDB partial index нет, и хвост
лежал бы прямо в том диапазоне, который relay опрашивает несколько раз в
секунду. По той же причине строка, исчерпавшая попытки, переезжает в
`_dead`, а не остаётся на месте: иначе она блокировала бы свой бакет навсегда.

Семантика publish-ошибок:

- `outbox.Transient(err, retryAfter)` обновляет `last_error`, но **не**
  увеличивает `attempts`. Так маркируются network/broker/auth outage, 429 и
  другие общие отказы зависимости;
- `outbox.Permanent(err)` сразу атомарно переносит конкретную event в `_dead`;
  после успешного move Store возвращает `DeadLetteredPublishError`, потому что
  только Store может подтвердить отсутствие pending row;
- немаркированная ошибка сохраняет совместимость со старыми publisher-ами:
  увеличивает `attempts` и попадает в `_dead` после `MaxAttempts`;
- исчерпавшая budget ошибка возвращается как `DeadLetteredPublishError`, чтобы
  relay не включал backoff для уже удалённой из pending строки.

Это изменение использует существующие `attempts`, `last_error`, `failed_at` и
не требует миграции схемы.

### Запись

Событие пишется **в транзакции того изменения, которое оно описывает** — в этом
весь смысл паттерна:

```go
store := ydbx.NewOutboxStore(db, ydbx.OutboxConfig{
    TablePathPrefix: cfg.YDBTablePathPrefix,
})

err := db.Query().DoTx(ctx, func(ctx context.Context, tx query.TxActor) error {
    if err := writeTheChange(ctx, tx); err != nil {
        return err
    }
    _, err := store.EnqueueTx(ctx, tx, outbox.Event{
        PartitionKey: subjectID,
        Payload:      payload,
    })
    return err
})
```

Бакет всегда вычисляется из `PartitionKey` здесь, а не берётся у вызывающего,
чтобы все писатели шардировали очередь одинаково.

### Публикация

```go
relay := outbox.NewRelay(store, outbox.PublisherFunc(
    func(ctx context.Context, ev outbox.Event) error {
        topic := ev.Topic
        if topic == "" {
            topic = defaultTopic
        }
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

Аренда бакетов заменяет `SELECT ... FOR UPDATE SKIP LOCKED`, которого в YDB нет:
инстансы не соревнуются за отдельные строки, а делят бакеты между собой. Один
инстанс просто забирает все. Бакеты умершего инстанса возвращаются в оборот,
когда истекает его аренда.

Lease renewal работает в отдельной goroutine и не ждёт завершения broker
publish. Relay держит локальный fencing deadline: если успешного renew не было
до TTL, контекст in-flight publish отменяется и бакет исчезает из owned.
`OutboxStore` дополнительно сверяет owner/`locked_until` перед каждым publish и
в той же короткой транзакции перед delete/failure update. Поэтому старый owner
после handoff не продолжает оставшуюся часть batch и не удаляет строку нового
владельца. Окно at-least-once для уже ушедшего во внешний broker сообщения
остаётся принципиально — consumer по-прежнему обязан быть идемпотентным.

### Проверка и targeted replay dead letters

`OutboxStore` дополнительно реализует optional-интерфейс
`outbox.DeadLetterStore`. Он не добавлен в горячий `outbox.Store`, поэтому
существующие PostgreSQL-сторы остаются совместимы.

```go
page, err := store.ListDeadLetters(ctx, outbox.DeadLetterListOptions{Limit: 100})
if err != nil {
    return err
}
for _, letter := range page.Items {
    log.Info("outbox dead letter",
        zap.String("eventId", letter.ID),
        zap.String("topic", letter.Topic),
        zap.Int("attempts", letter.Attempts),
        zap.String("lastError", letter.LastError),
        zap.Time("failedAt", letter.FailedAt))
}

state, err := store.ReplayDeadLetter(ctx, page.Items[0].Ref())
// state == outbox.ReplayRequeued on the first call;
// state == outbox.ReplayAlreadyPending only while that row is still pending.
```

List идёт keyset-пагинацией в порядке существующего PK
`(bucket, created_at, id)`. `GetDeadLetter` и `ReplayDeadLetter` требуют все три
поля ключа: точечный операторский запрос не превращается в full scan по UUID.
Replay в одной YDB-транзакции:

1. проверяет, что тот же ключ не существует одновременно в pending и dead;
2. переносит исходные topic/payload/partition key в pending;
3. сбрасывает `attempts` в 0 и `last_error` в NULL;
4. удаляет dead row.

Если команда была успешно выполнена, её повтор возвращает `already_pending`,
пока строка ещё pending. После её успешной публикации обеих строк уже нет, и
повтор возвращает typed `ErrDeadLetterNotFound` — новая копия события не
создаётся. Запомнить «этот replay когда-то выполнялся» после delivery без
отдельного audit/tombstone невозможно; добавлять DDL ради этого SDK не стал.
Если ключ одновременно найден в обеих таблицах, возвращается typed
`ErrDeadLetterReplayConflict`, и ни одна строка не перезаписывается.
`OutboxStats` отдаёт `pending_count`, `pending_oldest_at`, `dead_count` и
`dead_oldest_at`; это операторский full aggregate, его следует собирать с
невысокой частотой, а не на каждом relay tick. Audit log с operator identity и
причиной replay пишет вызывающий admin CLI/API — SDK не знает identity
оператора.

## Тесты

Интеграционные тесты идут против локальной YDB из репозитория `infrastructure`
(`task ydb:up`) и пропускаются, если база недоступна, — поэтому `go test ./...`
остаётся зелёным без Docker. Таблицы каждый тест создаёт и удаляет сам.

```bash
task ydb:up          # в репозитории infrastructure
go test ./ydb/...
```

Переопределяется переменными `YDB_TEST_DSN` (по умолчанию
`grpc://127.0.0.1:2136/local`) и `YDB_TEST_TABLE_PATH_PREFIX` (`/local`).

## Релиз

Модуль вложенный, поэтому его теги префиксованы — этого требует Go. Родительский
модуль релизится первым, потому что этот зависит от него:

```bash
task release:minor           # корневой модуль -> vX.Y.0
cd ydb && go mod edit -require=github.com/Alexey-zaliznuak/aheron-go-sdk@vX.Y.0
git commit -am "ydb: require aheron-go-sdk vX.Y.0"
task release:ydb             # -> ydb/v0.0.1
```

`task release:ydb` сам откажется работать, если `ydb/go.mod` ссылается не на
последний выпущенный корневой тег: иначе уехал бы модуль, который не собирается.

Локальная разработка опирается на `go.work` в корне репозитория — он закоммичен,
без него из свежего клона не собрать ни один из двух модулей, пока корневой не
опубликован.
