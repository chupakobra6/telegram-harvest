# Handoff

Проект: telegram-harvest
Обновлено: 2026-07-31

## Цель

- Проверить корректность и архитектуру Telegram Harvest.
- Закрыть безопасные findings и выполнить repo polish без изменения product contracts.

## Текущий Шаг

- active step: `STEP-008`
- status: `готово`
- requirements: `REQ-021`—`REQ-023`, `VAL-016`, `SCOPE-006`

## Реализовано

- Удалён единственный Staticcheck finding: redundant `break` в checkpoint proof switch.
- Go toolchain floor поднят с 1.26.0 до 1.26.5; `x/net` и связанные `x/crypto`/`x/sys`/`x/text` обновлены до исправленных версий.
- `make setup` стал non-mutating; добавлены `make check` и pinned `make audit`.
- Добавлен GitHub CI: standard check, Staticcheck, Govulncheck и full race suite.
- CLI help, README и AGENTS приведены к одному state-dir-relative path contract; ошибочная рекомендация `.state/.state/chat.jsonl` устранена.
- Дефолтные private `chat.jsonl` и `media-manual/` дополнительно игнорируются git.

- `HistoryStats`/`OutgoingStats` различают data pages, empty proof pages, sparse continuations, checkpoint proof candidates/stops, shadow confirmations/rejections и причины безопасного fallback.
- Early stop действует только на первой странице валидного checkpoint-запроса с `MinID > 0`, совпавшим известным head, уникальными ID строго выше `MinID` и exact Telegram response metadata. `inexact`, полный batch, offset/head/count anomaly, historical/first-run/gap/account/scope mismatch и любой flow без checkpoint продолжают старую pagination.
- Shadow mode проверяет candidate следующим запросом и умеет фиксировать как confirmation, так и contradiction; production auto использует enforced proof только при `DialogCheckpoint.Enabled`.
- Timing JSON/CLI получили `history_pagination` и `telegram_rpc`: static spacing, calls, scheduled wait, operation counts и transport floods.
- RPC scheduler остался один, последовательный и под mutex; Telegram crawlers/download RPC не распараллелены.
- Единый spacing для main/daily и study изменён с 700 до 500 ms. Калибровка выполнялась на main; применение того же default к study — прямое решение пользователя. Runtime-adaptive controller, профильное разветвление и новые env/CLI knobs не добавлялись.

## Live Evidence

### Checkpoint proof

- Текущий checkpoint, 700 ms shadow: 41 records, 21 batches, 4 candidates, 4 confirmed, 0 rejected, 0 FloodWait, 19.341 s.
- Enforced на том же состоянии: 41 records, 17 batches, 4 stops, 0 FloodWait, 16.730 s; JSONL побайтно совпал.
- Финальный combined 500 ms: shadow 15.359 s / 26 RPC против enforced 13.080 s / 22 RPC; 45 records, 4/4 confirmed, 0 rejected, 0 FloodWait; SHA-256 файлов одинаков.

### Pacing calibration

- Один historical run 2026-07-25 без media/ASR: 471 dialogs, 46 history dialogs, 98 history batches, 103 sequential RPC slots, 211 records.
- 700 ms: 74.998 s, 0 FloodWait.
- 600 ms: 66.503 s, 0 FloodWait.
- 550 ms: 61.058 s, 0 FloodWait.
- 500 ms: 57.336 s, 0 FloodWait.
- 450 ms: первый run clean 52.928 s; повтор после нижнего probe получил 1 FloodWait.
- 400 ms: первый FloodWait, 53.827 s после retry.
- Два следующих 500-ms run после FloodWait: 56.584/57.615 s, оба clean.
- Median 500 ms — 57.336 s против 74.998 s: `1.31×`, или `−23.6%` wall.
- Все варианты вернули 211 records; normalized JSONL SHA-256 одинаков: `e62281eb15e781a8e92479bed439a223497857876724040009d8610775dc6277`.

### Integrated CLI

- Изолированный `daily --date 2026-07-25 --download-media=false --transcribe=false`: 211 records, 98 history batches, 103 RPC, 500 ms, 0 FloodWait/transport flood, complete, 56.783 s.
- Timing JSON readback: 96 data pages, 2 empty proof pages, 14.967 s scheduled pacing wait, operations `get_dialogs=5`, `get_history=98`.
- Normalized result совпал с 700-ms baseline тем же SHA-256.
- Пользовательские reports, latest catch-up и checkpoint не изменялись; временный harness и `/tmp` artifacts удалены, helper-процессов нет.

## Проверка

- `make check` — зелёный: format, tidy diff, module verify, vet, full tests.
- `make audit` — Staticcheck clean; Govulncheck: 0 reachable vulnerabilities.
- `go test -race -count=1 ./...` — зелёный.
- `make setup`, `make help`, CLI global/command help smoke — зелёные.
- `git diff --check` — зелёный.
- Project Loop validation — зелёный.

## Ревью И Риски

- Blocking correctness/security findings отсутствуют после исправлений.
- `cmd/telegram-harvest/main.go` и `internal/mtproto/client.go` велики, но текущие package boundaries корректны; механический split не уменьшил бы coupling и намеренно не сделан в polish-проходе.
- 22 advisories остаются только в невызываемых transitive module symbols; symbol-level Govulncheck подтверждает 0 достижимых уязвимостей.
- Telegram, checkpoint, ASR/backend/media quality, report contract и daily sender scope не менялись.

## Следующее Действие

- Шаг завершён; обязательных дальнейших исправлений по ревью нет.

## Обновленные Источники Правды

- `.project-loop/requirements/source-map.md`
- `.project-loop/requirements/checklist.md`
- `.project-loop/plan/delivery-plan.md`
- `.project-loop/plan/current-step.md`
- `.project-loop/intake/user-deltas.md`
- `AGENTS.md`
- `README.md`
- `docs/catch-up.md`
- `docs/performance.md`
