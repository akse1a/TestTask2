#!/usr/bin/env python3
"""Чёрно-ящичный conformance-набор для платёжного API (docs/SPEC.md v1.0).

Запускает `go run .` в указанной папке реализации, ждёт /healthz, гоняет контракт
и печатает PASS/FAIL по каждому пункту. Возвращает код 0, если все пройдены.

Использование:
    python3 conformance/conformance.py /path/to/impl-dir
"""
import json
import os
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor

RESULTS = []


def free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def req(method, url, body=None, headers=None):
    data = None
    if body is not None:
        data = json.dumps(body).encode() if isinstance(body, (dict, list)) else body.encode()
    r = urllib.request.Request(url, data=data, method=method)
    r.add_header("Content-Type", "application/json")
    for k, v in (headers or {}).items():
        r.add_header(k, v)
    try:
        with urllib.request.urlopen(r, timeout=10) as resp:
            raw = resp.read().decode()
            return resp.status, (json.loads(raw) if raw.strip() else {})
    except urllib.error.HTTPError as e:
        raw = e.read().decode()
        try:
            return e.code, json.loads(raw) if raw.strip() else {}
        except json.JSONDecodeError:
            return e.code, {"_raw": raw}


def check(name, cond, detail=""):
    RESULTS.append((name, bool(cond), detail))
    mark = "PASS" if cond else "FAIL"
    print(f"  [{mark}] {name}" + (f" — {detail}" if detail and not cond else ""))


def err_code(body):
    return (body or {}).get("error", {}).get("code")


def run_suite(base):
    # 1. create -> 201
    st, b = req("POST", f"{base}/payments", {"amount_minor": 10000, "currency": "RUB"},
                {"Idempotency-Key": "k1"})
    check("1. create returns 201", st == 201, f"got {st} {b}")
    check("1b. status is created", b.get("status") == "created", f"got {b.get('status')}")
    check("1c. has id/created_at", bool(b.get("id")) and bool(b.get("created_at")), str(b))
    pid = b.get("id")
    created_at = b.get("created_at")

    # 2. same key + same body -> 200 same id
    st, b2 = req("POST", f"{base}/payments", {"amount_minor": 10000, "currency": "RUB"},
                 {"Idempotency-Key": "k1"})
    check("2. replay same key/body -> 200", st == 200, f"got {st}")
    check("2b. replay returns same id", b2.get("id") == pid, f"{b2.get('id')} vs {pid}")
    check("2c. replay same created_at", b2.get("created_at") == created_at, "created_at changed")

    # 2d. field-order independence: same values, different JSON field order
    st, b2b = req("POST", f"{base}/payments", '{"currency":"RUB","amount_minor":10000}',
                  {"Idempotency-Key": "k1"})
    check("2d. body compared by value not bytes", st == 200 and b2b.get("id") == pid,
          f"got {st} id={b2b.get('id')}")

    # 3. same key, different body -> 409
    st, b3 = req("POST", f"{base}/payments", {"amount_minor": 99999, "currency": "RUB"},
                 {"Idempotency-Key": "k1"})
    check("3. same key/different body -> 409", st == 409, f"got {st}")
    check("3b. code idempotency_key_reuse", err_code(b3) == "idempotency_key_reuse", str(b3))

    # 4. missing idempotency key -> 400
    st, b4 = req("POST", f"{base}/payments", {"amount_minor": 100, "currency": "RUB"})
    check("4. missing key -> 400", st == 400, f"got {st}")
    check("4b. code missing_idempotency_key", err_code(b4) == "missing_idempotency_key", str(b4))

    # 5. invalid amount / currency
    st, b5 = req("POST", f"{base}/payments", {"amount_minor": 0, "currency": "RUB"},
                 {"Idempotency-Key": "k-amt"})
    check("5. amount<=0 -> 400", st == 400, f"got {st}")
    check("5b. code invalid_amount", err_code(b5) == "invalid_amount", str(b5))
    st, b5c = req("POST", f"{base}/payments", {"amount_minor": 100, "currency": "GBP"},
                  {"Idempotency-Key": "k-cur"})
    check("5c. bad currency -> 400", st == 400, f"got {st}")
    check("5d. code invalid_currency", err_code(b5c) == "invalid_currency", str(b5c))
    st, b5e = req("POST", f"{base}/payments", "{not json",
                  {"Idempotency-Key": "k-json"})
    check("5e. invalid json -> 400", st == 400, f"got {st}")

    # 6. GET existing / missing
    st, b6 = req("GET", f"{base}/payments/{pid}")
    check("6. GET existing -> 200", st == 200 and b6.get("id") == pid, f"got {st}")
    st, b6b = req("GET", f"{base}/payments/pay_does_not_exist")
    check("6b. GET missing -> 404", st == 404, f"got {st}")
    check("6c. code payment_not_found", err_code(b6b) == "payment_not_found", str(b6b))

    # 7. cancel created -> canceled; cancel again -> 200 idempotent
    st, b7 = req("POST", f"{base}/payments/{pid}/cancel")
    check("7. cancel created -> 200 canceled", st == 200 and b7.get("status") == "canceled",
          f"got {st} {b7.get('status')}")
    st, b7b = req("POST", f"{base}/payments/{pid}/cancel")
    check("7b. cancel again -> 200 idempotent", st == 200 and b7b.get("status") == "canceled",
          f"got {st} {b7b.get('status')}")
    st, b7c = req("POST", f"{base}/payments/nope/cancel")
    check("7c. cancel missing -> 404", st == 404, f"got {st}")

    # 8. concurrency: N goroutines-equivalent, same key -> exactly one payment
    def hit():
        return req("POST", f"{base}/payments", {"amount_minor": 500, "currency": "USD"},
                   {"Idempotency-Key": "concurrent-key"})
    with ThreadPoolExecutor(max_workers=32) as ex:
        outs = list(ex.map(lambda _: hit(), range(64)))
    ids = {b.get("id") for st, b in outs if st in (200, 201)}
    statuses = [st for st, _ in outs]
    check("8. concurrent same-key -> exactly one payment", len(ids) == 1,
          f"distinct ids={ids} statuses={set(statuses)}")

    # extra: healthz, 404 route, 405 method
    st, _ = req("GET", f"{base}/healthz")
    check("9. healthz -> 200", st == 200, f"got {st}")
    st, bm = req("DELETE", f"{base}/payments/{pid}")
    check("10. unsupported method -> 405", st == 405, f"got {st} (code={err_code(bm)})")

    # 11. a full-length 255-char ASCII key is accepted (boundary, SPEC §2.1).
    #     (The bytes-vs-runes fix for multi-byte keys is validated by a Go unit
    #     test in service/handlers — non-latin-1 header values can't travel over
    #     a real HTTP client, so it's not exercised here.)
    st, bu = req("POST", f"{base}/payments", {"amount_minor": 100, "currency": "RUB"},
                 {"Idempotency-Key": "a" * 255})
    check("11. 255-char key accepted", st == 201, f"got {st} {bu}")

    # 12. unknown route -> 404 with code from the fixed list (SPEC §4)
    st, bn = req("GET", f"{base}/no-such-route")
    fixed = {"missing_idempotency_key", "invalid_json", "invalid_amount",
             "invalid_currency", "idempotency_key_reuse", "payment_not_found",
             "method_not_allowed", "not_found", "internal_error"}
    check("12. unknown route -> 404, code in fixed list", st == 404 and err_code(bn) in fixed,
          f"got {st} code={err_code(bn)}")


def main():
    if len(sys.argv) < 2:
        print("usage: conformance.py <impl-dir>")
        sys.exit(2)
    impl = os.path.abspath(sys.argv[1])
    port = free_port()
    env = dict(os.environ, PORT=str(port))
    print(f"== conformance against {impl} (port {port}) ==")
    proc = subprocess.Popen(["go", "run", "."], cwd=impl, env=env,
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    base = f"http://127.0.0.1:{port}"
    # wait for healthz
    ok = False
    for _ in range(60):
        if proc.poll() is not None:
            out = proc.stdout.read().decode()
            print("SERVER EXITED EARLY:\n", out)
            sys.exit(1)
        try:
            with urllib.request.urlopen(f"{base}/healthz", timeout=1):
                ok = True
                break
        except Exception:
            time.sleep(0.5)
    if not ok:
        print("server did not become healthy")
        proc.terminate()
        sys.exit(1)
    try:
        run_suite(base)
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()

    passed = sum(1 for _, ok, _ in RESULTS if ok)
    total = len(RESULTS)
    print(f"\n== {passed}/{total} checks passed ==")
    sys.exit(0 if passed == total else 1)


if __name__ == "__main__":
    main()
