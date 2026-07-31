# Telegram Harvest

Локальный read-only CLI для сбора Telegram-данных через MTProto user authorization.
Проект рассчитан на два практических сценария:

- **daily reports** - личные исходящие сообщения и настроенные chat-scoped источники за день, Markdown-отчеты в `reports/daily`, локальная транскрибация voice/audio/round-video и коротких вертикальных phone-like video через выбранный ASR backend;
- **study harvest** - выгрузка, синк и агентские Markdown-представления для учебных чатов из allowlist.

CLI один и тот же для всех сценариев. Аккаунт выбирается профилем `main` или `study`, а не отдельными account-specific командами.

## Что умеет

| Область | Поведение |
| --- | --- |
| Авторизация | MTProto user session через `login` и явные API credentials для каждого профиля. |
| Профили | `main` читает `TG_HARVEST_DAILY_*`; `study` читает `TG_HARVEST_STUDY_*`. Других алиасов профилей/env нет. |
| Daily | Сканирует диалоги за один московский день и пишет outgoing/self сообщения плюс настроенных отправителей в конкретных чатах. |
| Отчеты | Пользовательские daily-отчеты лежат в `reports/daily/YYYY-MM-DD.md`; JSONL и кэши остаются в `.state/`. |
| Медиа | Картинки сохраняются локально, audio/video временно скачиваются для ASR и удаляются после транскрибации; generic video проходит phone-like preflight. |
| ASR | Общий backend contract поддерживает Vosk CPU и долгоживущий whisper.cpp server с Metal или Metal + Core ML; worker policy выбирается по типу ресурса. |
| Study sync | `dump`/`sync` читают только allowlisted-чаты, поддерживают resumable backfill и производят JSONL. |
| Agent view | `agent-view` и `compact` строят компактные Markdown/TOON-представления из JSONL. |
| Safety | Инструмент не отправляет сообщения и не мутирует Telegram-состояние. RPC идут последовательно и с pacing. |

## Быстрый старт

```bash
cd telegram-harvest
cp .env.example .env
make setup
make test
```

Заполнить `.env`:

```dotenv
TG_HARVEST_DAILY_APP_ID=12345678
TG_HARVEST_DAILY_APP_HASH=main_account_app_hash
# Опционально: если не задано, `login` спросит номер интерактивно.
# TG_HARVEST_DAILY_PHONE=+10000000000
# Опционально: дополнительные источники daily как chat_id:sender_id.
# TG_HARVEST_DAILY_ADDITIONAL_SENDERS=3740223926:8718303786

# Учебный аккаунт:
TG_HARVEST_STUDY_APP_ID=12345678
TG_HARVEST_STUDY_APP_HASH=study_account_app_hash
# TG_HARVEST_STUDY_PHONE=+10000000000
TG_HARVEST_STUDY_ALLOWED_CHATS=1234567890,@study_chat
```

Telegram app credentials создаются на <https://my.telegram.org>. Секреты, сессии, `.state/`, модели и отчеты игнорируются git.

Логин основного аккаунта:

```bash
make login PROFILE=main
make doctor PROFILE=main
go run ./cmd/telegram-harvest --profile main me
```

Логин учебного аккаунта через API credentials:

```bash
make login PROFILE=study
make doctor PROFILE=study
go run ./cmd/telegram-harvest --profile study me
```

## Профили

Профиль всегда указывается явно. CLI не выбирает аккаунт по команде и не имеет дефолтного аккаунта.

```bash
go run ./cmd/telegram-harvest --profile main  <command>
go run ./cmd/telegram-harvest --profile study <command>
```

Makefile повторяет эту модель: команды, которые читают профиль, требуют `PROFILE=main|study`.

```bash
make doctor PROFILE=main
make doctor PROFILE=study
make daily PROFILE=main DATE=2026-06-04
make daily-catchup PROFILE=main
make sync CHAT=1234567890 NAME=study-main PROFILE=study
```

## Daily reports

Один день:

```bash
make daily PROFILE=main DATE=yesterday
make daily PROFILE=main DATE=2026-06-04
```

То же напрямую через CLI:

```bash
go run ./cmd/telegram-harvest --profile main daily --date 2026-06-04
```

Выходные файлы по умолчанию:

```text
reports/daily/00-latest-catchup.md
reports/daily/YYYY-MM-DD.md
.state/daily/jsonl/YYYY-MM-DD.jsonl
.state/daily/asr/YYYY-MM-DD.jsonl
.state/daily/timings/<run-id>-daily-catchup.json
.state/daily/media/...
.state/daily/transcripts/cache/...
```

Markdown в `reports/daily` - основной человекочитаемый результат. Он содержит время, чат назначения, текст сообщения, Markdown-ссылку на Telegram-сообщение когда она доступна, короткие сведения о вложениях и транскрипты без технических путей, размеров, cache-полей, ASR/ffmpeg ошибок и runtime-сводки вроде периода или числа просканированных диалогов. Пересланные сообщения явно помечаются как `Переслано из`/`Переслано от`; когда Telegram раскрывает исходный канал и message id, название источника ведет на оригинальный пост.

`daily-catchup` после успешной публикации всех дней атомарно пересобирает `reports/daily/00-latest-catchup.md`. Это один переносимый Markdown с полным диапазоном последнего catch-up: дневные заголовки и содержимое идут хронологически, поэтому файл можно сразу передать в другой чат. Отдельные `YYYY-MM-DD.md` остаются источниками и удобной навигацией по дням.

По умолчанию daily оставляет только outgoing/self. `TG_HARVEST_DAILY_ADDITIONAL_SENDERS` может добавить выбранных отправителей строго внутри выбранных чатов. Для пары `3740223926:8718303786` сообщения Trackmate из Haru попадают в ту же хронологию и помечаются именем отправителя; сообщения остальных участников Haru отфильтровываются.

JSONL в `.state/daily/jsonl` - технический audit/source layer. Он хранит raw-поля вроде `media_id`, `local_path`, `transcript_path`, `download_hint`, а для пересланных сообщений — структурированный `forward` с доступным источником, оригинальной датой, message id и ссылкой. Этот слой нужен для отладки, пересборки и анализа, но не является пользовательским отчетом.

ASR JSONL в `.state/daily/asr` - подробный машинный лог транскрибации текущего прогона: cache hits, skip reasons, download/ffmpeg/ASR timings, размер, длительность, разрешение, backend и real-time factor. Дневной файл перезаписывается следующим прогоном этой даты и может остаться частичным после interruption.

Каждый `daily`/`daily-catchup` дополнительно атомарно сохраняет отдельный неизменяемый JSON в `.state/daily/timings/` и печатает его путь. В нем напрямую измерены worker-seconds `telegram_scan`, `download`, `ffmpeg`, `model_cold_start`, `asr`, `render`, полный wall time и `stage_work_seconds`. Объект `media_pipeline` отдельно хранит backend/model/accelerator, speech-gate seconds, span/overlap, queue peak, jobs/dedup/failures, requested/activated/peak workers, startup/RSS/ASR speed, CPU evidence и решения auto-controller. ASR JSONL дополнительно сохраняет решение gate, confidence signals и удаленные terminal hallucinations. Worker-seconds могут быть больше wall time: локальные стадии перекрываются с Telegram и между собой.

Daily публикует финальные Markdown/JSONL отчеты атомарно: если день не собран до `complete=true`, файлы `reports/daily/YYYY-MM-DD.md` и `.state/daily/jsonl/YYYY-MM-DD.jsonl` не заменяются неполным результатом.

Полезные флаги:

```bash
go run ./cmd/telegram-harvest --profile main daily --date 2026-06-04 --progress
go run ./cmd/telegram-harvest --profile main daily --date 2026-06-04 --download-media=false
go run ./cmd/telegram-harvest --profile main daily --date 2026-06-04 --transcribe=false
go run ./cmd/telegram-harvest --profile main daily --date 2026-06-04 --transcribe-video=phone
go run ./cmd/telegram-harvest --profile main daily --date 2026-06-04 --asr-workers=2 # diagnostic override
go run ./cmd/telegram-harvest --profile main daily --date 2026-06-04 --markdown-out reports/daily/2026-06-04.md
```

Актуализировать отчеты одной командой:

```bash
make daily-catchup PROFILE=main
go run ./cmd/telegram-harvest --profile main daily-catchup
```

`daily-catchup` смотрит последние дневные Markdown-отчеты в `reports/daily`, берет день после самого свежего `YYYY-MM-DD.md` и строит все недостающие отчеты до текущей даты не включительно. Весь новый диапазон читается из Telegram одним последовательным range-scan, после чего записи разделяются по московским дням и атомарно публикуются в отдельные JSONL/Markdown. `00-latest-catchup.md` не участвует в определении последней даты. Если сегодня 2026-06-07, а последний дневной отчет — 2026-06-02, команда построит 2026-06-03 ... 2026-06-06 и объединит их в один файл. Существующие дневные Markdown не перезаписываются; их даты также исключаются из media/ASR-обработки range-scan.

После полного успешного запуска `.state/daily/dialog-checkpoint.json` запоминает Telegram account, daily scope, наблюдаемый `top_message_id` и безопасный `verified_message_id` каждого dialog. Следующий обычный автоматический catch-up не вызывает history RPC только для dialog, чей неизменившийся head был целиком покрыт предыдущим диапазоном (`head_fully_verified=true`). Если вчерашний catch-up уже видел сегодняшний head через `get_dialogs`, такой dialog обязательно сканируется с безопасным `MinID=verified_message_id`, даже когда head численно не изменился. Изменившиеся и новые dialog также проверяются последовательно. Любой ручной `--from`, разрыв дат, исторический запуск, смена аккаунта/scope, поврежденный state, аномальный head, неполный scan или ошибка включает полный fallback. Checkpoint заменяется атомарно только после успешной публикации `00-latest-catchup.md`.

Для первого catch-up без предыдущих отчетов передай явный старт:

```bash
go run ./cmd/telegram-harvest --profile main daily-catchup --from 2026-06-03
```

Каноническое описание слова «catch-up», daily scope, медиа, ASR и проверок готовности находится в [`docs/catch-up.md`](docs/catch-up.md). `daily`/`daily-catchup` — единственный пользовательский catch-up flow.

Архитектура единого range-scan и воспроизводимый old/new benchmark описаны в [`docs/performance.md`](docs/performance.md).

## Telegram pacing

Ограничения чтения Telegram не настраиваются через `.env`. Они живут в коде:

| Параметр | Значение | Причина |
| --- | ---: | --- |
| RPC spacing | 700 ms | Перенесено из Telegram E2E Test Tool как рабочий read-only pacing. |
| History batch size | 100 | Кодовый cap для одного Telegram history request. |
| Default history limit | 100 | Обычный `dump`/incremental `sync` читает один batch; полный backfill делается через `--all`. |

`FLOOD_WAIT` обрабатывается внутри MTProto слоя: инструмент записывает flood event, ждёт Telegram delay, сдвигает следующий RPC слот и ретраит ограниченное число раз.

## Медиа и лимиты

Автоматические лимиты защищают локальную машину от слишком тяжелых скачиваний:

| Тип | Дефолт | Поведение |
| --- | ---: | --- |
| Photo / image document | 10 MiB | Сохраняется под `.state/.../media`. |
| Generic document | 10 MiB | Сохраняется под `.state/.../media`. |
| Voice / audio | 50 MiB | Временно скачивается для транскрибации. |
| Round video | 200 MiB | Временно скачивается для транскрибации. |
| Generic video | 80 MiB phone prefilter, then 200 MiB media cap | По умолчанию транскрибируются только vertical phone videos до 6 минут и не выше 1080x1920. |

Если файл выше лимита, JSONL сохраняет `download_error` и `download_hint`, а Markdown остается чистым пользовательским отчетом. Ручное скачивание делается отдельной командой и лимиты не применяет:

```bash
go run ./cmd/telegram-harvest --profile main daily-download-media \
  --chat 1234567890 \
  --message-id 777 \
  --index 1 \
  --out-dir media-manual
```

## ASR backends

Штатный daily-профиль на этой машине — `whisper.cpp large-v3-turbo q5_0 + Metal`, `beam_size=5` и отдельный whole-file Silero speech gate. Профиль зафиксирован в коде: агенту не нужно подбирать decoder/gate-флаги. Vosk сохранён как CPU fallback и benchmark baseline.

`internal/transcribe` предоставляет единый managed-runner contract, descriptor, cache identity и worker policy:

- `vosk`: CPU backend; `auto` может динамически вырасти от одного до четырёх независимых процессов, если backlog и ресурсы доказывают пользу;
- `whispercpp`: один долгоживущий локальный `whisper-server`; модель загружается один раз, а WAV отправляются на loopback HTTP. Для Metal и Metal + Core ML безопасный `auto` всегда использует один GPU worker;
- custom command: одноразовый внешний hook с одним worker.

Явные `--asr-workers=2..4` остаются диагностическим override. Для Whisper они позволяют поставить эксперимент, но не являются штатным режимом: процессы конкурируют за один GPU и unified memory.

Transcript cache включает backend, accelerator, model/quantization, язык, threads, decode profile, speech-gate profile и Vosk grammar. Поэтому результаты Vosk, Metal, Core ML, разных моделей и настроек не смешиваются. Смена backend один раз создаст новый cache namespace; готовый transcript по-прежнему публикуется атомарно.

### Vosk CPU fallback

Daily-транскрибация использует локальный Vosk helper на Go:

```bash
make vosk-transcribe
```

Ожидаемая настройка:

```dotenv
TG_HARVEST_DAILY_VOSK_COMMAND=bin/vosk-transcribe
TG_HARVEST_DAILY_VOSK_MODEL_PATH=models/vosk-model-small-ru-0.22
TG_HARVEST_DAILY_FFMPEG_COMMAND=ffmpeg

# Если libvosk не лежит в /opt/homebrew/lib или /usr/local/lib:
TG_HARVEST_DAILY_VOSK_LIBRARY_PATH=.state/vosk-runtime/libvosk.dylib
```

Worker protocol (the first response is emitted only after the model and optional grammar are loaded):

```text
vosk-transcribe --session <model-dir> [grammar-json-path]
{"ready":true}
{"id":1,"wav_path":"/tmp/.vosk-123.wav"}
{"id":1,"text":"recognized text"}
```

Это гибридный режим без постоянного демона. Telegram producer один: история и media downloads остаются строго последовательными и paced. После скачивания отсутствующее медиа попадает в bounded queue, а локальные workers параллельно выполняют `ffmpeg → ASR`. Результаты присоединяются к исходным сообщениям по cache path перед сортировкой и публикацией, поэтому порядок завершения workers не меняет JSONL/Markdown.

По умолчанию `--asr-workers=auto`: запуск начинается с одного worker, затем controller может постепенно активировать до четырех. Он расширяет пул только при реальном queued audio backlog, ожидаемой выгоде больше startup cost, CPU ниже защитного порога и достаточной доступной памяти с системным резервом. Фактический RSS Vosk worker измеряется после загрузки модели. Уже активированный worker сохраняется до конца запуска; дребезга create/kill нет. Значения `1..4` предназначены для диагностики и benchmark, обычному агенту их задавать не нужно.

Каждый Vosk worker имеет собственный process, модель и session protocol. Transcript cache проверяется до download, одинаковый in-flight media key распознается один раз, а готовый transcript публикуется через `temp → fsync/close → rename`. Если все транскрипты уже есть в кэше, ASR process не стартует и timing report показывает `pipeline_workers=0`.

Generic `video` по умолчанию идет через preflight `--transcribe-video=phone`: только вертикальные телефонные видео с Telegram metadata, длительностью до 360 секунд, размером до 80 MiB и разрешением не выше 1080x1920. Горизонтальные фильмы/длинные ролики скипаются до скачивания и попадают в ASR log со skip reason. Режимы:

```bash
--transcribe-video=phone # default: only short vertical phone videos
--transcribe-video=all   # transcribe generic video too, still subject to media byte caps
--transcribe-video=off   # never transcribe generic video
```

Поддерживаемые настройки:

```dotenv
TG_HARVEST_DAILY_VOSK_GRAMMAR_PATH=
```

Кастомный ASR hook можно задать явно:

```dotenv
TG_HARVEST_DAILY_TRANSCRIBE_CMD=whisper-cli --language ru --input {input} --output {output}
```

Такой hook запускается per attachment и не использует Vosk session protocol.

Vosk helper использует `libvosk`/Kaldi только на CPU. Metal к нему намеренно не добавляется.

### whisper.cpp Metal / Core ML

whisper.cpp собирается из официального checkout. Пример Metal-сборки:

```bash
git clone https://github.com/ggml-org/whisper.cpp .state/asr-runtime/whisper.cpp
git -C .state/asr-runtime/whisper.cpp checkout v1.9.1
cmake -S .state/asr-runtime/whisper.cpp \
  -B .state/asr-runtime/whisper.cpp/build-metal \
  -DCMAKE_BUILD_TYPE=Release -DGGML_METAL=ON \
  -DWHISPER_COREML=OFF -DWHISPER_BUILD_TESTS=OFF
cmake --build .state/asr-runtime/whisper.cpp/build-metal -j 12 \
  --target whisper-server whisper-vad-speech-segments
.state/asr-runtime/whisper.cpp/models/download-ggml-model.sh large-v3-turbo-q5_0
.state/asr-runtime/whisper.cpp/models/download-vad-model.sh silero-v6.2.0
```

Настройка daily:

```dotenv
TG_HARVEST_DAILY_ASR_BACKEND=whispercpp
TG_HARVEST_DAILY_WHISPER_COMMAND=.state/asr-runtime/whisper.cpp/build-metal/bin/whisper-server
TG_HARVEST_DAILY_WHISPER_MODEL_PATH=.state/asr-runtime/whisper.cpp/models/ggml-large-v3-turbo-q5_0.bin
TG_HARVEST_DAILY_WHISPER_ACCELERATOR=metal
TG_HARVEST_DAILY_WHISPER_THREADS=4
TG_HARVEST_DAILY_ASR_LANGUAGE=ru
TG_HARVEST_DAILY_WHISPER_SPEECH_GATE_MODEL_PATH=.state/asr-runtime/whisper.cpp/models/ggml-silero-v6.2.0.bin
```

Daily всегда использует один GPU worker, `beam_size=5` и Silero с threshold `0.5`, minimum speech `250 ms`, minimum silence `100 ms`, padding `30 ms`. Gate запускается после ffmpeg и только отвечает «есть ли в файле речь»; исходный WAV целиком отправляется в Whisper. Это убирает non-speech hallucinations, не рискуя обрезать отрицания или начало/конец фразы. Известные точные terminal boilerplate-фразы Whisper (`Продолжение следует`, `Субтитры сделал DimaTorzok` и подобные) удаляются только отдельной последней строкой и записываются в diagnostics; совпадение внутри нормального предложения не трогается.

Core ML требует encoder `ggml-<model>-encoder.mlmodelc` рядом с GGML model и отдельную сборку с `-DWHISPER_COREML=ON`. Runtime evidence должен одновременно содержать `COREML = 1`, загрузку Core ML model и Metal device. Одного имени конфигурации недостаточно: benchmark проверяет фактические логи.

Инженерный benchmark запускается отдельным developer tool, не добавляя второй пользовательский catch-up flow:

```bash
go run ./cmd/asr-benchmark \
  --manifest .state/asr-benchmark/manifest.json \
  --out .state/asr-benchmark/results.json \
  --runs 3
```

## Производительность daily

Контрольный cold-cache день 2026-07-25 содержал 210 сообщений и три ASR jobs общей длительностью 170.284 с. Старый последовательный flow занял 94.22 с; auto pipeline — 54.95 и 55.61 с на двух повторах, то есть примерно 1.70–1.71× быстрее. Fixed 1 worker занял 60.66 с, fixed 2 — 54.56 с, fixed 4 — 55.06 с. Auto выбрал два workers; четыре не дали дополнительной пользы, потому что critical path снова определялся последовательной Telegram-работой.

| Нагрузка дня | Оценка |
| --- | ---: |
| Почти без ASR | 1.5-2 минуты |
| Обычный день с voice/round-video | 2.5-6 минут |
| Тяжелый день с десятками voice/round-video/phone-video ASR | 6-8 минут |
| 19 дней с локальным Vosk CPU (старая baseline-оценка) | около 1 часа |

Основной драйвер времени - количество и длительность audio/video, а не только число сообщений. Generic horizontal/long video по умолчанию скипается до скачивания, чтобы фильмы и крупные travel clips не уходили в CPU ASR. Transcript cache keyed by Telegram media id, поэтому повторные запуски заметно дешевле.
Daily пропускает per-chat history для чатов, где последнее сообщение старше нужного дня, но не останавливает загрузку списка диалогов по первому старому чату: на исторических датах Telegram dialog order оказался недостаточным стоп-критерием.

Для полноты daily использует только `messages.getHistory` и уже локально оставляет self/outgoing и настроенных дополнительных отправителей. `messages.search` намеренно не используется: реальный пустой-query поиск по self воспроизводимо пропустил существующее исходящее сообщение. History pagination завершается по временной границе, safe checkpoint `MinID`, пустой странице или явному hard limit, но не только потому, что Telegram вернул короткую страницу.

## Study sync

Сначала посмотреть доступные чаты:

```bash
make chats PROFILE=study QUERY=вшэ
```

Если `TG_HARVEST_STUDY_ALLOWED_CHATS` задан, `chats`, `topics`, `dump` и `sync` работают только в этом scope.

Полная выгрузка:

```bash
go run ./cmd/telegram-harvest --profile study sync \
  --chat 1234567890 \
  --name study-main \
  --all \
  --reset \
  --reset-merged \
  --merged-out messages.jsonl
```

Resume после interruption:

```bash
go run ./cmd/telegram-harvest --profile study sync \
  --chat 1234567890 \
  --name study-main \
  --all \
  --merged-out messages.jsonl
```

Обычный incremental sync:

```bash
go run ./cmd/telegram-harvest --profile study sync \
  --chat 1234567890 \
  --name study-main \
  --merged-out messages.jsonl
```

Типичные private outputs:

```text
.state/study-main.jsonl
.state/study-main.state.json
.state/messages.jsonl
messages.jsonl
messages.toon
agent-view/
```

Study `dump`/`sync` не транскрибируют audio/video. Они сохраняют inspectable материалы вроде photos/images/documents при включенном `--download-media`.

Для явно запрошенной полной выгрузки одного чата основного аккаунта bounded `dump` может подключить тот же Vosk session worker и phone-video policy, что и daily:

```bash
make dump PROFILE=main CHAT=1234567890 \
  FROM=2026-07-01 TO=2026-07-14 ALL=1 \
  OUT=chat.jsonl DOWNLOAD_MEDIA=1 MEDIA_DIR=media \
  TRANSCRIBE=1 TRANSCRIPT_DIR=transcripts ASR_LOG=asr.jsonl
```

`FROM` включается, `TO` не включается. Транскрибация у `dump` требует явного `TRANSCRIBE=1` и доступна только профилю `main`; обычное поведение `study` не меняется.

## Низкоуровневые кирпичи для агентов

`dump` и `sync` получают lossless JSONL одного чата. `compact` и `agent-view` не являются пользовательскими catch-up командами: они преобразуют уже собранный JSONL, когда агенту нужно работать с большим учебным корпусом. Daily их не запускает, потому что `reports/daily/*.md` уже является его готовым компактным представлением.

JSONL - canonical lossless source. Markdown/TOON - производные представления, их можно пересобрать:

```bash
go run ./cmd/telegram-harvest --profile study agent-view --in messages.jsonl --out-dir agent-view
go run ./cmd/telegram-harvest --profile study compact --in messages.jsonl --out messages.toon
make refresh-agent-view PROFILE=study
```

Обычный путь чтения для агента:

1. Открыть `agent-view/README.md`.
2. Для общего/latest-вопроса открыть `all-recent.md`.
3. Если известен чат или тема, идти в конкретный chat/topic каталог.
4. Открывать дневные Markdown-файлы, а raw JSONL использовать только для audit/debug.

## Команды разработки

```bash
make help
make setup
make fmt
make test
make vosk-transcribe
go run ./cmd/telegram-harvest --help
go run ./cmd/telegram-harvest --profile main daily --help
go run ./cmd/telegram-harvest --profile main daily-catchup --help
```

## Структура

| Путь | Назначение |
| --- | --- |
| `cmd/telegram-harvest` | CLI entrypoint и wiring команд. |
| `cmd/asr-benchmark` | Developer-only воспроизводимый benchmark ASR backends на локальном corpus manifest. |
| `cmd/vosk-transcribe` | Go/cgo Vosk helper с one-shot и session режимами. |
| `internal/config` | `.env`, профили, defaults, allowlist и runtime paths. |
| `internal/mtproto` | Telegram transport, login, dialogs/history/topics/daily reads. |
| `internal/harvest` | JSONL model, sync state, daily Markdown, compact и agent views. |
| `internal/transcribe` | Общий ASR descriptor/cache/policy contract, Vosk session runner, whisper.cpp server runner и custom hook. |
| `internal/asrbench` | Corpus hashing, cold repetitions, process resources, WER/CER и error/hallucination metrics. |
| `internal/runlock` | Per-session lock по файлу вида `.sessions/<session>.json.runtime.lock`, чтобы не запускать два MTProto процесса на одну session file и не блокировать другой аккаунт. |
| `reports/daily` | Локальные Markdown-отчеты для пользователя, ignored by git. |

## Safety model

- Telegram operations read-only: никаких send/click/delete/join/pin/mark-read.
- Broad daily scan пишет только outgoing/self messages и явно настроенных sender IDs строго в их настроенных chat IDs.
- Study scope ограничивается `TG_HARVEST_STUDY_ALLOWED_CHATS`, когда allowlist задан.
- `.env`, `.sessions/`, `.state/`, `reports/`, `models/`, `bin/`, dumps и generated views приватные и не коммитятся.
- Live Telegram поведение проверяется вручную после логина; автоматические тесты покрывают локальную логику, config, state, rendering и helpers.
