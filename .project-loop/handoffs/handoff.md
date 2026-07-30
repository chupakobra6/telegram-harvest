# Handoff

Проект: telegram-harvest
Обновлено: 2026-07-30

## Цель
- Сохранить единый Telegram range-scan и добавить прямые, долговечные stage timings для daily flow.

## Текущий Шаг
- active step: `STEP-002`
- status: `готово`

## Завершено
- `REQ-001`: каждый catch-up диапазон обслуживается одним последовательным `DumpOutgoingRange`.
- `REQ-002`: сохранены daily scope, forwards, media/transcripts, дневные JSONL/Markdown и merged catch-up.
- `REQ-003`: старый и новый flow измерены на диапазоне 2026-07-22—2026-07-29.
- Ошибки/неполный range не публикуют новые пользовательские reports; ASR open/encode errors не теряются.
- `REQ-004`: Telegram scan, download, ffmpeg, Vosk и render измеряются непосредственно у места выполнения, включая failed work.
- `REQ-005`: каждый daily run сохраняет уникальный атомарный `.state/daily/timings/<run-id>-<command>.json`; данные не зависят от перезаписываемых ASR logs.

## Измененные Файлы
- `cmd/telegram-harvest/main.go`, `cmd/telegram-harvest/main_test.go`
- `internal/mtproto/client.go`, `internal/mtproto/client_test.go`
- `internal/harvest/model.go`, `internal/harvest/daily_view.go` и тесты
- `internal/stages/stages.go`, `internal/transcribe/transcribe.go` и тесты
- `cmd/telegram-harvest/stage_timings.go` и тесты
- `README.md`, `AGENTS.md`, `docs/catch-up.md`, `docs/performance.md`
- `.project-loop/`, `inbox/README.md`

## Проверка
- `gofmt`, `git diff --check`, `go test ./...`, `loopctl.py validate` — зелёные.
- Baseline: 290.77 s, 1 742 records. Range: 60.28 s, 1 764 records; ускорение 4.82×, −79.3% wall time.
- Повторные current-head range-runs: 55.25 s и 54.74 s.
- Сверка: 0 baseline records потеряно, 22 исходящих добавлено, 0 semantic mismatches на общих records после исключения mutable Telegram counters.
- Scope: 1 706 self/outgoing, 58 Trackmate, 0 other incoming; 0 FloodWait.
- Stage timing live run: Telegram 52.107 s, download/ffmpeg/Vosk 0 s на прогретом кэше, render 0.024 s, unaccounted 1.265 s, internal total 53.396 s, external wall 53.98 s.
- Live timing JSON содержит все пять stage fields; их сумма точно совпала с `accounted_seconds`.
- Range-scan сохранен: один range, 70 Telegram batches, 1 764 records, 0 FloodWait.

## Агенты
- `/root/range_scan_reviewer`: независимое ревью завершено, итог accepted без findings.

## Аудит Промптов
- Создается при изменении prompts.

## Пользовательские Дельты
- Отдельный user-deltas stream создается для существенных свежих корректировок, решений или изменений области.

## Риски И Блокеры
- Блокеров нет. Изменяемые Telegram counters могут отличаться между последовательными чтениями.
- Технический ASR JSONL намеренно может остаться частичным при interruption; пользовательские report JSONL/Markdown атомарны.
- Дневные ASR logs по-прежнему отражают текущий прогон; исторические performance figures теперь живут в отдельных immutable timing reports.

## Следующее Действие
- Нет обязательного следующего шага.

## Обновленные Источники Правды
- `requirements/source-map.md`
- `requirements/checklist.md`
- `plan/delivery-plan.md`
- `plan/current-step.md`
