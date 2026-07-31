# Производительность Telegram Harvest

## Daily catch-up range scan

`daily-catchup` читает весь новый диапазон одним последовательным Telegram range-scan и только после завершения разбивает записи по московским дням. Telegram RPC не параллелятся и используют статический code-owned pacing 500 ms. Для следующего автоматического непрерывного диапазона dialog checkpoint сравнивает полученные из `get_dialogs` heads с последним полностью опубликованным запуском. Неизменившийся dialog не вызывает history RPC только при `head_fully_verified=true`. Иначе он сканируется с `verified_message_id` как безопасным `MinID`; новый dialog сканируется полностью.

Разделение `top_message_id` и `verified_message_id` обязательно: catch-up за вчера запускается сегодня, поэтому `get_dialogs` уже может вернуть head сегодняшнего сообщения, не входящего во вчерашний отчет. Такой head сохраняется как наблюдаемый, но не считается полностью проверенным и не разрешает skip на следующем запуске.

Контрольный benchmark от 2026-07-30:

| Параметр | Дневной scan для каждого дня | Единый range-scan |
| --- | ---: | ---: |
| Диапазон | 2026-07-22—2026-07-29 | 2026-07-22—2026-07-29 |
| Дней | 8 | 8 |
| Wall time | 290.77 s | 60.28 s |
| Flood waits | 0 | 0 |
| Сообщений | 1 742 | 1 764 |

Фактическое ускорение — **4.82×**, wall time уменьшился на **79.3%**. Оба прогона использовали профиль `main`, одинаковые параметры, прогретый transcript cache и отдельные report directories.

Два повторных прогона range-scan на итоговом коде заняли 55.25 s и 54.74 s. Они подтвердили стабильный результат; для консервативного сравнения выше оставлен первый замер 60.28 s.

Range-scan сохранил все 1 742 пары `(chat_id, message_id)` из baseline и дополнительно нашёл 22 исходящих сообщения, пропущенных узкими дневными Telegram search-запросами. Старых записей, отсутствующих в новом результате, нет. После исключения изменяемых Telegram counters (`views`, `forwards`, `replies` и dialog summary fields) содержимое всех общих records совпало.

Проверка daily scope нового результата:

- self/outgoing: 1 706;
- Trackmate в Haru: 58;
- остальные incoming: 0.

## Stage timings

Каждый `daily` и `daily-catchup` измеряет стадии непосредственно во время работы и сохраняет уникальный JSON в `.state/daily/timings/`. Новый запуск не перезаписывает предыдущий timing report, даже если дневные `.state/daily/asr/YYYY-MM-DD.jsonl` обновились.

Границы стадий:

- `telegram_scan` — `get_dialogs`, разрешение target и последовательные history RPC вместе со штатным pacing; media transfer сюда не входит;
- `download` — фактические попытки скачать пользовательское или временное ASR-медиа вместе с ожиданием download RPC slot; cache hits сюда не входят;
- `ffmpeg` — подготовка WAV внутри transcriber, включая завершившиеся ошибкой попытки;
- `model_cold_start` — время запуска Whisper worker и ожидания готовности после загрузки модели;
- `asr` — только распознавание после готовности модели, включая завершившуюся ошибкой работу;
- `render` — запись и атомарная публикация дневных JSONL/Markdown плюс merged `00-latest-catchup.md`.

`audio_seconds` — суммарная длительность WAV только для успешно распознанных cache misses. `asr_speed_x` показывает отношение audio duration к суммарным ASR worker-seconds. `pipeline_speed_x` показывает throughput всего media pipeline: audio duration к реальному pipeline span. При прогретом transcript cache или отсутствии успешно обработанного аудио все три значения равны нулю.

После перекрытия Telegram download и локального ASR stage durations являются work-seconds, а не разложением wall time. `stage_work_seconds` может быть больше `total_seconds`. Объект `media_pipeline` поэтому отдельно сохраняет:

- реальный `span_seconds` от начала первого ASR job до завершения последнего;
- `overlap_seconds` с продолжающимся Telegram producer;
- queue capacity/peak, submitted/deduplicated/completed/failed jobs;
- один requested/activated GPU worker и реальный peak одновременно занятых jobs;
- backend/model/accelerator и фиксированный worker resource;
- доступную память, среднюю/пиковую общую CPU utilization и измеренный RSS ASR process;
- доступность GPU sampler: на macOS без elevated `powermetrics` значение явно помечается unavailable, а Metal подтверждается runtime evidence;
- startup, RSS, jobs, audio, ffmpeg/ASR/busy seconds и скорость каждого использованного worker;

Объект `dialog_checkpoint` сохраняет `enabled`, `fallback_reason`, общее число dialog, `history_rpc`, `unchanged`, `changed` и `new`. CLI печатает те же counters. Checkpoint не используется для `daily`, ручного `daily-catchup --from`, исторического или разорванного диапазона, при несовпадении account/scope и при невалидном state. Неполный/error run его не меняет.

Live safety-проверка 2026-07-30 на текущем диапазоне сравнила полный и checkpoint scan после v2 bootstrap:

| Проверка | Full | Checkpoint |
| --- | ---: | ---: |
| Records | 88 | 88 |
| Trackmate records | 6 | 6 |
| History dialogs | 12 | 12 |
| Batches | 17 | 17 |
| Unchanged dialogs skipped by checkpoint | 0 | 460 |

Потерянных `(chat_type, chat_id, message_id)` — 0. Расхождений общих JSONL records после удаления только живых dialog counters — 0. Все 12 dialog с сообщениями текущего дня были просканированы; checkpoint не принял уже наблюдавшиеся сегодняшние heads за покрытые вчерашним отчетом.

## History-only completeness

Daily не использует `messages.search` как источник полноты. Empty-query search с `from_id=self` воспроизводимо не вернул существующее исходящее сообщение `1221157785:415830`, хотя `messages.getHistory` его возвращает. Поэтому каждый реально изменившийся dialog читается через последовательный `getHistory`, а self/Trackmate scope фильтруется локально после нормализации.

Короткая history page сама по себе не считается доказанной границей диапазона. В обычном historical/first-run/fallback scan pagination продолжается до сообщения на `start`, пустой страницы или явного `max_batches`; неубывающий offset завершает scan ошибкой и не публикует checkpoint вместо ложного `complete=true`. Регрессионный тест воспроизводит две разреженные history pages и проверяет, что обе попадают в JSONL.

Для непрерывного валидного checkpoint есть более узкий proof: только первая страница bounded-запроса с `MinID > 0`, известным `top_message_id`, уникальными ID строго выше `MinID`, совпавшим page head, exact Telegram response (`inexact=false`, нулевой response offset) и числом сообщений меньше requested limit. `messages.messages` считается конечным constructor по его контракту. `inexact`, полный batch, head/offset/count anomaly и любой flow без валидного checkpoint продолжают старую безопасную pagination. Shadow live-run подтвердил все 4/4 кандидата пустой следующей страницей; enforced-run убрал четыре RPC и вернул побайтно тот же 41-record JSONL.

Timing report сохраняет `history_data_pages`, `history_empty_proof_pages`, `history_sparse_continuations`, proof candidates/stops/shadow results и причины отказа. `telegram_rpc` отдельно хранит статический spacing, число slots, scheduled pacing wait, операции и transport floods.

## Калибровка Telegram pacing

Калибровка выполнена на одном historical range 2026-07-25 без download/ASR: 471 dialog, 46 history dialog, 98 history batches, 103 последовательных RPC slots и 211 records. Каждый прогон возвращал complete JSONL; после удаления только живых dialog counters (`unread_count`, `top_message_id`, `last_message_at`, `views`) normalized SHA-256 всех вариантов одинаков: `e62281eb…6277`.

| Spacing | Wall | FloodWait | Результат |
| ---: | ---: | ---: | --- |
| 700 ms | 74.998 s | 0 | baseline |
| 600 ms | 66.503 s | 0 | clean |
| 550 ms | 61.058 s | 0 | clean |
| 500 ms | 57.336 s | 0 | clean |
| 450 ms | 52.928 s | 0 | первый clean |
| 400 ms | 53.827 s | 1 | нижняя граница нестабильна |
| 450 ms после FloodWait | 57.018 s | 1 | накопительный лимит всё ещё проявился |
| 500 ms после FloodWait, повтор 1 | 56.584 s | 0 | clean |
| 500 ms после FloodWait, повтор 2 | 57.615 s | 0 | clean |

Единый production floor для профилей `main` и `study` закреплён на 500 ms. Калибровка выполнялась на main-аккаунте: три 103-RPC прогона чистые, включая два сразу после нижних probes. Median 500-ms wall — 57.336 s против 74.998 s на 700 ms: ускорение `1.31×`, или `−23.6%` wall. 450 ms не выбран, несмотря на лучший одиночный результат, потому что повтор после накопленного burst получил FloodWait. По прямому решению пользователя тот же статический default применяется к `study`; отдельной профильной ветки pacing в конфигурации нет.

`total_seconds` — wall time измеряемой daily-операции. `stage_work_seconds` — сумма всех stage work-seconds; она намеренно не вычитается из wall, потому что Telegram, ffmpeg и ASR могут выполняться одновременно. CLI печатает основные поля:

```text
timings telegram_scan=...s download=...s ffmpeg=...s model_cold_start=...s asr=...s render=...s stage_work=...s audio=...s asr_speed=...x pipeline_speed=...x pipeline_mode=... pipeline_span=...s pipeline_overlap=...s pipeline_workers=... pipeline_queue_peak=... rpc_spacing_ms=... rpc_calls=... rpc_wait=...s history_data_pages=... history_empty_proof_pages=... checkpoint_proof_candidates=... checkpoint_proof_stops=... checkpoint_enabled=... total=...s report=.state/daily/timings/<run-id>-daily-catchup.json
```

Live-проверка 2026-07-30 на том же восьмидневном диапазоне и прогретом media/transcript cache:

| Стадия | Секунды |
| --- | ---: |
| Telegram scan | 52.107 |
| Download | 0.000 |
| ffmpeg | 0.000 |
| ASR | 0.000 |
| Render | 0.024 |
| Internal total | 53.396 |
| External wall (`time -p`) | 53.98 |

Нулевые download/ffmpeg/ASR в этом прогоне означают, что медиа и транскрипты были взяты из существующих файлов/кэша; это не реконструкция из перезаписанного ASR-лога. Run обработал 1 764 сообщения за один range-scan, 70 Telegram batches, 0 FloodWait.

## Bounded media pipeline

Daily использует один последовательный Telegram producer. Он сканирует сообщения, проверяет transcript cache и последовательно скачивает отсутствующее media. После download файл кладется в bounded queue ёмкостью два элемента; Telegram producer не ждёт локальную обработку, пока в очереди есть место. Один production Whisper Metal worker выполняет `ffmpeg → whole-file Silero gate → ASR` через один long-lived process/model. Collector ждёт все jobs, применяет результаты по transcript cache path и только затем сортирует и публикует отчеты.

Заполненная очередь создаёт backpressure и ограничивает число временных файлов. Одинаковый in-flight cache key получает один job. Transcript публикуется атомарно через temporary file, `fsync`, close и rename. Несколько Whisper workers и динамический controller удалены из production flow: измерения показали, что они конкурируют за общий GPU/unified memory, а после перекрытия локального ASR bottleneck снова становится последовательный Telegram stage.

Current-head cold-cache E2E для дня 2026-07-25: 211 records, 21 attachments, 3 ASR jobs, 170.284 seconds audio, 0 FloodWait, 85.888 s wall. ASR speed — `17.08×`, media pipeline speed — `13.39×`; один non-speech файл остановлен gate. Warm-cache запуск не стартует `whisper-server`, потому что transcript cache проверяется до download.

## Расширенный ASR benchmark на M4 Pro

Корпус собран read-only из реальных исходящих Telegram-медиа за 2026-07-22..29: 28 voice и 14 коротких video/round-video, всего 42 файла и 2178.413 s (36.3 min) аудио. В 30 файлах есть речь; 12 роликов без полезной речи служат отдельным hallucination-control. Corpus hash: `d79e32bb0e7f2d1e05c2c5ee90584ed04827ef4d5f2aa10a62a47f6a6bb24c1a`.

Performance ниже — одинаковый полный корпус, один long-lived process на вариант. Финальный профиль дополнительно повторяется три раза; остальные значения — один cold process после прогрева OS file cache.

| Backend / decode | ASR speed | Pipeline speed | Cold-start | Peak RSS | CPU | Missed speech | False text on 12 controls |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Vosk small RU CPU | 5.29× | 5.24× | 0.643 s | 1304 MiB | 98.5% | 2 | 0 |
| Whisper small q5_1 Metal, greedy | 57.50× | 52.41× | 0.154 s | 542 MiB | 20.8% | 0 | 12 |
| Whisper turbo q5_0 Metal, greedy | 27.07× | 25.85× | 0.253 s | 878 MiB | 8.1% | 0 | 12 |
| Whisper large-v3 q5_0 Metal, greedy | 11.71× | 11.46× | 0.436 s | 1853 MiB | 9.7% | 0 | 12 |
| **Whisper turbo q5_0 Metal, beam 5 + speech gate** | **13.98×** | **13.63×** | **0.278 s** | **927 MiB** | **7.1%** | **0** | **0** |

`100% process CPU` в `ps` означает примерно одно полностью занятое logical core, а не всю 12-core систему. Точный GPU utilization не снимался: macOS `powermetrics` требует elevated privileges. Вместо выдуманного процента отчет сохраняет `available=false` и runtime evidence; Metal варианты подтвердили Apple M4 Pro и `MTL : EMBED_LIBRARY = 1`.

### Сравнение содержания

Пословной человеческой разметки 36 минут аудио нет. Для одинакового относительного сравнения независимый full `large-v3 q5_0` используется как silver reference только для 30 speech-файлов. Поэтому его собственные 0% WER нельзя считать абсолютным качеством. Пунктуация и регистр нормализованы. Кроме WER/CER измеряются multiset word F1 и отдельный recall отрицаний/чисел, чтобы малое, но смысловое удаление не растворялось в общей метрике.

| Backend / decode | Relative WER | CER | Content F1 | Negation recall | Number recall |
| --- | ---: | ---: | ---: | ---: | ---: |
| Vosk small RU | 36.14% | 19.65% | 78.75% | 96.58% | 46.27% |
| Whisper small q5_1 greedy | 25.85% | 16.55% | 87.00% | 93.84% | 70.15% |
| Whisper turbo q5_0 greedy | 11.64% | 7.57% | 94.89% | 97.95% | 85.07% |
| **Whisper turbo q5_0 beam 5** | **10.58%** | **6.39%** | **94.61%** | **97.95%** | **94.03%** |
| Whisper large-v3 q5_0 silver reference | 0% | 0% | 100% | 100% | 100% |

Beam search выбран несмотря на снижение raw speed относительно greedy: он восстановил пропущенные окончания, технические фразы и числа; number recall вырос с 85.07% до 94.03%. Три полных повтора дали pipeline speed `15.64× / 13.63× / 12.90×` (median `13.63×`), то есть даже медленный повтор в 2.46 раза быстрее Vosk. Peak RSS примерно на 29% ниже Vosk. Full large-v3 почти вдвое тяжелее по памяти; ручная проверка также показала, что он не всегда лучше на редких русских/технических словах.

### Защита от галлюцинаций

Model-level `no_speech_threshold=0.4`, `suppress_nst` и отключение temperature fallback не решили non-speech: Whisper присваивал шуму очень низкий `no_speech_prob` и всё равно печатал «ПОДПИШИСЬ!», «Продолжение следует…» и subtitle credits. Отключение fallback дополнительно вызвало длинный repetition loop. Встроенный VAD, который режет WAV на сегменты, раньше удалял реальные части фраз. Поэтому production использует другую схему:

1. ffmpeg делает mono 16 kHz WAV.
2. Отдельный Silero gate (`threshold=0.5`, minimum speech `250 ms`) проверяет только наличие речи.
3. Если речь есть, весь исходный WAV без нарезки уходит в Whisper.
4. Точная известная boilerplate-фраза удаляется только если занимает отдельную последнюю строку; удаление остается в diagnostics.

В stable whisper.cpp v1.9.1 найден upstream CLI defect: `--vad-min-silence-duration-ms` ошибочно перезаписывает minimum speech. Runner намеренно передает minimum speech после minimum silence и защищен regression test. После workaround gate дал `0 missed / 0 hallucinations` на 30 speech + 12 controls.

Machine-readable приватные результаты лежат в `.state/asr-quality-20260731/` и не входят в git. `cmd/asr-benchmark` сохраняет corpus hash, полные transcripts, decoder/gate descriptor, confidence diagnostics, audio/ffmpeg/gate/ASR/wall, cold-start, RSS/CPU, WER/CER/content metrics и ошибки.

Финальный повтор после архитектурной чистки на последних пяти voice (262.473 s аудио) занял 17.473 s wall: ASR speed `15.70×`, pipeline speed `15.02×`, cold-start 0.253 s, peak RSS 923,385,856 bytes, mean process CPU 9.41%. Ошибок, пустых результатов и пропущенной речи — 0; все пять текстов побайтово совпали с предыдущим production-прогоном. Результат сохранён в `.state/asr-quality-20260731/results-polish-current.json`.

### Telegram completeness и live E2E

Первоначальный ASR live-run на `messages.search` вернул 210 records и пропустил `1221157785:415830`. После перехода на history-only тот же день без download/ASR вернул все 211 baseline keys, включая проблемное сообщение, при 0 FloodWait. Telegram scan вырос с 42.263 до 69.144 s, batches — с 56 до 92, полный wall — с 43.335 до 70.530 s. Это сознательная плата для холодного исторического запуска; автоматический последовательный catch-up по-прежнему использует dialog checkpoint и не читает неизменившиеся диалоги.

Финальный current-head E2E с изолированным state, download и production Whisper-профилем вернул 211 records, 21 attachment и 3 ASR jobs: 98 history batches, 0 FloodWait, 85.888 s total. Из них 2 jobs дали полезный transcript, а 1 non-speech job был остановлен speech gate. Обработано 170.284 s аудио: ASR speed `17.08×`, media pipeline speed `13.39×`. После исключения только живых Telegram counters, локальных cache paths и самих ASR-текстов все 211 общих JSONL records семантически совпали с baseline; missing/extra keys и semantic mismatches — 0.

## Как повторять benchmark

Baseline revision — `e810c83` (`feat: preserve forwarded message origins`). Чтобы не переключать рабочую ветку и не копировать credentials/session в другой worktree, старый код собирается отдельным бинарником, а оба бинарника запускаются из текущего project root. Так они используют один `.env`, session, state и transcript cache:

```bash
git worktree add --detach /tmp/telegram-harvest-old e810c83
(cd /tmp/telegram-harvest-old && go build -o /tmp/telegram-harvest-old-bin ./cmd/telegram-harvest)
go build -o /tmp/telegram-harvest-range-bin ./cmd/telegram-harvest

mkdir -p /tmp/telegram-harvest-baseline
/usr/bin/time -p /tmp/telegram-harvest-old-bin \
  --profile main \
  daily-catchup \
  --from 2026-07-22 \
  --report-dir /tmp/telegram-harvest-baseline

mkdir -p /tmp/telegram-harvest-baseline-jsonl
cp .state/daily/jsonl/2026-07-{22,23,24,25,26,27,28,29}.jsonl \
  /tmp/telegram-harvest-baseline-jsonl/

mkdir -p /tmp/telegram-harvest-range
/usr/bin/time -p /tmp/telegram-harvest-range-bin \
  --profile main \
  daily-catchup \
  --from 2026-07-22 \
  --report-dir /tmp/telegram-harvest-range
```

Оба запуска используют default flags: `dialog-limit=500`, `download-media=true`, `transcribe=true`, `transcribe-video=phone` и одинаковый прогретый `.state/daily/transcripts/cache`.

Структурная сверка начинается с идентичности сообщений:

```bash
for day in 22 23 24 25 26 27 28 29; do
  jq -r '[.chat.id,.message_id] | @tsv' \
    "/tmp/telegram-harvest-baseline-jsonl/2026-07-$day.jsonl" | sort > "/tmp/old-$day.keys"
  jq -r '[.chat.id,.message_id] | @tsv' \
    ".state/daily/jsonl/2026-07-$day.jsonl" | sort > "/tmp/new-$day.keys"
  comm -23 "/tmp/old-$day.keys" "/tmp/new-$day.keys"
done
```

Пустой вывод `comm -23` означает, что range-scan не потерял ни одной baseline-записи. Общие records дополнительно сравниваются после исключения изменяемых Telegram counters: dialog summary fields, `views`, `forwards` и `replies`. Эти counters могут изменяться между последовательными чтениями одного исторического сообщения и не являются расхождением range partition.
