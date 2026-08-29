# Сессия 02 · Субагент B (ветка impl-b) — ВЫБРАНА как основная

Изолированный worktree `/home/akse1a/TestTask2-impl-b`. Дизайн-полоса: слои
`store`/`handlers`/`apperr`, `RWMutex` + атомарное резервирование ключа идемпотентности.

## Промпт (дословно)

См. шаблон в `01-orchestration.md`, папка `impl-b`, полоса «layered, RWMutex, per-key
reservation, typed errors».

## Результат (со слов агента)

- Коммит `cc25da5` в ветке `impl-b`, дерево чистое.
- `go vet ./...` — exit 0; `go test -race ./...` — `ok paymentapi/handlers`, `ok paymentapi/store`.
- Три пакета: `store/` (in-memory, `RWMutex` + per-key `idemEntry` mutex),
  `handlers/` (явный роутер, per-field валидация, единый конверт ошибок),
  `apperr/` (таблица статус↔код из SPEC §4).

## Что честно отметил агент

1. **Поправил окружение глобально:** выполнил `go env -w CC=gcc` (пишется в
   `~/.config/go/env`), чтобы команды DoD были зелёными без per-invocation override.
   Это влияет и на сиблинг-worktree — пометил явно.
2. Длинный (>255) `Idempotency-Key` → `400 missing_idempotency_key` (нет отдельного кода).
3. Мапа ключей растёт без TTL/эвикции — ок для in-memory v1.0, в проде нужна эвикция.
4. `captured` намеренно не реализован (зарезервирован SPEC §3).
5. Bash-песочница мешает сборке go (нет доступа к `/usr/include`) — гонял с отключённой.

## Почему выбрана

При равной корректности (25/25 conformance) — наиболее пригодна к сопровождению и самая
строгая валидация: `amount_minor` через `json.Number` (строка `"100"`, `1.5`, `true`, `null`
→ `invalid_amount`), кап тела 1 MiB, чёткое разделение 404/405. Детали — `reports/comparison.md`.
