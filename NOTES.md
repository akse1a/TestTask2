# NOTES — Implementation C

Payment mini-API with idempotency, Go standard library only (`net/http`,
`encoding/json`, `sync`). In-memory, thread-safe.

## Design lane (C): `sync.Map` + atomic load-or-store

- The store (`store.go`) holds two `sync.Map`s:
  - `payments`: `id -> *Payment` for GET / cancel.
  - `idem`: `idempotency-key -> *idemRecord` for the creation path.
- **Idempotency & concurrency (SPEC §5)** rest entirely on `sync.Map.LoadOrStore`,
  which is atomic. On `POST /payments` we eagerly build a candidate `*Payment`
  (id + timestamps) and attempt to publish it under the key with a single
  `LoadOrStore`. Exactly one racer wins the store; everyone else receives the
  winner's record. **No separate lock is taken on the hot path.** The id burned
  by a losing racer is simply discarded — it is never indexed by id, so no
  orphan payment is observable. This guarantees "N concurrent POSTs, same key →
  exactly one payment", verified by `TestConcurrentCreateSingleKey` under `-race`.
- **Key-reuse detection**: each `idemRecord` carries a SHA-256 hash of the
  *normalized* body — the canonical string `"<amount_minor>|<currency>"`
  (`normalizedBodyHash`). Because it hashes the semantic pair, JSON field order
  and whitespace do not matter (SPEC §2.1). Same key + equal hash → `200` with
  the existing payment; same key + different hash → `409 idempotency_key_reuse`.

## Mutation safety

- Creation is lock-free, but `cancel` mutates `status`/`updated_at`. Each
  `Payment` carries its own `sync.Mutex` guarding only those mutable fields;
  reads for serialization go through `snapshot()` under the same lock, so a
  JSON response is always a consistent snapshot. The mutex field is unexported
  and never serialized (a dedicated `paymentDTO` carries the snake_case wire shape).

## Validation details

- `Idempotency-Key`: required, 1–255 chars. Missing **or empty or >255** →
  `400 missing_idempotency_key`. The spec names a code only for absence; I map
  length violations to the same code (closest available) rather than inventing one.
- Body is first unmarshaled into `map[string]json.RawMessage`; failure →
  `400 invalid_json`.
- `amount_minor`: parsed via `json.Number` then `.Int64()`, so fractional
  numbers like `1.5` are rejected as `invalid_amount` (not silently truncated).
  Missing / non-numeric / `<= 0` → `400 invalid_amount`.
- `currency`: must be exactly 3 chars and one of RUB/USD/EUR → else
  `invalid_currency`.
- Unknown extra fields are ignored (allowed by SPEC §2.1).
- Method mismatch on a known path → `405 method_not_allowed` with the JSON error
  envelope. Handlers are registered path-only (not method-scoped) so we emit our
  own envelope instead of `net/http`'s empty-bodied 405.

## Routing

- `net/http.ServeMux` with Go 1.22+ path wildcards (`/payments/{id}`,
  `/payments/{id}/cancel`) — standard library, no framework. `PathValue("id")`
  extracts the id.

## Tests

`server_test.go` covers all 8 SPEC §7 items (1 create-201, 2 idempotent repeat
incl. reordered/whitespaced body, 3 key-reuse-409, 4 missing key, 5 amount &
currency validation as sub-tests, 6 GET 200/404, 7 cancel + idempotent re-cancel
+ 404, 8 concurrency with 64 goroutines) plus healthz and method-not-allowed.

## Environment caveat (IMPORTANT, honest)

This machine's **global** Go env has `CC=x86_64-w64-mingw32-gcc` (a Windows
MinGW cross-compiler) even though `GOOS=linux`. The race detector requires cgo,
and that cross-compiler cannot find Linux libc headers (`grp.h`, `sys/mman.h`),
so `go test -race` / `go vet` fail *with the machine's default CC*. Running with
the correct native compiler makes both green:

    CC=gcc go vet ./...        # exit 0
    CC=gcc go test -race ./... # ok  paymentapi

I did **not** modify the global `go env` (it lives outside this worktree). On a
normally-configured Linux host `CC` already points at native gcc/clang and the
plain `go vet ./...` / `go test -race ./...` commands pass as-is. This is purely
a host toolchain misconfiguration, not a code issue.

## Known limitations / not done

- In-memory only; restart clears data (expected for v1.0, SPEC §6).
- No pagination/list endpoint (not in spec).
- `captured` status reserved but intentionally not implemented (SPEC §3).
- Idempotency records are never evicted (unbounded growth over process
  lifetime). Fine for the exercise; a production version would add TTL/eviction.
- A losing concurrent racer consumes one `crypto/rand` id that is then
  discarded — negligible waste, ids are not sequential so there is no gap concern.
