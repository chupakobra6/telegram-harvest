# Handoff

Проект: telegram-harvest
Обновлено: 2026-07-30

## Цель
- Ускорить `daily-catchup`: один раз прочитать весь Telegram-диапазон, затем разбить сообщения по московским дням без изменения daily-контракта.

## Текущий Шаг
- active step: `STEP-001`
- status: `готово`

## Завершено
- `REQ-001`: каждый catch-up диапазон обслуживается одним последовательным `DumpOutgoingRange`.
- `REQ-002`: сохранены daily scope, forwards, media/transcripts, дневные JSONL/Markdown и merged catch-up.
- `REQ-003`: старый и новый flow измерены на диапазоне 2026-07-22—2026-07-29.
- Ошибки/неполный range не публикуют новые пользовательские reports; ASR open/encode errors не теряются.

## Измененные Файлы
- `cmd/telegram-harvest/main.go`, `cmd/telegram-harvest/main_test.go`
- `internal/mtproto/client.go`, `internal/mtproto/client_test.go`
- `internal/harvest/model.go`, `internal/harvest/daily_view.go` и тесты
- `README.md`, `AGENTS.md`, `docs/catch-up.md`, `docs/performance.md`
- `.project-loop/`, `inbox/README.md`

## Проверка
- `gofmt`, `git diff --check`, `go test ./...`, `loopctl.py validate` — зелёные.
- Baseline: 290.77 s, 1 742 records. Range: 60.28 s, 1 764 records; ускорение 4.82×, −79.3% wall time.
- Повторные current-head range-runs: 55.25 s и 54.74 s.
- Сверка: 0 baseline records потеряно, 22 исходящих добавлено, 0 semantic mismatches на общих records после исключения mutable Telegram counters.
- Scope: 1 706 self/outgoing, 58 Trackmate, 0 other incoming; 0 FloodWait.

## Агенты
- `/root/range_scan_reviewer`: независимое ревью завершено, итог accepted без findings.

## Аудит Промптов
- Создается при изменении prompts.

## Пользовательские Дельты
- Отдельный user-deltas stream создается для существенных свежих корректировок, решений или изменений области.

## Риски И Блокеры
- Блокеров нет. Изменяемые Telegram counters могут отличаться между последовательными чтениями.
- Технический ASR JSONL намеренно может остаться частичным при interruption; пользовательские report JSONL/Markdown атомарны.

## Следующее Действие
- Нет обязательного следующего шага.

## Обновленные Источники Правды
- `requirements/source-map.md`
- `requirements/checklist.md`
- `plan/delivery-plan.md`
- `plan/current-step.md`
