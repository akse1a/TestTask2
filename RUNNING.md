# Как запустить проект

Платёжный мини-API на Go (стандартная библиотека, без внешних зависимостей).
Все команды ниже проверены на Linux; порт по умолчанию — `8080`.

## Требования

- **Go 1.21+** — единственная зависимость. Проверить: `go version`.
- Хранилище in-memory, БД и внешние сервисы не нужны.

> На этой машине `go env CC` указывает на кросскомпилятор Windows, из-за чего
> сборка с cgo падает. Сервису cgo не нужен — запускайте с `CGO_ENABLED=0`
> (в командах ниже уже учтено).

## 1. Запуск сервера

```bash
cd service
CGO_ENABLED=0 go run .
# paymentapi listening on :8080
```

Свой порт:

```bash
cd service
CGO_ENABLED=0 PORT=8099 go run .
```

Проверка живости (в другом терминале):

```bash
curl -s localhost:8080/healthz
# {"status":"ok"}
```

## 2. Проверка вручную (curl)

```bash
# Создать платёж — идемпотентно по заголовку Idempotency-Key. Успех → 201.
curl -s -X POST localhost:8080/payments \
  -H 'Idempotency-Key: order-42' \
  -H 'Content-Type: application/json' \
  -d '{"amount_minor": 10000, "currency": "RUB"}'
# {"id":"pay_...","amount_minor":10000,"currency":"RUB","status":"created",...}

# Повтор с тем же ключом и телом → 200, тот же платёж (второй НЕ создаётся).
curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost:8080/payments \
  -H 'Idempotency-Key: order-42' \
  -d '{"amount_minor": 10000, "currency": "RUB"}'
# 200

# Тот же ключ, ДРУГОЕ тело → 409 idempotency_key_reuse.
curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost:8080/payments \
  -H 'Idempotency-Key: order-42' \
  -d '{"amount_minor": 999, "currency": "RUB"}'
# 409

# Статус по id (подставьте id из ответа на создание).
curl -s localhost:8080/payments/pay_xxx

# Отмена (повторная отмена идемпотентна → снова 200).
curl -s -X POST localhost:8080/payments/pay_xxx/cancel
```

Полный контракт (эндпоинты, коды ошибок, статусы) — в [docs/SPEC.md](docs/SPEC.md).

## 3. Тесты

Юнит- и конкурентные тесты (в составе реализации):

```bash
cd service
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test ./...
```

С детектором гонок (нужен рабочий cgo-компилятор, напр. `CC=gcc`):

```bash
cd service
CC=gcc go test -race ./...
```

## 4. Чёрно-ящичный conformance-набор

Поднимает сервер сам и проверяет контракт по HTTP (27 проверок, включая
64-поточный тест идемпотентности). Нужен только Python 3 (стандартная библиотека):

```bash
# из корня репозитория
CGO_ENABLED=0 python3 conformance/conformance.py service
# == 27/27 checks passed ==
```

## Диагностика

| Симптом | Причина / решение |
|---|---|
| `gcc: ... not found`, ошибки сборки cgo | Запускайте с `CGO_ENABLED=0` (см. заметку выше). |
| `bind: address already in use` | Порт занят — задайте другой: `PORT=8099 go run .`. |
| `go: command not found` | Go не установлен или не в `PATH` — поставьте Go 1.21+. |
| conformance «connection refused» | Порт 8080 занят другим процессом — освободите его. |
