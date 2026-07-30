# Handoff

Проект: telegram-harvest
Обновлено: 2026-07-30

## Цель
- Ускорить cold-cache daily через bounded local media pipeline и безопасный auto-пул независимых Vosk workers, не меняя Telegram/data safety.

## Текущий Шаг
- active step: `STEP-003`
- status: `готово`

## Завершено
- `REQ-001`: каждый catch-up диапазон обслуживается одним последовательным `DumpOutgoingRange`.
- `REQ-002`: сохранены daily scope, forwards, media/transcripts, дневные JSONL/Markdown и merged catch-up.
- `REQ-003`: старый и новый flow измерены на диапазоне 2026-07-22—2026-07-29.
- Ошибки/неполный range не публикуют новые пользовательские reports; ASR open/encode errors не теряются.
- `REQ-004`: Telegram scan, download, ffmpeg, Vosk и render измеряются непосредственно у места выполнения, включая failed work.
- `REQ-005`: каждый daily run сохраняет уникальный атомарный `.state/daily/timings/<run-id>-<command>.json`; данные не зависят от перезаписываемых ASR logs.
- `REQ-006`: один последовательный Telegram producer перекрывается с bounded local `ffmpeg → Vosk` pipeline.
- `REQ-007`: `auto` стартует с одного logical worker и активирует до четырех только при queued backlog, выгоде выше startup и CPU/memory headroom; fixed `1..4` — diagnostic override.
- `REQ-008`: in-flight media dedup, atomic transcript cache и deterministic collector сохраняют content/order.
- `REQ-009`: timing report хранит work-seconds отдельно от pipeline span/overlap и per-worker startup/RSS/jobs/audio/speed.

## Измененные Файлы
- `cmd/telegram-harvest/main.go`, `cmd/telegram-harvest/main_test.go`
- `internal/mtproto/client.go`, `internal/mtproto/client_test.go`
- `internal/harvest/model.go`, `internal/harvest/daily_view.go` и тесты
- `internal/stages/stages.go`, `internal/transcribe/transcribe.go` и тесты
- `cmd/telegram-harvest/stage_timings.go` и тесты
- `README.md`, `AGENTS.md`, `docs/catch-up.md`, `docs/performance.md`
- `.project-loop/`, `inbox/README.md`
- `internal/mtproto/media_pipeline.go` и тесты, `internal/stages/stages.go`

## Проверка
- `gofmt`, `git diff --check`, `go test ./...`, `loopctl.py validate` — зелёные.
- Baseline: 290.77 s, 1 742 records. Range: 60.28 s, 1 764 records; ускорение 4.82×, −79.3% wall time.
- Повторные current-head range-runs: 55.25 s и 54.74 s.
- Сверка: 0 baseline records потеряно, 22 исходящих добавлено, 0 semantic mismatches на общих records после исключения mutable Telegram counters.
- Scope: 1 706 self/outgoing, 58 Trackmate, 0 other incoming; 0 FloodWait.
- До pipeline stage timing live run зафиксировал Telegram 52.107 s, download/ffmpeg/Vosk 0 s на прогретом кэше, render 0.024 s и wall 53.396 s.
- После pipeline timing JSON хранит все stage work fields, `stage_work_seconds`, wall total и отдельный `media_pipeline` span/overlap/resource object; invalid wall-minus-work arithmetic удалена.
- Range-scan сохранен: один range, 70 Telegram batches, 1 764 records, 0 FloodWait.
- Pipeline cold benchmark 2026-07-25: sequential 94.22 s; fixed1 60.66 s; fixed2 54.56 s; fixed4 55.06 s; auto 54.95 и 55.61 s.
- Во всех cold runs: 210 records, 21 attachments, 3 ASR jobs, 170.284 s audio, 0 FloodWait; normalized JSONL и raw Markdown идентичны.
- Warm cache: 44.90 s, download/ffmpeg/model/Vosk/span 0, workers peak 0, user CPU 0.05 s.
- `go test ./...`, `go vet ./...`, focused `-race`, `TestMediaPipeline` ×20, `git diff --check`, Project Loop validation — зелёные.

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
- Нет обязательного следующего шага; смена ASR engine и Telegram pacing остаются отдельными решениями.

## Обновленные Источники Правды
- `requirements/source-map.md`
- `requirements/checklist.md`
- `plan/delivery-plan.md`
- `plan/current-step.md`
