# Telegram Harvest

Локальный read-only CLI для сбора Telegram-данных через MTProto user authorization.
Проект рассчитан на два практических сценария:

- **daily reports** - личные исходящие сообщения за день, Markdown-отчеты в `reports/daily`, локальная транскрибация voice/video через Vosk;
- **study harvest** - выгрузка, синк и агентские Markdown-представления для учебных чатов из allowlist.

CLI один и тот же для всех сценариев. Аккаунт выбирается профилем `main` или `study`, а не отдельными account-specific командами.

## Что умеет

| Область | Поведение |
| --- | --- |
| Авторизация | MTProto user session через `login` и явные API credentials для каждого профиля. |
| Профили | `main` читает `TG_HARVEST_DAILY_*`; `study` читает `TG_HARVEST_STUDY_*`. Других алиасов профилей/env нет. |
| Daily | Сканирует диалоги за один московский день и пишет только outgoing/self сообщения авторизованного аккаунта. |
| Отчеты | Пользовательские daily-отчеты лежат в `reports/daily/YYYY-MM-DD.md`; JSONL и кэши остаются в `.state/`. |
| Медиа | Картинки сохраняются локально, audio/video временно скачиваются для ASR и удаляются после транскрибации. |
| Vosk | Go helper `bin/vosk-transcribe` работает как session worker: модель грузится один раз на запуск `daily`. |
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
reports/daily/YYYY-MM-DD.md
.state/daily/jsonl/YYYY-MM-DD.jsonl
.state/daily/media/...
.state/daily/transcripts/cache/...
```

Markdown в `reports/daily` - основной человекочитаемый результат. Он содержит время, чат назначения, текст сообщения, Markdown-ссылку на Telegram-сообщение когда она доступна, короткие сведения о вложениях и транскрипты без технических путей, размеров, cache-полей, ASR/ffmpeg ошибок и runtime-сводки вроде периода или числа просканированных диалогов.

JSONL в `.state/daily/jsonl` - технический audit/source layer. Он хранит raw-поля вроде `media_id`, `local_path`, `transcript_path`, `download_hint` и нужен для отладки, пересборки и анализа, но не является пользовательским отчетом.

Полезные флаги:

```bash
go run ./cmd/telegram-harvest --profile main daily --date 2026-06-04 --progress
go run ./cmd/telegram-harvest --profile main daily --date 2026-06-04 --download-media=false
go run ./cmd/telegram-harvest --profile main daily --date 2026-06-04 --transcribe=false
go run ./cmd/telegram-harvest --profile main daily --date 2026-06-04 --retain-days 0
go run ./cmd/telegram-harvest --profile main daily --date 2026-06-04 --markdown-out reports/daily/2026-06-04.md
```

Daily retention по умолчанию хранит 14 дней state-артефактов. `--retain-days 0` отключает pruning для конкретного запуска.

## Медиа и лимиты

Автоматические лимиты защищают локальную машину от слишком тяжелых скачиваний:

| Тип | Дефолт | Поведение |
| --- | ---: | --- |
| Photo / image document | 10 MiB | Сохраняется под `.state/.../media`. |
| Generic document | 10 MiB | Сохраняется под `.state/.../media`. |
| Voice / audio | 50 MiB | Временно скачивается для транскрибации. |
| Video / round video | 200 MiB | Временно скачивается для транскрибации. |

Если файл выше лимита, JSONL сохраняет `download_error` и `download_hint`, а Markdown остается чистым пользовательским отчетом. Ручное скачивание делается отдельной командой и лимиты не применяет:

```bash
go run ./cmd/telegram-harvest --profile main daily-download-media \
  --chat 1234567890 \
  --message-id 777 \
  --index 1 \
  --out-dir media-manual
```

## Vosk

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

Worker protocol:

```text
vosk-transcribe --session <model-dir> [grammar-json-path]
{"id":1,"wav_path":"/tmp/.vosk-123.wav"}
{"id":1,"text":"recognized text"}
```

Это гибридный режим: постоянного демона нет, но внутри одного `daily`-запуска модель грузится один раз и переиспользуется для всех voice/audio/video cache misses. Если все транскрипты уже есть в кэше, Vosk process не стартует.

Поддерживаемые настройки:

```dotenv
TG_HARVEST_DAILY_VOSK_GRAMMAR_PATH=
TG_HARVEST_DAILY_RETENTION_DAYS=14
```

Кастомный ASR hook можно задать явно:

```dotenv
TG_HARVEST_DAILY_TRANSCRIBE_CMD=whisper-cli --language ru --input {input} --output {output}
```

Такой hook запускается per attachment и не использует Vosk session protocol.

## Производительность daily

На текущем диапазоне 2026-05-17 ... 2026-06-04 полный main-прогон с Vosk занял около часа. Практические ориентиры:

| Нагрузка дня | Оценка |
| --- | ---: |
| Почти без ASR | 1.5-2 минуты |
| Обычный день с voice/round-video | 2.5-6 минут |
| Тяжелый день с десятками media/ASR | 6-8 минут |
| 19 дней с локальным Vosk CPU | около 1 часа |

Основной драйвер времени - количество и длительность audio/video, а не только число сообщений. Transcript cache keyed by Telegram media id, поэтому повторные запуски заметно дешевле.
Daily пропускает per-chat history/search для чатов, где последнее сообщение старше нужного дня, но не останавливает загрузку списка диалогов по первому старому чату: на исторических датах Telegram dialog order оказался недостаточным стоп-критерием.

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
  --batch-size 100 \
  --merged-out messages.jsonl
```

Resume после interruption:

```bash
go run ./cmd/telegram-harvest --profile study sync \
  --chat 1234567890 \
  --name study-main \
  --all \
  --batch-size 100 \
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

## Agent views

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
```

## Структура

| Путь | Назначение |
| --- | --- |
| `cmd/telegram-harvest` | CLI entrypoint и wiring команд. |
| `cmd/vosk-transcribe` | Go/cgo Vosk helper с one-shot и session режимами. |
| `internal/config` | `.env`, профили, defaults, allowlist и runtime paths. |
| `internal/mtproto` | Telegram transport, login, dialogs/history/topics/daily reads. |
| `internal/harvest` | JSONL model, sync state, daily Markdown, media retention, compact и agent views. |
| `internal/transcribe` | ffmpeg conversion, Vosk session runner, custom command hook. |
| `internal/runlock` | Per-session lock, чтобы не запускать два MTProto процесса на одну session file. |
| `reports/daily` | Локальные Markdown-отчеты для пользователя, ignored by git. |

## Safety model

- Telegram operations read-only: никаких send/click/delete/join/pin/mark-read.
- Broad full-account daily scan пишет только outgoing/self messages.
- Study scope ограничивается `TG_HARVEST_STUDY_ALLOWED_CHATS`, когда allowlist задан.
- `.env`, `.sessions/`, `.state/`, `reports/`, `models/`, `bin/`, dumps и generated views приватные и не коммитятся.
- Live Telegram поведение проверяется вручную после логина; автоматические тесты покрывают локальную логику, config, state, rendering и helpers.
