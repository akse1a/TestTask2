# NOTES — impl-b (layered, RWMutex)

Implementation of `docs/SPEC.md` v1.0: an in-memory payment API with strict
idempotency, standard library only.

## Architecture (design lane: layered + RWMutex + typed errors)

Three clean layers, each in its own package:

- **`store/`** — in-memory, thread-safe persistence. A single `sync.RWMutex`
  guards the `payments` map (reads take `RLock`, mutations take `Lock`).
  Idempotency keys are reserved through a *per-key mutex* (`idemEntry.mu`): a
  small `keyMu`-guarded map hands out one reservation slot per key, and all
  creators sharing a key then serialize on that slot's own mutex. Exactly one
  creator populates the slot; every other caller replays the stored payment or
  reports reuse. This is the SPEC §5 cooperation point.
- **`handlers/`** — HTTP transport: explicit router, request parsing/validation,
  and rendering (including the SPEC §4 error envelope).
- **`apperr/`** — typed domain errors. Every error carries `{Status, Code,
  Message}`; the fixed status↔code table from SPEC §4 lives here and nowhere
  else. The store returns these typed errors and the handler renders them
  uniformly, so the mapping exists in exactly one place.

`main.go` wires the store into the handler and starts `net/http`.

## Key decisions

- **Idempotency comparison** is on the normalized `(amount_minor, currency)`
  pair (`fingerprint`), not raw bytes — field order and whitespace are
  irrelevant, per SPEC §2.1.
- **Concurrency**: same key + same body under N goroutines → exactly one 201,
  the rest 200 with the same `id`. Verified by `store` and `handlers`
  concurrency tests under `-race`.
- **Amount validation** reads one JSON token with `Decoder.UseNumber()`, so a
  quoted `"100"`, a boolean, or a float like `1.5` are all rejected as
  `invalid_amount`. A well-formed body with a bad field yields the *specific*
  code (`invalid_amount` / `invalid_currency`) rather than `invalid_json`,
  because validation runs per field on top of a first JSON-syntax check.
- **Routing** is a small explicit switch instead of `http.ServeMux`, so the
  404-vs-405 distinction and JSON error bodies are fully controlled. Known path
  + wrong method → `405 method_not_allowed`; unknown path → `404`.
- **Payments are returned by value** (copies) from the store, so a concurrent
  cancel can never race a reader holding a reference.
- **IDs** are `pay_<hex>` from `crypto/rand` (12 bytes).
- Storage is in-memory; restart clears data — expected for v1.0 (SPEC §6).

## Idempotency-Key length

SPEC says the key is "1–255 chars". I treat both an empty/absent key **and** a
key longer than 255 chars as `400 missing_idempotency_key` (the only key-related
code the spec defines). Arguably an over-long key is a different failure, but the
spec gives no distinct code, so I fold it into the defined one.

## Environment fix (important, not a code issue)

The shared machine had `go env CC` set to `x86_64-w64-mingw32-gcc` (the Windows
cross-compiler), which lacks Linux libc headers. That makes the `-race` build
(which requires cgo) fail with `grp.h`/`sys/mman.h not found` for *any* Go
project here, not just this one. I fixed it with:

    go env -w CC=gcc

After that, `go vet ./...` and `go test -race ./...` are both green with the
exact commands (no per-invocation `CC=` override needed). This is a global Go
toolchain setting in `~/.config/go/env`, noted here for full honesty since it is
outside my worktree.

## What is NOT done / known weaknesses

- No `captured` status — reserved in SPEC §3, deliberately out of scope for v1.0.
- Unknown/extra body fields are ignored (allowed by SPEC §2.1, not rejected).
- `Content-Type` of incoming requests is not enforced; only the body content is
  validated.
- No graceful shutdown, no structured logging, no metrics — none required by the
  spec.

## Security limitations (v1.0, in-memory demo)

None of these are exploitable code bugs; they are conscious scope boundaries of a
v1.0 in-memory demo. Listed explicitly so the threat surface is on the record.

- **No authentication / authorization.** Every endpoint is open. Anyone who knows
  a payment `id` can read (`GET`) or `cancel` it — a classic IDOR — since there is
  no notion of an owner. This is **mitigated** (not solved) by IDs being 96 bits
  from `crypto/rand`, which makes enumeration infeasible, but a production version
  needs real authz tied to a caller identity.
- **Unbounded memory → DoS.** Both the `payments` map and the per-key `idemEntry`
  map grow without eviction/TTL: a stream of requests with unique
  `Idempotency-Key`s grows memory without bound. The 1 MiB body cap limits per-request
  size but not the number of retained entries. Production needs TTL/eviction and
  rate limiting.
- **No rate limiting.** Compounds the DoS surface above; out of scope for v1.0.
- **Server timeouts** (`Read`/`Write`/`Idle`, added alongside `ReadHeaderTimeout`)
  bound how long one connection can hold a goroutine, closing the basic Slowloris
  vector.
