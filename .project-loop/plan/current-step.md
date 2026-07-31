# Текущий Шаг

Проект: telegram-harvest
Обновлено: 2026-07-31

## Активный Шаг
- id: `STEP-007`
- status: `готово`
- objective: Доказанно убрать лишний пустой checkpoint history RPC и выбрать статический безопасный Telegram RPC floor без потери сообщений.
- requirement IDs: `REQ-018`—`REQ-020`, `VAL-014`—`VAL-015`, `CON-001`, `SCOPE-005`
- owned paths: `internal/mtproto/`, `internal/harvest/` timing/stat contracts, `internal/config/`, `cmd/telegram-harvest/` timing wiring/tests, `README.md`, `docs/performance.md`, `.project-loop/`; приватные live artifacts только под `.state/` или `/tmp`.
- validation: fake Telegram response/fake clock tests; exact/inexact/sparse/full-page fallback coverage; `gofmt`; focused tests; `go test ./...`; `go vet ./...`; relevant `-race`; live 700/600/550/500/450 ms calibration in isolated state; structural JSONL comparison; final current-head catch-up; `git diff --check`; `loopctl.py validate`.
- done criteria: метрики различают data/proof/sparse pages; optimized stop включается только при формальном proof и не меняет records; сомнительные/fallback flows сохраняют полный scan; выбран один static code-owned spacing с 0 FloodWait/errors и запасом; интегрированный catch-up не имеет missing/extra/semantic mismatches.

## Фокус Ревью
- Оптимизация не принимает короткую страницу как proof без достаточной Telegram metadata.
- `MinID`, `top_message_id`, exact/inexact ответы и sparse pages не создают пропусков.
- Historical/gap/first-run/account/scope/error paths остаются безопасными.
- RPC по-прежнему последовательны; production spacing статический и code-owned.
- Live сравнение отделяет pacing эффект от media cache/ASR и проверяет реальные ключи/поля JSONL.

## Примечания
- Telegram RPC остаются последовательными; параллельный crawler не вводится.
- STEP-001 завершён: итоговый live run 54.74 s, 1 764 записи, 70 batches, 0 FloodWait.
- STEP-002 live run: 53.396 s internal total, один range-scan, 70 batches, 1 764 records, timing JSON сохранен отдельно от ASR logs.
- STEP-003 cold benchmark: 94.22 s sequential → auto 54.95/55.61 s (1.69–1.71×); fixed 2 best 54.56 s; fixed 4 не дал выигрыша.
- Auto v1 поздно добавлял второго worker после длинного файла и занял 62.05 s. Controller исправлен: асинхронный bootstrap scale использует только queued backlog, prior до первого result и resource guards; два повторных auto runs подтвердили исправление.
- STEP-004 расширяет engine scope отдельным решением S006; Telegram producer и pacing остаются без изменений.
- STEP-004 завершён: общий backend/cache/policy contract, Vosk/Metal/Core ML runners и reproducible benchmark реализованы.
- Расширенный corpus: 42 media / 2178.413 s / hash `d79e32…24c1`; 30 speech и 12 non-speech controls.
- Итоговый production profile: large-v3-turbo q5_0 Metal, beam 5, один GPU worker, whole-file Silero 0.5/250 ms и exact terminal filter.
- Relative large-v3-silver quality: WER 10.58%, CER 6.39%, content F1 94.61%, negation recall 97.95%, number recall 94.03%; Vosk WER 36.14%, F1 78.75%, number recall 46.27%.
- Три final runs: pipeline 15.64× / 13.63× / 12.90×; median 13.63×; 0 missed speech, 0/12 control hallucinations.
- Current-head isolated live daily 2026-07-25: 211 records, 21 attachments, 2 useful transcripts + 1 gated non-speech, 85.888 s, 13.39× pipeline, 1 GPU worker, 0 FloodWait; normalized JSONL hash совпал.
- STEP-005 завершён: `messages.search` удалён из daily, проблемный день вернул 211/211 records.
- History-only no-ASR: 70.530 s, 92 batches, 0 FloodWait; прежний search path: 43.335 s, 56 batches.
- Full Whisper E2E: 211 records, 21 attachments, 3 transcripts, 81.083 s; 0 missing/extra/semantic mismatches.
- STEP-006 явно разрешён текущей инструкцией Игоря как непрерывный цикл до выбора одного итогового варианта.
- STEP-006 завершён; блокеров нет.
- STEP-007 live shadow: 4/4 proof candidates подтверждены пустой следующей страницей, 0 rejected; enforced сократил 21→17 history batches и вернул побайтно тот же 45-record JSONL.
- Main pacing matrix на 211-record/103-RPC historical run: 700 ms 74.998 s; 600 ms 66.503 s; 550 ms 61.058 s; 500 ms median 57.336 s; 400 ms и повторный 450 ms дали FloodWait.
- Production: main/daily 500 ms, study остаётся 700 ms без непроверенного расширения. Integrated CLI: 211 records, 98 history batches, 0 FloodWait, 56.783 s; normalized SHA совпал с 700-ms baseline.
- Full, vet, race, diff и Project Loop validation зелёные; временный harness и `/tmp` evidence удалены.
