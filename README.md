# Telegram Harvest

[![CI](https://github.com/chupakobra6/telegram-harvest/actions/workflows/ci.yml/badge.svg)](https://github.com/chupakobra6/telegram-harvest/actions/workflows/ci.yml)

Локальный CLI для сбора Telegram-данных через MTProto user authorization. Сбор остаётся read-only. Отдельные узкие команды отправляют результат самому себе в «Избранное» основного аккаунта, организуют метаданные завершённых рабочих диалогов и по прямому поручению правят собственные материалы в закреплённом топике собеседований.
Проект рассчитан на три практических сценария:

- **daily reports** - личные исходящие сообщения и настроенные chat-scoped источники за день, Markdown-отчеты в `reports/daily`, локальная транскрибация voice/audio/round-video и коротких вертикальных phone-like video через production Whisper pipeline;
- **study harvest** - выгрузка, синк и агентские Markdown-представления для учебных чатов из allowlist.
- **полный архив аккаунтов** — полная история доступных обычных и архивных диалогов любого зарегистрированного аккаунта: входящие и исходящие сообщения, приватный индекс и возобновление прерванной выгрузки.

CLI один и тот же для всех сценариев. Аккаунт выбирается явным профилем `main`, `study` или именем зарегистрированного аккаунта.

## Что умеет

| Область | Поведение |
| --- | --- |
| Авторизация | MTProto user session через `login`; каждый зарегистрированный аккаунт получает отдельную сессию и использует API credentials выбранного `main` или `study`. |
| Профили | `main` читает `TG_HARVEST_DAILY_*`; `study` читает `TG_HARVEST_STUDY_*`; дополнительные аккаунты регистрируются командой `account add`. |
| Daily | Сканирует диалоги за один московский день и пишет outgoing/self сообщения плюс настроенных отправителей в конкретных чатах. |
| Отчеты | Пользовательские daily-отчеты лежат в `reports/daily/YYYY-MM-DD.md`; JSONL и кэши остаются в `.state/`. |
| Медиа | Картинки сохраняются локально, audio/video временно скачиваются для ASR и удаляются после транскрибации; generic video проходит phone-like preflight. |
| Daily ASR | Один адаптивный профиль whisper.cpp large-v3-turbo q5_0 на Metal: быстрый short decode для обычных сообщений и защищённый long-form для длинного либо долго молчащего медиа. |
| Study sync | `dump`/`sync` читают только allowlisted-чаты, поддерживают resumable backfill и производят JSONL. |
| Полный архив | `account-sync` обходит все доступные диалоги выбранного зарегистрированного аккаунта без allowlist, сохраняет полную историю по чатам вне репозитория и обновляет приватный индекс. |
| Agent view | `agent-view` и `compact` строят компактные Markdown/TOON-представления из JSONL. |
| Safety | Сбор читает Telegram; узкие команды записи выполняются только по прямой просьбе Игоря. `send-saved` доступна только профилю `main`, проверяет активную сессию как `@Pheik13`, использует только `InputPeerSelf` и не принимает адресата. `account-organize --apply` ограничена папкой «Отработка» и архивом точных user peers из проверенного manifest; `interview-topic-update --apply` – собственными правками и двумя backed-up ZIP в закреплённом топике; `interview-topic-publish --apply` – новыми собственными интервью в том же фиксированном топике. `interview-topic-replace-video --apply` заменяет только audio IDs `2362`/`2365` на video с сохранением ID/родителя. History и выбор файлов идут последовательно и с pacing; downloader использует не более двух глобальных Telegram chunk slots. |

## Быстрый старт

Минимальная версия языка — Go 1.26.6; сборка использует Go 1.27.2, закреплённый в `go.mod`. Стандартный `GOTOOLCHAIN=auto` загружает его при необходимости. Для daily ASR также нужны `ffmpeg`, Metal-сборка `whisper-server` и две локальные модели; `doctor` показывает готовность каждого компонента.

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
bin/telegram-harvest --profile <account-name> <command>
```

Makefile повторяет эту модель: команды, которые читают профиль, требуют `PROFILE=main|study|<account-name>`. Первый Make-запуск собирает `bin/telegram-harvest`; следующие запуски переиспользуют бинарник, пока не изменятся Go sources, `go.mod` или `go.sum`.

```bash
make doctor PROFILE=main
make doctor PROFILE=study
make daily PROFILE=main DATE=2026-06-04
make daily-catchup PROFILE=main
make sync CHAT=1234567890 NAME=study-main PROFILE=study
```

## Полная история отдельных аккаунтов

Каждый аккаунт регистрируется один раз коротким именем. `--api-profile` выбирает только существующие Telegram API ID/hash из `.env`; номер, сессия и переписки `main` или `study` не наследуются. При `login` вводятся номер этого аккаунта, код Telegram и, если включён, пароль 2FA. Пароль вводится без отображения символов; при ошибке доступны ещё две попытки в том же запуске. Команда закрепляет числовой Telegram ID за именем; вход под другим аккаунтом с тем же именем отклоняется. Для второго рабочего аккаунта повтори `account add` с другим именем.

```bash
make account-add ACCOUNT_NAME=lumina22 API_PROFILE=main
make account-add ACCOUNT_NAME=work API_PROFILE=main
make account-list
make login PROFILE=lumina22
make doctor PROFILE=lumina22
make account-sync PROFILE=lumina22
```

У Telegram API ID/hash относятся к приложению, а доступные сообщения определяются авторизованной сессией. Поэтому один комплект credentials `main` можно использовать для входа в разные аккаунты, но у каждого будут собственные сессия и архив. Эти credentials настраиваются один раз; при полном отсутствии API ID/hash MTProto-вход невозможен. Обычный `make login PROFILE=<имя>` создаёт отдельную сессию по номеру и коду Telegram с паролем 2FA при необходимости; одного пароля для входа в новый аккаунт недостаточно. Harvest не читает файлы или ключи Telegram Desktop.

На macOS приватные профили лежат в `~/Library/Application Support/telegram-harvest/accounts/<имя>/`: `profile.json` хранит привязку, `session.json` — авторизацию, `state/` — архив. Каталоги доступны только владельцу (`0700`), файл профиля — `0600`. `account-sync` не меняет охват `study` или дневного `main` отчёта.

Первый `account-sync` читает всю доступную историю обычных и архивных диалогов; повторные запуски продолжают незавершённую выгрузку и дочитывают новые сообщения. Начинай анализ с `state/README.md` выбранного аккаунта: там статус полноты, список чатов и ссылки на `chats/<тип>-<id>/messages.jsonl`. Для вопроса о конкретном человеке или событии найди нужные JSONL через `rg -n`, открой подходящие сообщения и укажи тип чата с `message_id` в ответе. Тип входит в ключ потому, что числовые ID разных типов могут совпадать. Индекс с `complete=false` означает, что часть истории ещё не проверена. Текстовые RichMessage (Telegram layer 229) разворачиваются в полный текст: абзацы, оформление, списки, вложенные блочные цитаты с подписями и ссылки. Для custom emoji сохраняется текст Alt, а при его отсутствии – явная отметка с DocumentID. Сокращённая часть догружается отдельным RPC с тем же pacing; Документы, фотографии, аудио и видео внутри RichMessage сохраняют подписи и метаданные; скачивание использует общие ограничения, кэш и уникальный индекс вложения. Неизвестный блок или отсутствующий media resource дают явную ошибку полноты, а не пустой текст. Текст и метаданные вложений выгружаются; сами медиа и содержание голосовых/видеосообщений автоматически не скачиваются и не расшифровываются. Нужное вложение можно прочитать адресно командой `download-media --chat <тип>:<id>` с профилем этого аккаунта. Режим остаётся только для чтения; `send-saved` разрешён исключительно для `main`. После исправления декодера или для проверки правок старых сообщений запускай `make account-sync PROFILE=<имя> REFRESH_FROM=YYYY-MM-DD` либо `account-sync --refresh-from YYYY-MM-DD`. Дата задаётся по Москве. Текущий архив сохраняется; изменившиеся записи добавляются ревизиями, неизменные не дублируются. Новый профиль сначала выгружает всю доступную историю.

## Организация рабочих диалогов

По прямой просьбе Игоря можно добавить завершённые рабочие переписки в папку «Отработка» и архив Telegram. Эти команды используют обычную авторизованную сессию, её runtime lock, числовую привязку аккаунта и общий RPC pacing. Поддерживаются только зарегистрированные аккаунты, точные user peers и указанная папка. Сообщения, unread, join и pin не меняются.

```bash
# Чтение серверных фильтров: порядок, ID, названия, настройки и peer IDs без access hashes.
bin/telegram-harvest --profile work account-folders
# Проверка без записи по умолчанию:
bin/telegram-harvest --profile work account-organize --manifest /private/work-organize.json
# Применение того же проверенного списка:
bin/telegram-harvest --profile work account-organize --manifest /private/work-organize.json --apply
# Эквивалентный Make-вызов: APPLY=1 добавляет --apply, без него только dry-run.
make account-organize PROFILE=work MANIFEST=/private/work-organize.json APPLY=1
```

Manifest – один приватный JSON-файл (`0600` или строже), не больше 1 MiB. Неизвестные поля отклоняются. `profile` и `account_id` должны совпадать с выбранным профилем и его привязкой; текущая сессия проверяется повторно до обращения к диалогам.

```json
{
  "profile": "work",
  "account_id": 123456,
  "folder_name": "Отработка",
  "peers": [{"user_id": 654321, "last_message_id": 987}]
}
```

Допустимы 1–100 уникальных user IDs, кроме self. `last_message_id` – свежий серверный head диалога, включая сервисные сообщения; последние содержательные IN/OUT могут иметь меньший ID. До первой записи команда сверяет все heads через `messages.getPeerDialogs`, затем повторяет проверку перед архивированием каждого peer. Новый head, pinned-диалог вне архива или другая идентичность останавливают проход. При отказе первичной сверки ни папка, ни архив не меняются.

Существующий обычный фильтр копируется целиком, с названиями, оформлением, флагами, include/pinned/exclude и их порядком; добавляются только отсутствующие peers. Фильтры повторно читаются перед записью. Если «Отработка» отсутствует, создаётся обычная папка со свободным ID от 2 до 255. Одноимённая shared-папка, неоднозначное название или явный target в exclude вызывают отказ. Архивирование вызывает только `folders.editPeerFolders` с `folder_id=1`; уже архивированный peer повторно не перемещается. Новые сообщения не читаются и не помечаются прочитанными.

Результат – JSON. В dry-run `dry_run=true`, успешная сверка даёт `ready=true`, а `complete` остаётся `false`. После применения каждая строка получает `in_folder`, `archived` и `server_verified` только по серверному readback; `complete=true` требует подтверждения всех peers. При сбое возвращаются частичные строки и ненулевой exit code. Сохраняй этот результат приватно; повтор по тому же актуальному manifest перечитывает сервер и не повторяет подтверждённые изменения. Telegram не объединяет проверку head и перенос в одну транзакцию: ответ между RPC может дать частичный результат, который требует повторного разбора, без автоматического снятия архива. Одновременно редактировать эту папку другим клиентом во время команды не следует.

Локальный адресный набор: `go test ./internal/mtproto ./cmd/telegram-harvest -run 'TestAccountOrganize|TestAccountFolders|TestAccountOrganization' -count=1`. Он проверяет ограничения manifest, сохранность фильтра, dry-run, идемпотентность, свежесть heads, частичный сбой и серверные подтверждения. Живая проверка выполняется отдельно в разрешённом сценарии.

## Правка материалов в топике собеседований

`interview-topic-update` работает только по прямому поручению Игоря с уже опубликованными собственными материалами: `main/@Pheik13`, account ID `1266978055`, супергруппа `2621478426`, топик `115` «Собеседования». Другие аккаунты, получатели, новые сообщения и загрузки не поддерживаются. Используются обычные session/runtime lock и RPC pacing; команда не помечает сообщения прочитанными.

```bash
bin/telegram-harvest --profile main interview-topic-update --manifest /private/interview-review.json
bin/telegram-harvest --profile main interview-topic-update --manifest /private/interview-review.json --apply
make interview-topic-update PROFILE=main MANIFEST=/private/interview-review.json APPLY=1
```

Manifest – один JSON-объект в приватном файле `0600` или строже, до 1 MiB. Неизвестные поля и дубликаты message IDs отклоняются. Поля верхнего уровня: `profile: "main"`, `account_id: 1266978055`, `channel_id: 2621478426`, `topic_id: 115`, массивы `edits` и `delete_zip`; всего 1–100 операций.

- `edits`: `{id, expected_text_hash, new_text, expected_media_id?, new_entities?}`. Hash – SHA-256 полного старого текста/подписи в UTF-8 без нормализации. `expected_media_id` обязателен при имеющемся document/photo и имеет вид `document:123` или `photo:123`; для сообщения без вложения он отсутствует. Лимиты нового текста – 1024 UTF-16 единицы для подписи и 4096 для текста. Media, reply, album group и topic сохраняются.
- `new_entities`: массив `{type, offset, length, url?, language?}` с UTF-16 offsets, не разделяющими emoji. Поддерживаются `bold`, `italic`, `underline`, `strike`, `code`, `pre` (опциональный `language`), `url`, `hashtag`, `text_url` (явный HTTP(S) `url`). `hashtag` создаёт нативный кликабельный тег. `[]` задаёт обычный текст. При неизменном тексте и отсутствии поля прежние entities сохраняются; при изменении текста с прежними entities поле обязательно, чтобы не переносить старые offsets автоматически. Web previews и RichMessage этой узкой командой не редактируются.
- `delete_zip`: `{id, expected_text_hash, expected_media_id, filename, local_backup_path, sha256}`. Допустимы только IDs `2348`, `2352`. Команда проверяет своё авторство, топик, точные подпись/media ID/имя/MIME/размер, локальный абсолютный путь к непустому ZIP, его формат и SHA-256. Без подходящей локальной копии удаление не выполняется.

Текстовые правки передают `no_webpage=true`: новая URL в тексте не должна создавать превью и менять защищённое media. Уже прикреплённые document/photo, reply и topic сохраняются; существующие webpage этой командой по-прежнему не редактируются.

Все операции проходят preflight до первой записи. Перед каждой записью проверка повторяется; после неё сервер должен вернуть точные новые текст/entities и прежние media/reply/topic либо точный `MessageEmpty` удалённого ZIP. По умолчанию `dry_run=true`, успешный preflight даёт `ready=true`, но не `complete`. После `--apply` результат содержит частичные `rows` с `server_verified`, `already_applied`, `outcome_unknown`, `error`; `complete=true` требует подтверждения всего manifest. При ошибке записи команда делает одно readback, сохраняет подтверждённые строки и останавливается с ненулевым exit code, без повторной записи. Повторный запуск распознаёт уже новый текст и уже отсутствующий ZIP. Сохраняй JSON результата приватно. Изменение сообщения между RPC не проверяется атомарно; несовпадение означает остановку и повторный разбор, а не автоматический откат.

Для document сравниваются ID, MIME, размер, имя, все атрибуты/длительность/кодек, access hash и остальные прежние поля; исключён только временный `Document.FileReference`. Два нативных read одного неизменного сообщения подтвердили изменение именно этого поля. Dry-run повторно читает каждую правку, проверяет устойчивость защищённых полей и возвращает только имена изменившихся путей в `protected_changed_paths`; значения ссылок доступа не выводятся. `source_entity_types` первой строки содержит только классы исходных entities.

Адресная проверка: `go test ./internal/mtproto ./cmd/telegram-harvest -run 'TestInterviewTopicUpdate' -count=1`. Живой preflight/apply выполняется отдельно в разрешённом сценарии.

## Публикация новых интервью в закреплённый топик

`interview-topic-publish` выполняет прямое поручение Игоря опубликовать собственные интервью только в `main/@Pheik13` ID `1266978055`, супергруппе `2621478426`, топике `115` «Собеседования». Команда не принимает адресата. Рекрутерам и другим чатам она не пишет; `send-saved` по-прежнему использует исключительно self-peer.

```bash
bin/telegram-harvest --profile main interview-topic-publish --manifest /private/interviews.json
bin/telegram-harvest --profile main interview-topic-publish --manifest /private/interviews.json --apply
make interview-topic-publish PROFILE=main MANIFEST=/private/interviews.json APPLY=1
```

Manifest – приватный JSON-файл `0600` или строже, не более 1 MiB. Неизвестные поля отклоняются. Пример структуры (путь, hash и размер заменить проверенными значениями):

```json
{
  "profile": "main",
  "account_id": 1266978055,
  "channel_id": 2621478426,
  "topic_id": 115,
  "posts": [
    {"key": "2026-10-23-company-screening", "text": "Компания – скрининг, 23.10.2026"},
    {"key": "2026-10-23-company-transcript", "parent_key": "2026-10-23-company-screening", "text": "Полная расшифровка", "file": {
      "path": "/private/transcript.md", "sha256": "<64 lowercase hex characters>",
      "kind": "document", "filename": "transcript.md", "mime": "text/markdown", "size": 12345
    }}
  ]
}
```

В `posts` допустимы 1–20 упорядоченных записей, максимум два новых текстовых родителя. `key` – уникальный постоянный ключ публикации до 100 символов без пробелов и разделителей пути; не переиспользуй его для другого содержания. Новый родитель не имеет parent-полей. Ответ ссылается либо через `parent_key` на ранее объявленного нового родителя, либо через точный `parent_id` и `parent_text_hash` (SHA-256 полного серверного текста UTF-8). Родитель должен быть собственным сообщением в этом топике. Вложенные цепочки новых родителей не поддерживаются. `text` – точный текст или подпись; лимиты 4096/1024 UTF-16 единиц. Необязательные `entities` используют тот же формат и проверки, что `new_entities` команды update; отсутствие означает обычный текст, без автоматического Markdown-парсинга.

`file` содержит абсолютный `path`, SHA-256 исходных байтов, точное имя `filename` (basename пути), `mime`, положительный размер `size` и `kind`:

- `document`: только `.md` с `text/markdown` либо `.pdf` с `application/pdf`; PDF проходит проверку заголовка.
- `audio`: только `.m4a` с `audio/mp4` либо `.mp3` с `audio/mpeg`, `has_audio: true`, целая `duration` = округлённая вверх фактическая длительность в секундах. Загружается как обычное проигрываемое audio с `DocumentAttributeAudio`, без voice-флага.
- `video`: `.mp4` с MP4 `major_brand` (QuickTime `qt` отклоняется), `mime: "video/mp4"`, фактическими `duration`, `width`, `height`, `has_audio`. Загружается как video с `supports_streaming: true` и правильным аудиопризнаком.

Для audio/video нужен локальный `ffprobe`. Его format/streams/duration сверяются с manifest; допуск video duration – 0.1 секунды. Файлы не перекодируются, новый ASR не запускается. Локальные hash/метаданные проверяются до первой записи, перед каждой загрузкой и после неё. Аккаунт, группа, имя/ID топика и принадлежность родителя проверяются до отправки, в том числе после загрузки. Как и другие Telegram-операции, CLI использует runtime lock и общий RPC pacing; сам send не повторяется после ошибки.

Без `--apply` нет загрузок, отправок и записи receipts. `ready=true` означает успешный preflight, `complete=false`; ответ к ещё не созданному родителю окончательно проверяется при apply. Перед send проверяется существующая публикация с точным текстом/entities, родителем и media metadata через серверный поиск своего автора в этом топике. Совпадение без receipt останавливает команду для ручной сверки.

Apply атомарно сохраняет и синхронизирует приватный `<manifest>.receipts.json` до отправки, затем сохраняет полученный ID и серверное подтверждение. Random ID детерминирован постоянным ключом и фиксированным назначением. Храни manifest и receipts вместе вне Git, сохраняй путь и ключи при повторном запуске. Receipt связывает ключ с hash всего post; изменение содержания отклоняется. При известном ID команда только перечитывает сообщение и сверяет его. При неизвестном исходе без ID она останавливается без повторного send; автоматического восстановления такой строки нет. Не удаляй journal и не меняй key ради повторной отправки: сначала вручную установи серверный исход. Сетевая ошибка или несовпадение readback оставляют `outcome_unknown` и ненулевой exit code; подтверждённые предыдущие строки сохраняются. Частичный результат не означает готовность всего manifest.

Серверный readback проверяет собственное авторство, группу, forum topic, точный reply parent, текст/entities и для файла имя, MIME и размер. Для audio также проверяет audio attribute, длительность и отсутствие voice/video; для video – длительность, разрешение, streaming, отсутствие round-video и аудиопризнак. `complete=true` требует проверки всех записей. Telegram readback подтверждает метаданные, а не повторное скачивание и hash серверных байтов. Локальные файлы и родительские сообщения нельзя менять во время выполнения; сервер не предоставляет атомарную проверку родителя вместе с send.

Адресная проверка без живых сообщений: `go test ./internal/mtproto ./cmd/telegram-harvest -run 'TestInterviewTopicPublish|TestInterviewTopicUpdate|TestSendSaved' -count=1`. Живой preflight и apply выполняются отдельно в разрешённом сценарии.

## Замена двух аудиопостов собеседований на видео

`interview-topic-replace-video` – отдельная корректирующая команда по прямому поручению Игоря. Она заменяет audio на video в существующих собственных сообщениях `2362` (родитель `2361`) и `2365` (родитель `2364`) закреплённого топика `main/@Pheik13`, группа `2621478426`, топик `115`. Она не создаёт и не удаляет сообщения, не меняет MD/родителей и не удаляет локальные исходники. Альбомы исключены. API [`messages.editMessage`](https://core.telegram.org/method/messages.editMessage) принимает новое `InputMedia`; реальную допустимость конкретного media сервер подтверждает при apply.

```bash
bin/telegram-harvest --profile main interview-topic-replace-video --manifest /private/replace-video.json
bin/telegram-harvest --profile main interview-topic-replace-video --manifest /private/replace-video.json --apply
make interview-topic-replace-video PROFILE=main MANIFEST=/private/replace-video.json APPLY=1
```

Manifest – приватный JSON `0600` или строже, до 1 MiB; неизвестные поля отклоняются. Верхние поля: `profile: "main"`, `account_id: 1266978055`, `channel_id: 2621478426`, `topic_id: 115`, `replacements` с одной или двумя записями:

```json
{
  "id": 2362,
  "parent_id": 2361,
  "parent_text_hash": "<SHA-256 полного текста родителя>",
  "expected_caption_hash": "<SHA-256 полной старой подписи>",
  "expected_media_id": "document:<точный старый Telegram ID>",
  "old_audio": {"filename": "screening.m4a", "mime": "audio/mp4", "size": 12345, "duration": 60},
  "new_caption": "Видеозапись",
  "new_entities": [],
  "new_video": {
    "path": "/private/screening.mp4", "sha256": "<SHA-256 байтов MP4>",
    "kind": "video", "filename": "screening.mp4", "mime": "video/mp4", "size": 123456,
    "duration": 60.0, "width": 1920, "height": 1080, "has_audio": true
  }
}
```

`new_video` использует тот же hash/ffprobe/upload/video attributes, что publisher; `new_entities` – тот же UTF-16 формат entities. До первой загрузки проверяются обе строки, локальные hash/metadata, текущий аккаунт, группа/топик, авторство, точные ID/родитель/старые caption hash/media ID, имя/MIME/размер/длительность и обычный audio attribute без voice/video. После загрузки проверки локальных байтов, аккаунта/топика и исходного сообщения повторяются. Новый caption задаётся явно, без переноса прежних offsets.

Dry-run не загружает, не редактирует и не пишет receipts. Apply сохраняет приватный `<manifest>.receipts.json` до edit; каждый edit вызывает ровно один paced RPC, без автоматического retry. После любого результата RPC выполняется readback того же ID: точные video metadata, caption/entities, sender, topic и прежний reply parent. `complete=true` требует подтверждения всех строк. Если сервер уже возвращает ожидаемое video, повторный запуск только проверяет его. Ошибка с неизвестным исходом оставляет `pending` receipt; если сервер всё ещё возвращает старое audio, автоматический повтор запрещён до ручной сверки. Если уже возвращает точное новое video, apply завершает receipt без нового upload/edit. Сохраняй manifest/receipts вместе вне Git и не очищай journal ради повторной попытки. Публикационные receipts исходного audio сохраняются как историческое подтверждение; прежний `publish` manifest после замены повторно не применяй.

Адресная проверка без живого Telegram: `go test ./internal/mtproto ./cmd/telegram-harvest -run 'TestInterviewTopicReplaceVideo|TestInterviewTopicPublish' -count=1`. В тестах локальный `ffmpeg/ffprobe` создаёт короткий MP4; проверены оба ID, dry-run, точный edit без send/delete, readback/recovery, остановка unknown outcome и изменение исходного сообщения во время upload.

## Отправка в «Избранное»

Когда пользователь просит отправить что-либо «мне», «в Telegram» или «в Избранное», используется только сессия `@Pheik13` и только self-peer:

```bash
bin/telegram-harvest --profile main send-saved --text 'Текст сообщения'
bin/telegram-harvest --profile main send-saved --file '/absolute/path/Документ.pdf' --caption 'Памятка'
bin/telegram-harvest --profile main send-saved --from-chat 3978717463 --message-id 14787
```

Команда не имеет флага адресата и не умеет отправлять другим пользователям или чатам. Профиль `study` отклоняется до подключения, а профиль `main` перед отправкой сверяется с username `@Pheik13`. После отправки сообщение читается обратно из `InputPeerSelf`; для локального документа сверяются имя файла, MIME-тип и размер.

Режим `--from-chat ... --message-id ...` копирует одно уже существующее Telegram-видео в self-chat. Исходные байты и серверное превью временно скачиваются, затем файл без перекодирования загружается как Telegram video с исходной подписью, entities, именем, MIME-типом и video attributes; временные файлы удаляются. Этот путь работает и для source chats, где нативная пересылка запрещена. Перед записью команда требует скачиваемое серверное превью, а после записи проверяет подпись, имя, MIME-тип, размер, длительность, разрешение, аудиопризнак, streaming attribute и наличие превью. `--json` возвращает SHA-256 исходного файла, source/destination media IDs, проверенный readback и отдельный объект `copy` с доказательствами сохранения медиа.

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
| Preview history limit | 100 | Только обычный `dump`; `complete=false` означает ограниченную выборку. `dump --all` и `sync` читают весь заданный диапазон. |

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

Канонический production backend — `whisper.cpp large-v3-turbo q5_0 + Metal`, четыре CPU helper threads и `beam_size=5`. Telegram, локальный `transcribe-file` и OBS используют один публичный `adaptive-media-v2`: режим выбирает Harvest после фактического WAV и Silero bounds. CLI позволяет менять только локальные пути к server/model/gate/ffmpeg и включать или выключать транскрибацию.

Тот же production-профиль доступен локальным автоматизациям без Telegram RPC:

```bash
bin/telegram-harvest --profile main transcribe-file \
  --input /absolute/path/recording.mp4 \
  --output /absolute/path/transcript.txt
```

Команда пишет plain UTF-8 transcript в `--output`, а в stdout — ASR contract v4 с `profile_id=adaptive-media-v2`, `validation_status`, выбранными `strategy`/`route_reason`, backend descriptor, diagnostics и timings. `transcribe-file --check` проверяет runtime/model paths без обработки медиа и возвращает `runtime-ready`. OBS доверяет только этому публичному контракту; Whisper, VAD, Metal/decode, language/prompt и post-filter настраиваются только в `internal/transcribe`.

Медиа короче 180 секунд, где речь начинается раньше 10 секунд, использует быстрый `no_timestamps` short decode с автоопределением языка. Длительность от 180 секунд либо leading silence от 10 секунд автоматически включает long-form: bounded Silero находит границы речи, сохраняется секундный lead-in, физический 15-секундный WAV probe определяет язык, а затем выполняется один timestamped decode. Русский получает punctuation seed `Да. Нет? Хорошо! Пожалуйста, продолжайте.` без carry; английский и остальные языки — без prompt.

Если корректный timestamped ответ не покрывает последнюю Silero-речь с допуском 2 секунды, Harvest один раз повторно распознаёт хвост на той же загруженной модели. Интервал начинается на границе целого сегмента с контекстом до 20 секунд и заканчивается через секунду после последней речи; предел — 45 секунд. Повтор использует выбранный язык без prompt и заменяет только конечные сегменты. Текст предыдущих сегментов сохраняется; совпадение текста сегментов с полным ответом проверяется до замены. Повтор и итог проходят прежние проверки длительности, timestamps, покрытия и повторов. Неполный повтор или структурная ошибка остаются отказом. Diagnostics сохраняют `tail_recovery_applied`, начало повторного интервала и исходный разрыв покрытия; параметры восстановления входят в идентичность кэша.

Для long-form Harvest проверяет duration, монотонность timestamps, достижение последней Silero-речи и отсутствие только явного exact-token цикла (минимум пять повторов и 20 токенов). Если whisper.cpp округляет конец последнего пересекающего аудио сегмента до границы внутреннего 30-секундного окна, Harvest ограничивает его реальной длительностью PCM; остальные выходы timestamps за аудио отклоняются. Допустимые речевые повторы не переписываются; перед проверкой extreme-loop каждый точный цикл, включая цикл внутри одного сегмента и перед содержательным продолжением, схлопывается до первого блока, а остальной текст сохраняется. `coverage-validated` доказывает структуру, хвост и отсутствие обнаруженного extreme loop, но не WER/CER. Все routing thresholds, prompt и decode settings входят в descriptor/cache identity.

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

Daily использует один GPU worker, `beam_size=5` и Silero с threshold `0.5`, minimum speech `250 ms`, minimum silence `100 ms`, padding `30 ms`. Для короткого сообщения Silero определяет границы речи, а исходный WAV целиком отправляется в Whisper. Для длинного аудио или позднего начала речи найденные границы задают обрезку вступительной тишины и проверку достижения последней речи. Качество текста и скорость подтверждаются сравнением на одинаковых входах. Известные точные terminal boilerplate-фразы (`Продолжение следует`, `Субтитры сделал DimaTorzok` и подобные) удаляются только отдельной последней строкой.

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
bin/telegram-harvest --profile main chats --all --json
# Эквивалент через Make: make chats PROFILE=main ALL=1
```

`chats --all` последовательно читает обычную и архивную папки до подтверждённого конца и убирает дубли по типу/ID чата. Фильтр `--query` применяется после полного обхода; `--all` нельзя сочетать с `--limit`. Без `--all` сохраняется диагностическая выборка `--limit` (по умолчанию 300), которая не подтверждает полный охват архива. JSON остаётся массивом чатов; папка каждого чата в нём не хранится.

Если `TG_HARVEST_STUDY_ALLOWED_CHATS` задан, `chats` (включая `--all`), `topics`, `dump` и `sync` работают только в этом scope.

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

## Карьерные чаты: обновление по запросу

По поручению Игоря от 06.10.2026 агент подкачивает эти источники по запросу «обнови/подкачай чаты» и перед поиском свежей карьерной информации. Отдельное разрешение на чтение и локальную синхронизацию не требуется. Выбирай нужный источник; при общем запросе обновляй обе Mafia и все зарегистрированные ОМ. Вступления и публикации Игорь выполняет вручную.

| Источник | Профиль и охват | Состояние и вход в поиск |
| --- | --- | --- |
| GO MAFIA 2.0, `2651305316` | `main`, вся доступная история | `.state/daily/go-mafia.state.json`; `.state/daily/go-mafia-agent-view/README.md` |
| ВУ МАФИЯ, `3713172942` | `main`, вся доступная история | `.state/daily/vu-mafia.state.json`; `.state/daily/vu-mafia-agent-view/README.md` |
| ОМ, 27 зарегистрированных чатов | `study`, с 01.01.2026 по Москве | `.state/career-study/collection-plan.json`; `.state/career-study/agent-view/README.md` |

Реестр ОМ — существующий приватный `collection-plan.json`: поля `sources[].id`, `name`, `title`. Перед проходом сверяй ID с соответствующим `<name>.state.json`; расхождение исправляй до записи. `boosty-bot` остаётся в объединённом поиске, но в обновление 27 ОМ не входит.

Один источник Mafia обновляется без сброса. Подставь ID, имя потока и каталог из таблицы; `REFRESH_DATE` — московская дата семь дней назад:

```bash
cd '/Users/igor/projects/telegram-harvest'
bin/telegram-harvest --profile main sync --chat CHAT_ID --name STREAM \
  --refresh-from REFRESH_DATE
bin/telegram-harvest --profile main agent-view \
  --in /Users/igor/projects/telegram-harvest/.state/daily/STREAM.jsonl \
  --out-dir /Users/igor/projects/telegram-harvest/.state/daily/STREAM-agent-view
```

Для ОМ читай реестр и последовательно вызывай этот `sync` для каждого выбранного источника. При общей подкачке проходят все 27; после успешного прохода представление пересобирается один раз:

```bash
TG_HARVEST_STUDY_STATE_DIR=/Users/igor/projects/telegram-harvest/.state/career-study \
bin/telegram-harvest --profile study sync --chat CHAT_ID --name STREAM \
  --refresh-from REFRESH_DATE \
  --merged-out /Users/igor/projects/telegram-harvest/.state/career-study/messages.jsonl
bin/telegram-harvest --profile study agent-view \
  --in /Users/igor/projects/telegram-harvest/.state/career-study/messages.jsonl \
  --out-dir /Users/igor/projects/telegram-harvest/.state/career-study/agent-view \
  --since 2026-01-01
```

`sync` дочитывает все новые ID от сохранённой позиции, даже если прошлый запуск был раньше `REFRESH_DATE`; семь дней задают дополнительную сверку правок. Более старую нужную правку проверяй адресно, расширив дату. Историю и state при обычной подкачке не сбрасывай. Полный первый backfill нового потока требует `--all --reset`; незавершённый backfill продолжай с `--all`, без `--reset`.

На ошибке прерви текущий проход, сохрани возобновляемые файлы и покажи неполный охват. Соблюдай FloodWait и блокировку сессии; не запускай конкурирующую историю одним профилем. Готовность подтверждают завершённый `sync` с `complete=true`, обновлённый `last_sync_at` и успешный `agent-view`. Проверь дату сборки, диапазон и поиск известного сообщения по теме. Медиа автоматически не скачиваются: архив содержит текст, темы, ссылки и метаданные вложений.

Когда Игорь сообщает о новом чате ОМ, перечисли все доступные диалоги `study` с `--limit 10000`, сопоставь префикс `ОМ:` с реестром и добавь отсутствующие источники в приватный allowlist и план. Собери их с 01.01.2026 без сброса прежних потоков. Для нового Mafia-чата сперва подтверди доступ и числовой ID. Канонический карьерный маршрут — `obsidian/money/03_Работа/00_Поиск работы/Стратегия поиска работы.md`.

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
make verify
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
| `internal/runlock` | Блокировка сессии через `*.runtime.lock` и общих входов/выходов через `*.harvest.lock` по каноническим путям. Файлы блокировок сохраняются между запусками; независимые ресурсы не блокируют друг друга. [Владение и восстановление](docs/collection-integrity.md). |
| `reports/daily` | Локальные Markdown-отчеты для пользователя, ignored by git. |

## Границы доступа

- Сбор читает Telegram. По прямой просьбе Игоря доступны пять узких операций с серверной проверкой результата: [`main send-saved`](#отправка-в-избранное) в собственное «Избранное» `@Pheik13`, [`account-organize --apply`](#организация-рабочих-диалогов) для завершённых рабочих диалогов и [`interview-topic-update --apply`](#правка-материалов-в-топике-собеседований) для собственных материалов в закреплённом топике и [`interview-topic-publish --apply`](#публикация-новых-интервью-в-закреплённый-топик) для новых собственных интервью там же, а также [`interview-topic-replace-video --apply`](#замена-двух-аудиопостов-собеседований-на-видео) для замены audio IDs `2362`/`2365` на video в тех же сообщениях. Получатели, действия и проверки ограничены контрактом каждой команды; остальные операции записи не поддерживаются.
- Broad daily scan пишет только outgoing/self messages и явно настроенных sender IDs строго в их настроенных chat IDs.
- Study scope ограничивается `TG_HARVEST_STUDY_ALLOWED_CHATS`, когда allowlist задан.
- `.env`, `.sessions/`, `.state/`, `reports/`, `models/`, `bin/`, дефолтные `chat.jsonl`/`media-manual` и generated views приватные и не коммитятся.
- Live Telegram поведение проверяется вручную после логина; автоматические тесты покрывают локальную логику, config, state, rendering и helpers.
