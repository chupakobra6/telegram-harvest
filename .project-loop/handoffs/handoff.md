# Handoff

Проект: telegram-harvest
Обновлено: 2026-07-31

## Цель

- Убрать лишний пустой history proof RPC только при доказанной полноте checkpoint range.
- Выбрать единый статический безопасный pacing для main/daily и study без runtime-регулятора.

## Текущий Шаг

- active step: `STEP-007`
- status: `готово`
- requirements: `REQ-018`—`REQ-020`, `VAL-014`—`VAL-015`, `CON-001`, `SCOPE-005`

## Реализовано

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

- `go test -count=1 ./...` — зелёный.
- `go vet ./...` — зелёный.
- `go test -race -count=1 ./internal/mtproto ./internal/harvest ./internal/config ./cmd/telegram-harvest` — зелёный.
- `git diff --check` — зелёный.
- Project Loop validation — зелёный.

## Ревью И Риски

- Findings после self-review закрыты: непроверенный 500-ms default не распространён на study; `count` Telegram history не ошибочно трактуется как размер bounded `MinID` window.
- Proof намеренно консервативен: при любой неопределённости теряется только ускорение, а не сообщения.
- Pacing account- и workload-specific; при будущих стабильных FloodWait на main floor нужно пересмотреть вверх, но автоматически колебать его на каждом запуске не следует.
- ASR/backend/media quality и daily sender scope не менялись.

## Следующее Действие

- Шаг завершён. Следующий обычный `daily-catchup` автоматически использует main spacing 500 ms и checkpoint proof; по timing report проверить реальные `checkpoint_proof_stops` на следующем новом полном дне.

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
