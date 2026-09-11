# SDK tasks

## Installation lifecycle

- [x] Отдельный signed lifecycle protocol с installationId, постоянным sequence,
  проверяемой квитанцией и защитой от подмены адресата.
- [x] Отдельное назначение Ed25519-подписи до timestamp: подписи обычных блоков
  и lifecycle нельзя использовать друг вместо друга.
- [x] Чистый DecideLifecycle для применения в транзакции получателя; persistent
  watermark/tombstone, конфликты повторов и отсутствие эффекта от старых событий.
- [x] Отправитель: один запрос, HTTPS по умолчанию, без редиректов/fallback;
  строгая квитанция вместо пустого 200/202, без секретов в ошибках.
- [x] Проверки 24 порядков доставки, конкурентных повторов и HTTP validation;
  `task test`, `task test:lifecycle -- -race`, `task vet` проходят.
- [ ] Выпустить подготовленный SDK v0.33.0 из main после разрешения публикации.
- [ ] Подключить опубликованную версию к backend и интеграциям вместе с постоянным
  состоянием. Сам SDK не заменяет транзакции получателя и durable worker backend.

Схема следующего шага: `../docs/integration-uninstall-storage-proposal.md`.
