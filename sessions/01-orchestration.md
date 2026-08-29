# Сессия 01 · Оркестрация (главная сессия)

Инструмент: Claude Code (Opus 4.8). Соло. Вариант C1, стек Go, «максимум баллов».

## Ход

1. Прочитал `04 · Домашнее задание №2.md` и `05 · Материалы к ДЗ №2.md`, вытащил три
   обязательных пункта и список необязательных.
2. Выбрал вариант **C1** (платёжный мини-API с идемпотентностью) — он естественно ложится
   на субагентов (три реализации одного контракта) и на worktrees.
3. `git init`, структура папок, написал контракт `docs/SPEC.md` v1.0.
4. Написал workflow `workflows/spec-parallel-build.md` + `workflows/PROMPT.txt`.
5. Создал три worktree и ветки `impl-a/b/c`.
6. Записал время старта и **спавнил трёх субагентов параллельно** (по одному на worktree),
   каждому — своя «дизайн-полоса», чтобы реализации отличались:
   - A: единый `sync.Mutex` + map, простейшая проверка-вставка под локом;
   - B: слоистая архитектура, `sync.RWMutex`, атомарное резервирование ключа;
   - C: `sync.Map` / load-or-store, хеш нормализованного тела для детекта reuse.
7. Пока агенты работали — написал общий conformance-набор `conformance/conformance.py`
   (чёрный ящик по HTTP) и проверил PDF-конвейер (headless Google Chrome).

## Дословный промпт на параллельный спавн (шаблон, одинаковый для A/B/C с заменой буквы и «полосы»)

```
You are implementer A in a parallel build. Work ONLY inside your assigned git worktree:
    /home/akse1a/TestTask2-impl-a
Read docs/SPEC.md in full and implement it as a Go HTTP service at the worktree root.
Go stdlib only, in-memory thread-safe storage. Implement every endpoint/status/error code.
Idempotency + concurrency (SPEC §5) must be correct: two simultaneous POSTs with the same
Idempotency-Key + same body create exactly ONE payment. Write tests covering SPEC §7 incl.
the concurrency test; go vet ./... and go test -race ./... must be green. go run . starts the
server; GET /healthz -> 200. Write NOTES.md with decisions and an HONEST list of what is not
finished. Design lane: single sync.Mutex + map, simplest check-then-insert. Finish by committing
to YOUR branch (git add -A && git commit -m "feat(impl-a): ..."). Do not merge or push.
Report back: commit hash, exact output of go test -race ./... and go vet ./..., 3-line summary.
```

## Дословный промпт workflow (сохранён в `workflows/PROMPT.txt`)

См. файл `workflows/PROMPT.txt` — им процедура запускается повторно на любом контракте.

## Замер «последовательно против параллельно»

См. `reports/measurement.md` (заполняется по факту: время и остаток контекста главной сессии).
