# Handoff

Проект: telegram-harvest
Обновлено: 2026-07-31

## Цель

- Выбрать один daily ASR-профиль по расширенному корпусу реальных исходящих Telegram voice.
- Улучшить сохранение содержания относительно Vosk и подавить Whisper hallucinations без нарезки/потери речи.

## Текущий Шаг

- active step: `STEP-006`
- status: `готово`

## Завершено

- Собран приватный ignored corpus: 28 voice Игоря, 2 weak-speech video и 12 non-speech controls; 2178.413 s, hash `d79e32bb0e7f2d1e05c2c5ee90584ed04827ef4d5f2aa10a62a47f6a6bb24c1a`.
- Voice покрывают 5.1–150.2 s, median 76.3 s; есть разговорная/техническая речь, числа, отрицания, английские термины, имена и разный фон.
- whisper.cpp обновлён в ignored runtime до stable v1.9.1; сравнивались Vosk, small q5_1, turbo q5_0, full large-v3 q5_0 и 10 decoder/gate вариантов.
- Benchmark хранит decode/gate descriptor, confidence diagnostics, speech-gate seconds и content precision/recall/F1, negation recall, number recall.
- Выбран один production profile: `large-v3-turbo-q5_0 + Metal + beam_size=5`, один GPU worker.
- Перед Whisper запускается whole-file Silero gate: threshold 0.5, minimum speech 250 ms, minimum silence 100 ms, pad 30 ms. Gate не режет и не объединяет WAV.
- Исправлен upstream defect whisper.cpp v1.9.1: min-silence CLI argument перезаписывает min-speech; runner передаёт min-speech после min-silence, regression test фиксирует порядок.
- Exact terminal filter удаляет только отдельную последнюю boilerplate-строку из проверенного набора и пишет удалённое в diagnostics. Совпадение внутри нормального предложения не трогается.
- Daily ASR log и timing report получили speech-gate timings, confidence/gate decision и removed hallucinations.
- Daily CLI/config/docs используют production profile; локальный `.env` переключён на Whisper turbo Metal + Silero.

## Результаты

### Качество

Независимый full large-v3 q5_0 использован как silver reference только для 30 speech samples; это относительная, не human-ground-truth оценка.

| Вариант | WER | CER | Content F1 | Negation recall | Number recall |
| --- | ---: | ---: | ---: | ---: | ---: |
| Vosk small RU | 36.14% | 19.65% | 78.75% | 96.58% | 46.27% |
| Whisper small q5_1 greedy | 25.85% | 16.55% | 87.00% | 93.84% | 70.15% |
| Whisper turbo q5_0 greedy | 11.64% | 7.57% | 94.89% | 97.95% | 85.07% |
| **Whisper turbo q5_0 beam 5** | **10.58%** | **6.39%** | **94.61%** | **97.95%** | **94.03%** |

Beam восстановил окончания, технические фразы и числа, которые greedy пропускал. Model no-speech threshold, `suppress_nst` и отключение fallback не остановили non-speech hallucinations; no-fallback дал repetition loop.

### Производительность

- Final full corpus ×3: pipeline `15.64× / 13.63× / 12.90×`, median `13.63×`; ASR median `13.98×`.
- Peak RSS 927 MiB; mean process CPU 7.1%; cold-start median 0.278 s.
- 0/30 missed speech, 0/12 non-speech false transcripts.
- Два terminal boilerplate в speech-файлах удалены с diagnostics: `DimaTorzok`, `Продолжение следует`.

### Live current-head

- Isolated state: `/tmp/telegram-harvest-e2e-final.YMBawV`; пользовательские reports/latest не затронуты.
- 2026-07-25: 211 records, 21 attachments, 2 полезных transcript и 1 gated non-speech, 98 history batches, 0 FloodWait, complete.
- Wall 85.888 s; Telegram 72.342 s; audio 170.284 s; ASR 9.968 s / 17.08×; overlapping pipeline 13.39×; один GPU worker.
- После удаления только mutable Telegram counters, local paths и ASR text normalized JSONL SHA-256 совпал с baseline: `25d5dd9f8a364efb11e1b0b1073b10e4e7417722df603aa370b4ac7de4dd6019`.
- Live ASR log подтвердил gate true/false и удаление `Продолжение следует...`.

## Проверка

- `go test ./...` — зелёный.
- `go vet ./...` — зелёный.
- `go test -race ./internal/transcribe ./internal/asrbench ./internal/mtproto ./cmd/telegram-harvest` — зелёный.
- `go test ./internal/mtproto -run TestMediaPipeline -count=20` — зелёный.
- `git diff --check` — зелёный.
- Project Loop validation — зелёный.

## Риски И Ограничения

- Silver reference не заменяет human transcription; full large-v3 иногда сам ошибается в редких словах.
- Точный GPU utilization недоступен без elevated `powermetrics`; Metal activation подтверждена runtime evidence.
- Terminal filter намеренно короткий и точный; он не пытается угадывать произвольные hallucinations.
- Vosk остаётся CPU fallback/backend для benchmark, но active local daily profile один — выбранный Whisper.
- Private audio/transcripts/results остаются только в ignored `.state`; во внешние API ничего не отправлялось.

## Следующее Действие

- Шаг завершён. Расширять terminal filter или менять decoder/gate только после новых повторяемых ошибок на сохранённом локальном corpus.

## Обновленные Источники Правды

- `requirements/source-map.md`
- `requirements/checklist.md`
- `plan/delivery-plan.md`
- `plan/current-step.md`
- `README.md`
- `docs/performance.md`
