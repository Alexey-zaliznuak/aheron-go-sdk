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
