# Текущий Шаг

Проект: telegram-harvest
Обновлено: 2026-07-30

## Активный Шаг
- id: `STEP-004`
- status: `готово`
- objective: Реализовать общий ASR backend с Vosk CPU и whisper.cpp Metal/Core ML, backend-specific worker policy и доказательно выбрать конфигурацию по скорости, ресурсам и качеству русского текста.
- requirement IDs: `REQ-010`, `REQ-011`, `REQ-012`, `REQ-013`, `VAL-007`, `VAL-008`, `VAL-009`, `SCOPE-003`
- owned paths: `cmd/telegram-harvest/`, `internal/mtproto/`, `internal/transcribe/`, `internal/harvest/`, `README.md`, `docs/`, `.project-loop/`
- validation: backend/cache/policy/helper tests; race/failure tests; `gofmt`; `go test ./...`; одинаковый real corpus для Vosk/Metal/Core ML; cold/steady repetitions; WER/CER и empty/error analysis; live daily-catchup; `loopctl.py validate`.
- done criteria: единый typed backend contract; whisper.cpp действительно использует заявленный accelerator; auto policy не конкурирует GPU workers; cache изолирован по engine/model/config; benchmark воспроизводим; выбранный вариант сохраняет daily output contract.

## Фокус Ревью
- Pipeline зависит только от общего ASR contract, а не от Vosk/Whisper subprocess details.
- Cache fingerprint меняется при backend/model/quantization/accelerator/decode settings.
- Vosk auto использует CPU/memory evidence; Whisper auto ограничен одним GPU worker.
- Metal/Core ML activation подтверждается runtime evidence, а отсутствие доступного GPU sampler явно отмечается.
- Benchmark не включает Telegram variability в ASR comparison и использует один corpus hash/reference.
- Worker-seconds и overlapping stages не складываются как последовательная wall decomposition.

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
