# Производительность Telegram Harvest

## Daily catch-up range scan

`daily-catchup` читает весь новый диапазон одним последовательным Telegram range-scan и только после завершения разбивает записи по московским дням. Telegram RPC не параллелятся и сохраняют штатный pacing 700 ms. Для следующего автоматического непрерывного диапазона dialog checkpoint сравнивает полученные из `get_dialogs` heads с последним полностью опубликованным запуском. Неизменившийся dialog не вызывает history RPC только при `head_fully_verified=true`. Иначе он сканируется с `verified_message_id` как безопасным `MinID`; новый dialog сканируется полностью.

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
- `model_cold_start` — суммарное время запуска использованных ASR workers и ожидания готовности после загрузки модели;
- `asr` — только распознавание после готовности модели, включая завершившуюся ошибкой работу;
- `render` — запись и атомарная публикация дневных JSONL/Markdown плюс merged `00-latest-catchup.md`.

`audio_seconds` — суммарная длительность WAV только для успешно распознанных cache misses. `asr_speed_x` показывает отношение audio duration к суммарным ASR worker-seconds. `pipeline_speed_x` показывает throughput всего media pipeline: audio duration к реальному pipeline span. При прогретом transcript cache или отсутствии успешно обработанного аудио все три значения равны нулю.

После появления параллелизма stage durations являются work-seconds, а не разложением wall time. `stage_work_seconds` может быть больше `total_seconds`. Объект `media_pipeline` поэтому отдельно сохраняет:

- реальный `span_seconds` от начала первого ASR job до завершения последнего;
- `overlap_seconds` с продолжающимся Telegram producer;
- queue capacity/peak, submitted/deduplicated/completed/failed jobs;
- requested cap, activated workers и реальный peak одновременно занятых workers;
- backend/model/accelerator и backend-specific worker resource/policy;
- доступную память, среднюю/пиковую общую CPU utilization и измеренный RSS ASR process;
- доступность GPU sampler: на macOS без elevated `powermetrics` значение явно помечается unavailable, а Metal/Core ML подтверждается runtime evidence;
- startup, RSS, jobs, audio, ffmpeg/ASR/busy seconds и скорость каждого использованного worker;
- каждое решение auto-controller `grow`/`hold` с backlog, ожидаемой экономией и причиной.

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

Короткая history page сама по себе также не считается доказанной границей диапазона. Pagination продолжается до сообщения на `start`, safe checkpoint `MinID`, пустой страницы или явного `max_batches`. Неубывающий offset завершает scan ошибкой и не публикует checkpoint вместо ложного `complete=true`. Регрессионный тест воспроизводит две разреженные history pages и проверяет, что обе попадают в JSONL.

`total_seconds` — wall time измеряемой daily-операции. `stage_work_seconds` — сумма всех stage work-seconds; она намеренно не вычитается из wall, потому что Telegram, ffmpeg и ASR могут выполняться одновременно. CLI печатает основные поля:

```text
timings telegram_scan=...s download=...s ffmpeg=...s model_cold_start=...s asr=...s render=...s stage_work=...s audio=...s asr_speed=...x pipeline_speed=...x pipeline_mode=... pipeline_span=...s pipeline_overlap=...s pipeline_workers=... pipeline_queue_peak=... checkpoint_enabled=... total=...s report=.state/daily/timings/<run-id>-daily-catchup.json
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

Daily использует один последовательный Telegram producer. Он сканирует сообщения, проверяет transcript cache и последовательно скачивает отсутствующее media. После download файл кладется в bounded queue; Telegram producer не ждет локальную обработку. Независимые workers выполняют `ffmpeg → ASR`, каждый через собственный process/model/session. Collector ждет все jobs, применяет результаты по transcript cache path и только затем сортирует и публикует отчеты.

Queue capacity равна `2 × configured worker cap`: 8 в `auto`, 2/4/6/8 в диагностических fixed modes. Заполненная очередь создает backpressure и ограничивает число временных файлов. Одинаковый in-flight cache key получает один job. Transcript публикуется атомарно через temporary file, `fsync`, close и rename.

Vosk CPU `auto` начинается с одного worker и может активировать до четырех. Scale-up выполняется асинхронно, чтобы resource sampling не задерживал Telegram producer. Controller требует:

- queued backlog сверх свободных workers и не меньше 30 секунд известного аудио;
- ожидаемую экономию больше cold-start с safety margin;
- общую CPU utilization не выше 80%;
- достаточно available memory для измеренного RSS дополнительной модели плюс 4 GiB системного резерва.

До первого готового job используется консервативный prior 4× ASR и 2 секунды startup; затем решение опирается на фактические audio/ASR/startup/RSS. Внутри запуска pool только растет: уже загруженная модель сохраняется до конца, чтобы не создавать дребезг.

whisper.cpp Metal/Core ML использует другую policy: `auto_max_workers=1`, `dynamic=false`. Явный fixed count остаётся диагностическим override, но штатный flow не запускает несколько процессов, конкурирующих за один GPU и unified memory.

Cold-cache benchmark 2026-07-30 для дня 2026-07-25:

| Режим | Wall | Pipeline span | Workers | Относительно sequential |
| --- | ---: | ---: | ---: | ---: |
| Старый последовательный flow (`021bbf7`) | 94.22 s | 39.26 s local work sequentially | 1 | 1.00× |
| Pipeline fixed 1 | 60.66 s | 40.32 s | 1 | 1.55× |
| Pipeline fixed 2 | 54.56 s | 33.94 s | 2 | 1.73× |
| Pipeline fixed 4 | 55.06 s | 36.30 s | 3 used / 4 activated | 1.71× |
| Pipeline auto, repeat 1 | 54.95 s | 35.67 s | 2 | 1.71× |
| Pipeline auto, repeat 2 | 55.61 s | 33.82 s | 2 | 1.69× |

Во всех cold runs: 210 records, 21 attachments, 3 successful ASR jobs, 170.284 seconds audio, 0 FloodWait. После удаления только живых Telegram dialog counters и локальных cache paths normalized JSONL совпал побайтово во всех режимах. Markdown совпал побайтово без нормализации. Временных source/WAV/transcript файлов после запусков не осталось.

Warm-cache повтор в `auto` занял 44.90 s: download/ffmpeg/model/ASR/pipeline span равны нулю, `pipeline_workers=0`, user CPU 0.05 s. Это подтверждает, что cache hit не запускает ASR process.

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
