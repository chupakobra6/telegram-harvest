# Telegram Harvest

[![CI](https://github.com/chupakobra6/telegram-harvest/actions/workflows/ci.yml/badge.svg)](https://github.com/chupakobra6/telegram-harvest/actions/workflows/ci.yml)

Локальный read-only CLI для сбора Telegram-данных через MTProto user authorization.
Проект рассчитан на два практических сценария:

- **daily reports** - личные исходящие сообщения и настроенные chat-scoped источники за день, Markdown-отчеты в `reports/daily`, локальная транскрибация voice/audio/round-video и коротких вертикальных phone-like video через production Whisper pipeline;
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
| Daily ASR | Один долгоживущий whisper.cpp large-v3-turbo q5_0 server на Metal, whole-file Silero gate и один GPU worker. OBS использует отдельную адаптивную long-form policy того же backend. |
| Study sync | `dump`/`sync` читают только allowlisted-чаты, поддерживают resumable backfill и производят JSONL. |
| Agent view | `agent-view` и `compact` строят компактные Markdown/TOON-представления из JSONL. |
| Safety | Инструмент не отправляет сообщения и не мутирует Telegram-состояние. History и выбор файлов идут последовательно и с pacing; downloader использует не более двух глобальных Telegram chunk slots. |

## Быстрый старт

Требуется Go 1.26.5 или новее. Для daily ASR также нужны `ffmpeg`, Metal-сборка `whisper-server` и две локальные модели; `doctor` показывает готовность каждого компонента.

```bash
cd telegram-harvest
cp .env.example .env
make setup
make build
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
bin/telegram-harvest --profile main me
```

Логин учебного аккаунта через API credentials:

```bash
make login PROFILE=study
make doctor PROFILE=study
bin/telegram-harvest --profile study me
```

## Профили

Профиль всегда указывается явно. CLI не выбирает аккаунт по команде и не имеет дефолтного аккаунта.

```bash
bin/telegram-harvest --profile main  <command>
bin/telegram-harvest --profile study <command>
```

Makefile повторяет эту модель: команды, которые читают профиль, требуют `PROFILE=main|study`. Первый Make-запуск собирает `bin/telegram-harvest`; следующие запуски переиспользуют бинарник, пока не изменятся Go sources, `go.mod` или `go.sum`.

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
bin/telegram-harvest --profile main daily --date 2026-06-04
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

Каждый `daily`/`daily-catchup` дополнительно атомарно сохраняет отдельный неизменяемый JSON в `.state/daily/timings/` и печатает его путь. В нем напрямую измерены worker-seconds `telegram_scan`, `download`, `ffmpeg`, `model_cold_start`, `asr`, `render`, полный wall time и `stage_work_seconds`. Объект `download_transport` хранит policy, число файлов и байт, сумму transfer wall, throughput, выбранные chunk workers, retries, failures, downloader FloodWait и transport floods; `media_pipeline` отдельно хранит backend/model/accelerator, speech-gate seconds, span/overlap, queue peak, jobs/dedup/failures, startup/RSS/ASR speed и CPU evidence. `telegram_rpc` хранит static spacing, calls, scheduled wait, операции и transport floods; `history_pagination` — data/empty/sparse pages и checkpoint proof decisions. ASR JSONL дополнительно сохраняет решение gate, confidence signals и удаленные terminal hallucinations. Worker-seconds могут быть больше wall time, потому что локальная обработка перекрывается с Telegram download.

Daily публикует финальные Markdown/JSONL отчеты атомарно: если день не собран до `complete=true`, файлы `reports/daily/YYYY-MM-DD.md` и `.state/daily/jsonl/YYYY-MM-DD.jsonl` не заменяются неполным результатом.

Полезные флаги:

```bash
bin/telegram-harvest --profile main daily --date 2026-06-04 --progress
bin/telegram-harvest --profile main daily --date 2026-06-04 --download-media=false
bin/telegram-harvest --profile main daily --date 2026-06-04 --transcribe=false
bin/telegram-harvest --profile main daily --date 2026-06-04 --transcribe-video=phone
bin/telegram-harvest --profile main daily --date 2026-06-04 --markdown-out reports/daily/2026-06-04.md
```

Актуализировать отчеты одной командой:

```bash
make daily-catchup PROFILE=main
bin/telegram-harvest --profile main daily-catchup
```

`daily-catchup` смотрит последние дневные Markdown-отчеты в `reports/daily`, берет день после самого свежего `YYYY-MM-DD.md` и строит все недостающие отчеты до текущей даты не включительно. Весь новый диапазон читается из Telegram одним последовательным range-scan, после чего записи разделяются по московским дням и атомарно публикуются в отдельные JSONL/Markdown. `00-latest-catchup.md` не участвует в определении последней даты. Если сегодня 2026-06-07, а последний дневной отчет — 2026-06-02, команда построит 2026-06-03 ... 2026-06-06 и объединит их в один файл. Существующие дневные Markdown не перезаписываются; их даты также исключаются из media/ASR-обработки range-scan.

После полного успешного запуска `.state/daily/dialog-checkpoint.json` запоминает Telegram account, daily scope, наблюдаемый `top_message_id` и безопасный `verified_message_id` каждого dialog. Граница берётся из всех реально прочитанных raw-сообщений строго до exclusive end диапазона, а не только из self/Trackmate records, попавших в отчет. Следующий обычный автоматический catch-up не вызывает history RPC только для dialog, чей неизменившийся head был целиком покрыт предыдущим диапазоном (`head_fully_verified=true`). Если вчерашний catch-up уже видел сегодняшний head через `get_dialogs`, такой dialog обязательно сканируется с безопасным `MinID=verified_message_id`, даже когда head численно не изменился. Изменившиеся и новые dialog также проверяются последовательно. Любой ручной `--from`, разрыв дат, исторический запуск, смена аккаунта/scope, поврежденный state, аномальный head, неполный scan или ошибка включает полный fallback. Checkpoint заменяется атомарно только после успешной публикации `00-latest-catchup.md`.

Для первого catch-up без предыдущих отчетов передай явный старт:

```bash
bin/telegram-harvest --profile main daily-catchup --from 2026-06-03
```

Каноническое описание слова «catch-up», daily scope, медиа, ASR и проверок готовности находится в [`docs/catch-up.md`](docs/catch-up.md). `daily`/`daily-catchup` — единственный пользовательский catch-up flow.

## Telegram pacing

Ограничения чтения Telegram не настраиваются через `.env`. Они живут в коде:

| Параметр | Значение | Причина |
| --- | ---: | --- |
| RPC spacing (`main` и `study`) | 500 ms | Единый статический code-owned floor: три 103-RPC прогона прошли без FloodWait, тогда как 400 ms и повторный 450 ms упёрлись в накопительный лимит. |
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
bin/telegram-harvest --profile main daily-download-media \
  --chat 1234567890 \
  --message-id 777 \
  --index 1 \
  --out-dir media-manual
```

Downloader выбирает chunk concurrency автоматически по размеру: файл меньше 1 MiB занимает один из двух глобальных slots, поэтому два маленьких файла могут скачиваться одновременно; файл от 1 MiB занимает оба slots. History RPC получает эксклюзивный доступ и не пересекается с download wave. Четыре slots не используются в production, потому что live-матрица дала с ними реальный FloodWait. CPU/RAM не участвуют в этом выборе: это сетевые chunk workers с bounded 512 KiB parts, а безопасная граница определяется Telegram transport evidence, не загрузкой локальной машины.

## ASR pipeline

Канонический production backend — `whisper.cpp large-v3-turbo q5_0 + Metal`, четыре CPU helper threads и `beam_size=5`. Публичный `short-message-v1` фиксирует русский язык и отдельный whole-file Silero speech gate; `trusted-long-form-v3` переиспользует тот же backend с адаптивной long-form policy. Эти параметры зафиксированы в коде. CLI позволяет менять только локальные пути к server/model/gate/ffmpeg и включать или выключать транскрибацию.

Тот же production-профиль доступен локальным автоматизациям без Telegram RPC:

```bash
bin/telegram-harvest --profile main transcribe-file \
  --input /absolute/path/recording.mp4 \
  --output /absolute/path/transcript.txt

# Адаптивный long-form профиль для доверенного локального caller вроде OBS:
bin/telegram-harvest --profile main transcribe-file \
  --trusted-long-form \
  --input /absolute/path/interview.mp4 \
  --output /absolute/path/interview.txt
```

Команда пишет plain UTF-8 transcript в `--output`, а в stdout — ASR contract v3 с `profile_id`, `validation_status`, backend descriptor, diagnostics и timings. `transcribe-file --check` проверяет текущие runtime/model paths без обработки медиа и возвращает `validation_status: runtime-ready`. Публичных профиля ровно два: обычный Telegram/file-вход — `short-message-v1`, OBS long-form — `trusted-long-form-v3`. OBS Interview Pipeline использует только этот публичный контракт, поэтому Whisper profile, VAD, Metal/decode validation, language policy, prompt и post-filter обновляются только здесь, в `internal/transcribe`.

Для длинной OBS-записи `--trusted-long-form` bounded Silero-окнами находит первую и последнюю речь и сохраняет секундный lead-in. Затем короткий 15-секундный Whisper probe определяет язык без построения transcript: русский получает code-owned punctuation seed `Да. Нет? Хорошо! Пожалуйста, продолжайте.` с `carry_initial_prompt=false`, английский декодируется без prompt, остальные языки — через native auto-language без prompt. После probe выполняется ровно один нативный timestamped long-form decode всего непрерывного аудио. Внутренний декодер сам выбирает границы окон, переносит подтверждённый контекст и продвигается по timestamp-токенам; внешних чанков и текстовой склейки нет.

Harvest проверяет duration, монотонность timestamps и достижение последней найденной речи с двухсекундным допуском; только после этого возвращается `validation_status: coverage-validated`. Это гарантия структурной целостности и покрытия хвоста, а не оценка WER/CER. Detected language, применение prompt и отдельное время `language_detection` входят в diagnostics/contract; policy и prompt входят в descriptor и cache identity. Модель `large-v3-turbo-q5_0`, Metal, beam/fallback settings и terminal post-filter остаются единым кодовым профилем. Обычные Telegram-команды не используют long-form policy и сохраняют русский профиль, whole-file Silero gate и `no_timestamps=true`.

Один `whisper-server` загружается лениво при первой ASR job и переиспользуется до конца запуска. Telegram scan и выбор следующего download остаются у единственного последовательного paced producer; общий coordinator распределяет ровно два chunk slots между двумя маленькими файлами либо одним большим и не пересекает download wave с history RPC. Отсутствующее в transcript cache медиа попадает в bounded queue ёмкостью два элемента; пока один GPU worker выполняет `ffmpeg → speech gate → Whisper`, producer может скачать следующее медиа. Результаты присоединяются по cache path и публикуются в исходном порядке сообщений.

Несколько Whisper-процессов не запускаются: на Apple Silicon они конкурируют за один GPU и unified memory, а измеренный production workload этого не требует. Transcript cache включает runtime/model/quantization, язык, threads, decode, gate и post-filter; готовый transcript публикуется через `temp → fsync/close → rename`. Cache проверяется до media download, поэтому повторный catch-up не запускает ASR для уже известных вложений.

Generic `video` по умолчанию идет через preflight `--transcribe-video=phone`: только вертикальные телефонные видео с Telegram metadata, длительностью до 360 секунд, размером до 80 MiB и разрешением не выше 1080x1920. Горизонтальные фильмы/длинные ролики скипаются до скачивания и попадают в ASR log со skip reason. Режимы:

```bash
--transcribe-video=phone # default: only short vertical phone videos
--transcribe-video=all   # transcribe generic video too, still subject to media byte caps
--transcribe-video=off   # never transcribe generic video
```

### whisper.cpp Metal runtime

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
TG_HARVEST_DAILY_WHISPER_COMMAND=.state/asr-runtime/whisper.cpp/build-metal/bin/whisper-server
TG_HARVEST_DAILY_WHISPER_MODEL_PATH=.state/asr-runtime/whisper.cpp/models/ggml-large-v3-turbo-q5_0.bin
TG_HARVEST_DAILY_WHISPER_SPEECH_GATE_MODEL_PATH=.state/asr-runtime/whisper.cpp/models/ggml-silero-v6.2.0.bin
TG_HARVEST_DAILY_FFMPEG_COMMAND=ffmpeg
```

Daily всегда использует один GPU worker, `beam_size=5` и Silero с threshold `0.5`, minimum speech `250 ms`, minimum silence `100 ms`, padding `30 ms`. Gate запускается после ffmpeg и только отвечает «есть ли в файле речь»; исходный WAV целиком отправляется в Whisper. Это убирает non-speech hallucinations, не рискуя обрезать отрицания или начало/конец фразы. Известные точные terminal boilerplate-фразы Whisper (`Продолжение следует`, `Субтитры сделал DimaTorzok` и подобные) удаляются только отдельной последней строкой и записываются в diagnostics; совпадение внутри нормального предложения не трогается.

Runtime обязан подтвердить `ggml_metal_init: found device`; отсутствие Metal или неожиданная активация Core ML останавливает ASR вместо тихого перехода на другой pipeline.

Инженерный benchmark сравнивает только Whisper-варианты decoder/model/gate и не добавляет второй пользовательский catch-up flow:

```bash
go run ./cmd/asr-benchmark \
  --manifest /path/to/manifest.json \
  --out /path/to/results.json \
  --runs 3
```

## Study sync

Сначала посмотреть доступные чаты:

```bash
make chats PROFILE=study QUERY=вшэ
```

Если `TG_HARVEST_STUDY_ALLOWED_CHATS` задан, `chats`, `topics`, `dump` и `sync` работают только в этом scope.

Полная выгрузка:

```bash
bin/telegram-harvest --profile study sync \
  --chat 1234567890 \
  --name study-main \
  --all \
  --reset \
  --reset-merged \
  --merged-out messages.jsonl
```

Resume после interruption:

```bash
bin/telegram-harvest --profile study sync \
  --chat 1234567890 \
  --name study-main \
  --all \
  --merged-out messages.jsonl
```

Обычный incremental sync:

```bash
bin/telegram-harvest --profile study sync \
  --chat 1234567890 \
  --name study-main \
  --merged-out messages.jsonl
```

Типичные private outputs:

```text
.state/study-main.jsonl
.state/study-main.state.json
.state/messages.jsonl
.state/messages.toon
.state/agent-view/
```

Все относительные `--out`, `--in`, `--merged-out`, `--media-dir` и `--out-dir` в low-level командах разрешаются внутри state-dir выбранного профиля. Абсолютный путь используется без изменений.

Study `dump`/`sync` не транскрибируют audio/video. Они сохраняют inspectable материалы вроде photos/images/documents при включенном `--download-media`.

Для явно запрошенной полной выгрузки одного чата основного аккаунта bounded `dump` использует тот же production Whisper pipeline и phone-video policy, что и daily:

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
bin/telegram-harvest --profile study agent-view --in messages.jsonl --out-dir agent-view
bin/telegram-harvest --profile study compact --in messages.jsonl --out messages.toon
make refresh-agent-view PROFILE=study
```

Обычный путь чтения для агента:

1. Открыть `.state/agent-view/README.md` (или соответствующий каталог внутри настроенного study state-dir).
2. Для общего/latest-вопроса открыть `all-recent.md`.
3. Если известен чат или тема, идти в конкретный chat/topic каталог.
4. Открывать дневные Markdown-файлы, а raw JSONL использовать только для audit/debug.

## Команды разработки

```bash
make help
make setup
make fmt
make test
make check
make audit
bin/telegram-harvest --help
bin/telegram-harvest --profile main daily --help
bin/telegram-harvest --profile main daily-catchup --help
```

## Структура

| Путь | Назначение |
| --- | --- |
| `cmd/telegram-harvest` | CLI entrypoint и wiring команд. |
| `cmd/asr-benchmark` | Developer-only воспроизводимый benchmark Whisper model/decode/gate вариантов на локальном corpus manifest. |
| `internal/config` | `.env`, профили, defaults, allowlist и runtime paths. |
| `internal/mtproto` | Telegram transport, login, dialogs/history/topics/daily reads. |
| `internal/harvest` | JSONL model, sync state, daily Markdown, compact и agent views. |
| `internal/transcribe` | Production Whisper profile, descriptor/cache contract, speech gate и long-lived whisper.cpp server runner. |
| `internal/asrbench` | Corpus hashing, cold repetitions, process resources, WER/CER и error/hallucination metrics. |
| `internal/runlock` | Per-session lock по файлу вида `.sessions/<session>.json.runtime.lock`, чтобы не запускать два MTProto процесса на одну session file и не блокировать другой аккаунт. |
| `reports/daily` | Локальные Markdown-отчеты для пользователя, ignored by git. |

## Safety model

- Telegram operations read-only: никаких send/click/delete/join/pin/mark-read.
- Broad daily scan пишет только outgoing/self messages и явно настроенных sender IDs строго в их настроенных chat IDs.
- Study scope ограничивается `TG_HARVEST_STUDY_ALLOWED_CHATS`, когда allowlist задан.
- `.env`, `.sessions/`, `.state/`, `reports/`, `models/`, `bin/`, дефолтные `chat.jsonl`/`media-manual` и generated views приватные и не коммитятся.
- Live Telegram поведение проверяется вручную после логина; автоматические тесты покрывают локальную логику, config, state, rendering и helpers.
