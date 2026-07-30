# Текущий Шаг

Проект: telegram-harvest
Обновлено: 2026-07-30

## Активный Шаг
- id: `STEP-001`
- status: `готово`
- objective: Заменить отдельный Telegram scan каждого catch-up дня одним range-scan и доказать эквивалентность/ускорение.
- requirement IDs: `REQ-001`, `REQ-002`, `REQ-003`, `VAL-001`, `VAL-002`, `VAL-003`
- owned paths: `cmd/telegram-harvest/`, `internal/mtproto/`, `internal/harvest/`, `README.md`, `docs/`, `.project-loop/`
- validation: focused Go tests; `gofmt`; `go test ./...`; live old/new benchmark 2026-07-22—2026-07-29; JSONL/Markdown comparison; `loopctl.py validate`.
- done criteria: Один range collector обслуживает весь catch-up, результаты по дням эквивалентны baseline, все проверки зелёные, фактическое ускорение измерено.

## Фокус Ревью
- Нет скрытого повторного `loadDialogs`/chat scan на каждый день.
- Полуинтервалы `[start,end)` и московские дни не смешиваются.
- Existing/skipped reports не вызывают лишнюю media/ASR обработку.
- Ошибка range-scan не публикует неполные новые отчёты.
- Сохранены Trackmate/Haru scope, forward metadata и ASR cache behavior.

## Примечания
- Telegram RPC остаются последовательными; параллельный crawler не вводится.
- Итоговый live run: 54.74 s, 1 764 записи, 70 batches, 0 FloodWait; следующий активный шаг отсутствует.
