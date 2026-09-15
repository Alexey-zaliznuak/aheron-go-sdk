# SDK tasks

## Integration OAuth

- [x] OAuth clients and ordered installation lifecycle remain the supported
  platform integration contract; retired migration receivers and proof helpers
  were removed after the production cutover.

- [x] Отдельный Provider для clientId/keyId/окружения: Ed25519 private_key_jwt,
  новые jti, точный form-контракт auth-service и строгая проверка ответа.
- [x] Ограниченный кеш по project/installation/audience/scopes, lazy refresh с
  jitter, singleflight, независимая отмена ожидающих и безопасная инвалидация.
- [x] Ресурсный HTTP-клиент: HTTPS origin/path binding, без redirects/cookies/
  legacy fallback; максимум один 401 retry для GET/HEAD или явной идемпотентной
  операции с воспроизводимым телом.
- [x] Подключить OAuth к typed Steps/Triggers: audience execution, проверка
  конфигурации проекта, отдельные scopes, безопасные повторы без fallback.
- [x] CRMOAuth: все существующие typed CRM-методы, project/installation binding,
  scopes по операциям, upsert с variables.write, безопасные ошибки без fallback;
  GET может повторить 401 один раз, записи автоматически не повторяются.
- [x] FilesOAuth: все typed Files-методы, audience media, отдельные files.read/
  files.write, S3 PUT без OAuth/cookies/redirects, ошибки без секретов.
- [x] LinksOAuth для шести проектных методов: scopes/project/owner, безопасный
  Create retry по сохранённому ключу; RegisterCallback не использует grant проекта.
- [x] Application OAuth для Catalog и Links.RegisterCallback: отдельное право
  клиента на собственные глобальные настройки, без project installation grant.
- [ ] Обновить интеграции следующей опубликованной minor-версией SDK.

Контракт и настройки: `docs/integration-oauth.md`. Реальные установки не переключены.

## Template files

- [x] Platform-only RetireSnapshot: stable DELETE, explicit 204, no redirects or automatic retries.
- [ ] Backend must persist and retry snapshot retirement after revoking revision access.

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
- [x] SDK v0.33.0 опубликован из main (27fd345); release выполнил test/vet обоих модулей.
- [ ] Подключить опубликованную версию к backend и интеграциям вместе с постоянным
  состоянием. Сам SDK не заменяет транзакции получателя и durable worker backend.

Схема следующего шага: `../docs/integration-uninstall-storage-proposal.md`.

- [x] Manifest.InstallationLifecyclePath / installationLifecycleUrl declares the durable v1 receiver independently of legacy callbacks.
