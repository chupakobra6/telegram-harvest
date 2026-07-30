# Текущий Шаг

Проект: telegram-harvest
Обновлено: 2026-07-30

## Активный Шаг
- id: `STEP-003`
- status: `готово`
- objective: Реализовать безопасный bounded media pipeline и консервативный auto-пул независимых Vosk workers без изменения Telegram read/pacing flow и пользовательского daily интерфейса.
- requirement IDs: `REQ-006`, `REQ-007`, `REQ-008`, `REQ-009`, `SCOPE-002`
- owned paths: `cmd/telegram-harvest/`, `internal/mtproto/`, `internal/transcribe/`, `internal/harvest/`, `README.md`, `docs/`, `.project-loop/`
- validation: focused pipeline/controller/cache/timing tests; race tests; `gofmt`; `go test ./...`; structural sequential/pipeline equivalence; cold-cache sequential/1/2/4/auto benchmark; live daily-catchup; `loopctl.py validate`.
- done criteria: Telegram RPC/download последовательны; очередь bounded; локальный ASR перекрывается с producer; auto выбирает workers по измеренным ресурсам/выгоде; reports детерминированы; timing JSON честно отражает overlap и worker metrics.

## Фокус Ревью
- Ни один worker не владеет MTProto client; Telegram RPC и downloads остаются в producer.
- Очередь ограничена, cancellation/error не оставляют goroutine/process/temp-file leaks.
- Один media key не получает два ASR job; cache publish атомарен.
- Результаты присоединяются к исходным attachments и рендерятся в прежнем порядке.
- Каждый worker имеет отдельную Vosk session; auto не масштабируется без измеримой ожидаемой выгоды.
- Worker-seconds и overlapping stages не складываются как последовательная wall decomposition.

## Примечания
- Telegram RPC остаются последовательными; параллельный crawler не вводится.
- STEP-001 завершён: итоговый live run 54.74 s, 1 764 записи, 70 batches, 0 FloodWait.
- STEP-002 live run: 53.396 s internal total, один range-scan, 70 batches, 1 764 records, timing JSON сохранен отдельно от ASR logs.
- STEP-003 cold benchmark: 94.22 s sequential → auto 54.95/55.61 s (1.69–1.71×); fixed 2 best 54.56 s; fixed 4 не дал выигрыша.
- Auto v1 поздно добавлял второго worker после длинного файла и занял 62.05 s. Controller исправлен: асинхронный bootstrap scale использует только queued backlog, prior до первого result и resource guards; два повторных auto runs подтвердили исправление.
