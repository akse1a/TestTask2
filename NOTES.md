# NOTES — Implementation A

Payment mini-API per `docs/SPEC.md` v1.0. Go standard library only
(`net/http`, `encoding/json`, `sync`). In-memory, thread-safe.

## Design lane (A): single global mutex + map

- `store.go` — one `sync.Mutex` guards two maps: `byID` (id → *Payment) and
  `byKey` (idempotency key → id). **Every** store method takes the lock for its
  whole body. There is exactly one lock, so there is no lock-ordering concern
  and no partially-updated view.
- Idempotency is the simplest possible **check-then-insert under the lock**
  (`CreateIdempotent`): while holding the mutex we look up the key; if present
  we return the existing payment (200) or a conflict (409) depending on whether
  the normalized body matches; otherwise we insert a new payment (201). Because
  the whole decision is atomic under the single lock, two simultaneous POSTs
  with the same key create exactly one payment (SPEC §5). The concurrency test
  (`TestCreate_ConcurrentSameKey`, 64 goroutines released together) asserts the
  store holds exactly 1 payment, all responses share the id, and exactly one is
  201.
- Store methods return **copies** of `Payment` (by value), so handlers
  serialize JSON without holding the lock and callers cannot mutate stored state.

## Contract decisions

- **Body normalization**: comparison is on the normalized pair
  `(amount_minor, currency)`, not byte-for-byte, so field order/whitespace do
  not matter (SPEC §2.1). Verified in `TestCreate_IdempotentReplay`.
- **amount_minor** is parsed from `json.RawMessage` so we can distinguish
  absent vs. present-but-invalid, and explicitly reject floats (`1.5`), `null`,
  strings, and values `<= 0` — all → `400 invalid_amount`.
- **currency** must be exactly 3 A–Z letters AND in {RUB, USD, EUR}; otherwise
  `400 invalid_currency`. Missing currency → `invalid_currency`.
- **Routing** is done by hand in `ServeHTTP` so that a known path with an
  unsupported method returns `405 method_not_allowed` (not 404). Unknown paths
  return `404 payment_not_found`.
- **id** format: `pay_<hex>` from 16 random bytes (`crypto/rand`).
- **Timestamps**: RFC3339 UTC. `created_at` is fixed at creation; `cancel`
  updates `updated_at` only on the real `created → canceled` transition; a
  repeated cancel returns the same object unchanged (idempotent, 200).

## Places I was unsure / judgment calls

- **Idempotency-Key length**: SPEC says "1–255 chars" but defines an error code
  only for the missing case (`missing_idempotency_key`). I treat empty as
  missing, and reject `> 255` with `400 missing_idempotency_key` as the closest
  available code. A stricter reading might accept over-long keys instead.
- **Unknown JSON fields** are ignored (SPEC allows either behavior).
- **404 vs 405 on `/payments/`** (empty id) and malformed nested paths: I return
  404 `payment_not_found`. Not dictated by the spec.

## Environment caveat (important, honest)

- This machine's `go env CC` defaults to `x86_64-w64-mingw32-gcc` (a Windows
  cross-compiler). The race detector requires cgo, and that cross-compiler
  cannot compile the Linux `runtime/cgo`, so a bare `go test -race ./...` fails
  to **build** (not a test failure — a toolchain misconfiguration). Building
  with the native compiler works:

  ```
  CC=gcc go vet ./...        # exit 0
  CC=gcc go test -race ./... # ok  paymentapi
  ```

  The application code itself uses only the standard library and has no cgo.
  Fix for the environment (not applied here, since `go env -w` is shared across
  the sibling worktrees): `go env -w CC=gcc`.

## Not finished / known weaknesses

- No request body size limit — a hostile client could send an arbitrarily large
  JSON body. `net/http` default server limits mitigate but I did not add
  `http.MaxBytesReader`.
- In-memory only: restart clears all data (expected for v1.0, SPEC §6).
- No pagination/listing endpoint (not in scope).
