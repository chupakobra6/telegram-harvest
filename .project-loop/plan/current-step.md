# Текущий Шаг

Проект: telegram-harvest
Обновлено: 2026-07-30

## Активный Шаг
- id: `STEP-005`
- status: `готово`
- objective: Удалить неполный `messages.search` из daily flow, перейти на history-only scan и доказать возврат всех 211 baseline messages.
- requirement IDs: `REQ-014`, `VAL-010`
- owned paths: `internal/mtproto/`, `README.md`, `docs/performance.md`, `AGENTS.md`, `.project-loop/`
- validation: focused boundary/MinID/scope tests; `gofmt`; `go test ./...`; `go vet ./...`; relevant `-race`; live daily 2026-07-25 без ASR; key/semantic comparison; `loopctl.py validate`.
- done criteria: daily не вызывает `messages.search`; history идёт до безопасно подтверждённой границы; `415830` восстановлено; 211/211 keys сохранены; performance trade-off задокументирован.

## Фокус Ревью
- `messages.search` полностью удалён из daily routing, а не оставлен fallback/alias.
- `getHistory` pagination не завершает scan только из-за короткой страницы.
- Self/Trackmate scope фильтруется после получения полной history page.
- Checkpoint `MinID` и sequential RPC/pacing сохранены.
- Полнота важнее прежнего historical wall time; цена измерена, а не скрыта.

## Примечания
- Telegram RPC остаются последовательными; параллельный crawler не вводится.
- STEP-001 завершён: итоговый live run 54.74 s, 1 764 записи, 70 batches, 0 FloodWait.
- STEP-002 live run: 53.396 s internal total, один range-scan, 70 batches, 1 764 records, timing JSON сохранен отдельно от ASR logs.
- STEP-003 cold benchmark: 94.22 s sequential → auto 54.95/55.61 s (1.69–1.71×); fixed 2 best 54.56 s; fixed 4 не дал выигрыша.
- Auto v1 поздно добавлял второго worker после длинного файла и занял 62.05 s. Controller исправлен: асинхронный bootstrap scale использует только queued backlog, prior до первого result и resource guards; два повторных auto runs подтвердили исправление.
- STEP-004 расширяет engine scope отдельным решением S006; Telegram producer и pacing остаются без изменений.
- STEP-004 завершён: общий backend/cache/policy contract, Vosk/Metal/Core ML runners и reproducible benchmark реализованы.
- Финальный corpus benchmark: 170.284 s, 6 variants × 3; рекомендуемый quality-first профиль — large-v3-turbo-q5_0 Metal, один worker, без VAD.
- Live daily: 54.383 s total, 29.40× ASR, 16.69× pipeline, 1 worker, 0 FloodWait.
- STEP-005 завершён: `messages.search` удалён из daily, проблемный день вернул 211/211 records.
- History-only no-ASR: 70.530 s, 92 batches, 0 FloodWait; прежний search path: 43.335 s, 56 batches.
- Full Whisper E2E: 211 records, 21 attachments, 3 transcripts, 81.083 s; 0 missing/extra/semantic mismatches.
