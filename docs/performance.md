# Производительность Telegram Harvest

## Daily catch-up range scan

`daily-catchup` читает весь новый диапазон одним последовательным Telegram range-scan и только после завершения разбивает записи по московским дням. Telegram RPC не параллелятся и сохраняют штатный pacing 700 ms. Для следующего автоматического непрерывного диапазона dialog checkpoint сравнивает полученные из `get_dialogs` heads с последним полностью опубликованным запуском. Неизменившийся dialog не вызывает history/search RPC только при `head_fully_verified=true`. Иначе он сканируется с `verified_message_id` как безопасным `MinID`; новый dialog сканируется полностью.

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

- `telegram_scan` — `get_dialogs`, разрешение target и последовательные history/search RPC вместе со штатным pacing; media transfer сюда не входит;
- `download` — фактические попытки скачать пользовательское или временное ASR-медиа вместе с ожиданием download RPC slot; cache hits сюда не входят;
- `ffmpeg` — подготовка WAV внутри transcriber, включая завершившиеся ошибкой попытки;
- `model_cold_start` — суммарное время запуска использованных Vosk session workers и ожидания их `ready` после загрузки модели и grammar;
- `vosk` — только распознавание после готовности модели, включая завершившуюся ошибкой работу; при custom non-Vosk command поле честно остается нулевым;
- `render` — запись и атомарная публикация дневных JSONL/Markdown плюс merged `00-latest-catchup.md`.

`audio_seconds` — суммарная длительность WAV только для успешно распознанных cache misses. `asr_speed_x` показывает отношение audio duration к суммарным Vosk worker-seconds. `pipeline_speed_x` показывает throughput всего media pipeline: audio duration к реальному pipeline span. При прогретом transcript cache или отсутствии успешно обработанного аудио все три значения равны нулю.

После появления параллелизма stage durations являются work-seconds, а не разложением wall time. `stage_work_seconds` может быть больше `total_seconds`. Объект `media_pipeline` поэтому отдельно сохраняет:

- реальный `span_seconds` от начала первого ASR job до завершения последнего;
- `overlap_seconds` с продолжающимся Telegram producer;
- queue capacity/peak, submitted/deduplicated/completed/failed jobs;
- requested cap, activated workers и реальный peak одновременно занятых workers;
- доступную память, общую CPU utilization и измеренный RSS Vosk process;
- startup, RSS, jobs, audio, ffmpeg/Vosk/busy seconds и скорость каждого использованного worker;
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

## Search pagination completeness

`messages.search` не гарантирует, что короткая страница (`len(messages) < requested_limit`) является последней. Daily outgoing search поэтому продолжает последовательную пагинацию до пустой страницы или безопасной границы `MinID`; короткая страница завершает scan только для `getHistory`. Если Telegram возвращает неубывающий offset, scan завершается ошибкой и не публикует checkpoint вместо ложного `complete=true`.

Регрессионный тест воспроизводит две разреженные search-страницы и проверяет, что сообщения со второй страницы попадают в итоговый JSONL.

`total_seconds` — wall time измеряемой daily-операции. `stage_work_seconds` — сумма всех stage work-seconds; она намеренно не вычитается из wall, потому что Telegram, ffmpeg и Vosk могут выполняться одновременно. CLI печатает основные поля:

```text
timings telegram_scan=...s download=...s ffmpeg=...s model_cold_start=...s vosk=...s render=...s stage_work=...s audio=...s asr_speed=...x pipeline_speed=...x pipeline_mode=... pipeline_span=...s pipeline_overlap=...s pipeline_workers=... pipeline_queue_peak=... checkpoint_enabled=... total=...s report=.state/daily/timings/<run-id>-daily-catchup.json
```

Live-проверка 2026-07-30 на том же восьмидневном диапазоне и прогретом media/transcript cache:

| Стадия | Секунды |
| --- | ---: |
| Telegram scan | 52.107 |
| Download | 0.000 |
| ffmpeg | 0.000 |
| Vosk | 0.000 |
| Render | 0.024 |
| Internal total | 53.396 |
| External wall (`time -p`) | 53.98 |

Нулевые download/ffmpeg/Vosk в этом прогоне означают, что медиа и транскрипты были взяты из существующих файлов/кэша; это не реконструкция из перезаписанного ASR-лога. Run обработал 1 764 сообщения за один range-scan, 70 Telegram batches, 0 FloodWait.

## Bounded media pipeline

Daily использует один последовательный Telegram producer. Он сканирует сообщения, проверяет transcript cache и последовательно скачивает отсутствующее media. После download файл кладется в bounded queue; Telegram producer не ждет локальную обработку. Независимые workers выполняют `ffmpeg → Vosk`, каждый через собственный process/model/session. Collector ждет все jobs, применяет результаты по transcript cache path и только затем сортирует и публикует отчеты.

Queue capacity равна `2 × configured worker cap`: 8 в `auto`, 2/4/6/8 в диагностических fixed modes. Заполненная очередь создает backpressure и ограничивает число временных файлов. Одинаковый in-flight cache key получает один job. Transcript публикуется атомарно через temporary file, `fsync`, close и rename.

`auto` начинается с одного worker и может активировать до четырех. Scale-up выполняется асинхронно, чтобы resource sampling не задерживал Telegram producer. Controller требует:

- queued backlog сверх свободных workers и не меньше 30 секунд известного аудио;
- ожидаемую экономию больше cold-start с safety margin;
- общую CPU utilization не выше 80%;
- достаточно available memory для измеренного RSS дополнительной модели плюс 4 GiB системного резерва.

До первого готового job используется консервативный prior 4× ASR и 2 секунды startup; затем решение опирается на фактические audio/Vosk/startup/RSS. Внутри запуска pool только растет: уже загруженная модель сохраняется до конца, чтобы не создавать дребезг.

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

Warm-cache повтор в `auto` занял 44.90 s: download/ffmpeg/model/Vosk/pipeline span равны нулю, `pipeline_workers=0`, user CPU 0.05 s. Это подтверждает, что cache hit не запускает Vosk process.

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
