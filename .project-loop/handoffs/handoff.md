# Handoff

Проект: telegram-harvest
Обновлено: 2026-07-30

## Цель
- Сохранить полный daily contract: общий ASR backend и history-only Telegram scan без воспроизводимых потерь `messages.search`.

## Текущий Шаг
- active step: `STEP-005`
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
- `REQ-010`: единый typed ASR contract реализован для Vosk, whisper.cpp и custom command; WAV upload в long-lived whisper-server потоковый.
- `REQ-011`: auto policy backend-specific — динамический CPU pool для Vosk и ровно один GPU worker для Whisper Metal/Core ML.
- `REQ-012`: Metal и Metal + Core ML собраны и проверяются по runtime evidence; model/quantization/accelerator/binary/decode config изолированы в transcript cache.
- `REQ-013`: developer benchmark сравнивает 6 вариантов на одном real corpus по performance, resources, quality и failure/empty/hallucination counts.
- `REQ-014`: daily `messages.search` удалён; все изменившиеся dialogs читаются через `getHistory`, sender scope фильтруется локально.

## Измененные Файлы
- `cmd/telegram-harvest/main.go`, `cmd/telegram-harvest/main_test.go`
- `internal/mtproto/client.go`, `internal/mtproto/client_test.go`
- `internal/harvest/model.go`, `internal/harvest/daily_view.go` и тесты
- `internal/stages/stages.go`, `internal/transcribe/transcribe.go` и тесты
- `cmd/telegram-harvest/stage_timings.go` и тесты
- `README.md`, `AGENTS.md`, `docs/catch-up.md`, `docs/performance.md`
- `.project-loop/`, `inbox/README.md`
- `internal/mtproto/media_pipeline.go` и тесты, `internal/stages/stages.go`
- `internal/transcribe/backend.go`, `internal/transcribe/whisper_server.go` и тесты
- `internal/asrbench/`, `cmd/asr-benchmark/`
- `internal/mtproto/client.go`, `internal/mtproto/client_test.go`

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
- ASR corpus: 3 Telegram media, 170.284 s, hash `ba03ca…16c4`; 6 variants × 3 fresh process runs.
- Current benchmark: Vosk 6.06×; small Metal 39.02×; small Core ML 37.69×; small q5_1 Metal 48.63×; turbo q5_0 Metal 29.37×; turbo + VAD 36.26×.
- Core ML small оказался на 4.7% медленнее plain Metal pipeline и потребовал примерно на 513 MiB больше peak RSS. VAD убрал non-speech hallucination, но порезал speech.
- Выбран рекомендуемый quality-first профиль: large-v3-turbo-q5_0 Metal, один worker, без VAD. Built-in default остаётся Vosk из-за внешней установки whisper.cpp/model.
- Финальный live daily: 54.383 s total, Telegram 41.851 s, ASR 5.793 s / 29.40×, pipeline 16.69×, 1 worker, 0 FloodWait.
- History-only no-ASR live на 2026-07-25 вернул 211/211 baseline keys, включая `1221157785:415830`: 92 batches, 70.530 s, 0 FloodWait.
- Full Whisper E2E вернул 211 records, 21 attachments, 3 transcripts: 81.083 s, 0 FloodWait, 0 missing/extra/semantic mismatches после штатной нормализации.
- Прежний no-ASR search path занимал 43.335 s и 56 batches. Дополнительные 27.195 s — измеренная цена полноты на холодной исторической дате; checkpoint сохраняется для последовательного catch-up.
- Добавлены tests для sparse history pages, false-complete при `max_batches`, non-advancing pagination и checkpoint Trackmate/self scope.

## Агенты
- `/root/range_scan_reviewer`: независимое ревью завершено, итог accepted без findings.

## Аудит Промптов
- Создается при изменении prompts.

## Пользовательские Дельты
- Отдельный user-deltas stream создается для существенных свежих корректировок, решений или изменений области.

## Риски И Блокеры
- Блокеров нет. Изменяемые Telegram counters могут отличаться между последовательными чтениями.
- Точный GPU utilization недоступен без elevated `powermetrics`; отчет честно хранит `available=false`, а Metal/Core ML activation подтверждается runtime logs.
- Silver reference создан turbo-моделью, поэтому WER/CER являются относительным сравнением, не абсолютной human-annotated оценкой.
- History-only безопаснее, но холодный исторический scan читает больше страниц. Это принятый trade-off; автоматический contiguous catch-up уменьшает его dialog checkpoint.
- Технический ASR JSONL намеренно может остаться частичным при interruption; пользовательские report JSONL/Markdown атомарны.
- Дневные ASR logs по-прежнему отражают текущий прогон; исторические performance figures теперь живут в отдельных immutable timing reports.

## Следующее Действие
- Шаг завершён. Следующее ускорение должно оптимизировать только доказанно безопасные history/checkpoint paths, не возвращая `messages.search`.

## Обновленные Источники Правды
- `requirements/source-map.md`
- `requirements/checklist.md`
- `plan/delivery-plan.md`
- `plan/current-step.md`
