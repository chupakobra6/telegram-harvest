# Текущий Шаг

Проект: telegram-harvest
Обновлено: 2026-07-31

## Активный Шаг
- id: `STEP-006`
- status: `готово`
- objective: Расширить реальный Telegram ASR benchmark, настроить quality/hallucination pipeline и закрепить один итоговый production-профиль.
- requirement IDs: `REQ-015`—`REQ-017`, `VAL-011`—`VAL-013`, `SCOPE-004`
- owned paths: `internal/transcribe/`, `internal/asrbench/`, `cmd/asr-benchmark/`, ASR config wiring/tests, `README.md`, `docs/performance.md`, `.env.example`, `.project-loop/`; приватный runtime corpus/results только под `.state/`.
- validation: corpus inventory/hash; focused decoder/cache/quality tests; одинаковый live corpus для Vosk/Whisper; speech/non-speech regression; performance repeats финалистов; `gofmt`; `go test ./...`; `go vet ./...`; relevant `-race`; current-head live daily; `git diff --check`; `loopctl.py validate`.
- done criteria: репрезентативный приватный корпус собран; quality evaluation не использует Turbo как собственный эталон; опасные настройки отклонены; один backend/profile заметно лучше Vosk по содержанию и закреплён как production default; полный validation зелёный.

## Фокус Ревью
- Корпус достаточно разнообразен и не состоит из пары удобных примеров.
- Quality comparison оценивает содержание, а не пунктуацию и не self-reference WER.
- Hallucination mitigation не удаляет речь и критичные слова вроде отрицаний.
- Все decode/VAD параметры входят в descriptor/cache identity.
- Итоговый default действительно один; экспериментальные профили не превращаются в параллельные production contracts.

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
