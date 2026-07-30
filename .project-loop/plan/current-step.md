# Текущий Шаг

Проект: telegram-harvest
Обновлено: 2026-07-30

## Активный Шаг
- id: `STEP-002`
- status: `готово`
- objective: Добавить честные stage timings и отдельный неизменяемый timing report для каждого daily запуска.
- requirement IDs: `REQ-004`, `REQ-005`, `VAL-004`
- owned paths: `cmd/telegram-harvest/`, `internal/mtproto/`, `internal/transcribe/`, `internal/harvest/`, `README.md`, `docs/`, `.project-loop/`
- validation: focused timing tests; `gofmt`; `go test ./...`; live `daily-catchup`; JSON timing report inspection; подтверждение одного `DumpOutgoingRange`; `loopctl.py validate`.
- done criteria: Пять стадий измеряются напрямую, wall/unaccounted прозрачен, report уникален и атомарен, range-scan остаётся единым.

## Фокус Ревью
- Telegram scan не включает media download/ffmpeg/Vosk.
- Download включает фактические попытки и pacing, в том числе error path.
- ffmpeg/Vosk учитываются непосредственно внутри runner, включая failed work и первую загрузку модели.
- Render включает дневные JSONL/Markdown и merged catch-up.
- Новый timing report не перезаписывает предыдущий и не зависит от ASR JSONL.
- Нет скрытого повторного `loadDialogs`/chat scan на каждый день.

## Примечания
- Telegram RPC остаются последовательными; параллельный crawler не вводится.
- STEP-001 завершён: итоговый live run 54.74 s, 1 764 записи, 70 batches, 0 FloodWait.
- STEP-002 live run: 53.396 s internal total, один range-scan, 70 batches, 1 764 records, timing JSON сохранен отдельно от ASR logs.
