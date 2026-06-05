<h1 align="center">telegram-harvest</h1>

<p align="center">
  Read-only MTProto harvester for study automation and daily personal Telegram context.
</p>

<p align="center">
  <img alt="Go" src="https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white">
  <img alt="Telegram" src="https://img.shields.io/badge/Telegram-MTProto-26A5E4?logo=telegram&logoColor=white">
  <img alt="JSONL" src="https://img.shields.io/badge/output-JSONL-111111">
  <img alt="Read only" src="https://img.shields.io/badge/default-read--only-0E7C7B">
</p>

<p align="center">
  <a href="#why">Why</a> ·
  <a href="#quick-start">Quick start</a> ·
  <a href="#daily-harvest">Daily harvest</a> ·
  <a href="#agent-views">Agent views</a> ·
  <a href="#safety">Safety</a> ·
  <a href="#repository-map">Repository map</a>
</p>

## Why

`telegram-harvest` is the current CLI path for Telegram Harvest. It reads
Telegram through MTProto user authorization and writes local data for downstream agents.

Study commands stay scoped by an explicit allowlist. Daily commands use the main harvest account
configuration and export only messages sent by the authorized user. The tool does not send messages, click
buttons, delete content, join chats, pin/unpin messages, or mark chats read.

| Capability | What it gives |
| --- | --- |
| Study mode | Existing `dump`, `sync`, `topics`, `agent-view`, and `compact` flows use `TG_HARVEST_STUDY_*`. |
| Daily mode | `daily` scans dialogs sequentially with `TG_HARVEST_*` and exports only outgoing/self messages for one day. |
| Allowlisted reads | Study commands refuse chats outside the study allowlist when it is set. |
| Resumable sync | Full backfills can resume from `backfill.next_offset_id` after interruption. |
| JSONL source layer | Every message record keeps chat, message id, date, sender, text, links, attachments, and optional local attachment paths. |
| Daily media | Daily harvest saves photos/image documents, transcribes voice/audio/video through local Vosk, and keeps only transcript cache for audio/video. |
| Topic awareness | Forum chats preserve `topic` and `thread_top_message_id`. |
| Compact agent views | Markdown navigation and TOON-style summaries are generated from the same JSONL source. |
| Conservative pacing | RPC calls are sequential, spaced, and flood-wait aware. |

## Quick Start

```bash
cd telegram-harvest
cp .env.example .env
make setup
make daily-doctor
make daily-login
```

For fresh daily login, create Telegram app credentials at <https://my.telegram.org>. For study mode,
configure `TG_HARVEST_STUDY_*`, then run `make doctor` and `make login`.
If Telegram Desktop is already logged in locally, you can import its `tdata` session for study mode
instead:

```bash
go run ./cmd/telegram-harvest import-tdesktop --account-index 1
go run ./cmd/telegram-harvest me
```

The CLI auto-loads `.env` from the current directory and the project root. Keep the allowlist narrow:

```dotenv
TG_HARVEST_STUDY_STATE_DIR=.state
TG_HARVEST_STUDY_ALLOWED_CHATS=1234567890,@study_chat
```

Useful commands:

```bash
make help
make test
make doctor
make chats QUERY=study
make topics CHAT=1234567890
```

## Daily Harvest

Daily harvest is intended for the main-account session. It does not use the Telegram Desktop
test-app fallback; configure app credentials from <https://my.telegram.org> with `TG_HARVEST_*`:

```dotenv
TG_HARVEST_APP_ID=12345678
TG_HARVEST_APP_HASH=your_main_account_app_hash
TG_HARVEST_PHONE=+10000000000
TG_HARVEST_SESSION_PATH=.sessions/daily.json
TG_HARVEST_STATE_DIR=.state/daily
```

Login and verify:

```bash
go run ./cmd/telegram-harvest daily-login
go run ./cmd/telegram-harvest daily-doctor
go run ./cmd/telegram-harvest daily-me
```

Export a day. Relative output paths live under `TG_HARVEST_STATE_DIR`:

```bash
go run ./cmd/telegram-harvest daily --date yesterday
```

Default outputs:

```text
.state/daily/days/YYYY-MM-DD.jsonl
.state/daily/days/YYYY-MM-DD.md
.state/daily/media/...
.state/daily/transcripts/cache/...
```

The Markdown file is the agent-readable daily surface: each line shows local time, destination chat,
message text or media kind, Telegram message links when Telegram can produce them, saved image paths,
and transcripts when available. Photos and image documents are kept under `media/`. Voice, audio,
round video, and regular video are downloaded to a temporary file, converted with `ffmpeg`, transcribed,
then deleted; the report keeps only transcript text and `transcript_path`.

Vosk is the default transcription path. On this Mac the project auto-detects the Shelfy small Russian
model at `../shelfy/models/vosk-model-small-ru-0.22` when it exists, and uses `vosk-transcribe` from
`PATH` when available:

```dotenv
TG_HARVEST_VOSK_COMMAND=/usr/local/bin/vosk-transcribe
TG_HARVEST_VOSK_MODEL_PATH=~/projects/shelfy/models/vosk-model-small-ru-0.22
TG_HARVEST_FFMPEG_COMMAND=ffmpeg
TG_HARVEST_RETENTION_DAYS=14
```

`daily-doctor` reports `daily_ffmpeg_status`, `daily_vosk_command_status`, and
`daily_vosk_model_status`. Vosk itself is CPU-based; `ffmpeg` handles audio extraction/conversion.
Transcript cache is keyed by Telegram media id when available, so reruns skip already-transcribed
audio/video without downloading it again.

A custom command-template hook can override Vosk; placeholders are shell-quoted by the CLI:

```dotenv
TG_HARVEST_TRANSCRIBE_CMD=whisper-cli --language ru --input {input} --output {output}
```

The command may either write `{output}` or print transcript text to stdout. Use
`--download-media=false` to skip media downloads/transcription, `--transcribe=false` to skip
audio/video transcription, or `--retain-days` to change the default two-week daily buffer.

Daily media caps are intentionally conservative:

| Kind | Default cap | Behavior |
| --- | ---: | --- |
| Photo or image document | 10 MiB | Saved under `media/` when inside cap. |
| Generic document | 10 MiB | Saved under `media/` when inside cap. |
| Voice/audio | 50 MiB | Temporarily downloaded for transcription when inside cap. |
| Video/round video | 200 MiB | Temporarily downloaded for transcription when inside cap. |

If a file exceeds its cap, the JSONL/Markdown record keeps `download_error` and a `download_hint`
with the exact manual command. Manual downloads are explicit one-off reads and do not apply these
caps:

```bash
go run ./cmd/telegram-harvest daily-download-media \
  --chat 1234567890 \
  --message-id 777 \
  --index 1 \
  --out-dir media-manual
```

## Sync

Start a full history rebuild with `--all --reset`. Use `--reset-merged` only on the first stream
when rebuilding a shared `messages.jsonl`:

```bash
go run ./cmd/telegram-harvest sync \
  --chat 1234567890 \
  --name study-main \
  --all \
  --reset \
  --reset-merged \
  --batch-size 100 \
  --merged-out messages.jsonl
```

If interrupted, rerun without `--reset`:

```bash
go run ./cmd/telegram-harvest sync \
  --chat 1234567890 \
  --name study-main \
  --all \
  --batch-size 100 \
  --merged-out messages.jsonl
```

Weekly incremental sync omits `--all` and uses `last_id`:

```bash
go run ./cmd/telegram-harvest sync \
  --chat 1234567890 \
  --name study-main \
  --merged-out messages.jsonl
```

Study dump locations are controlled by `TG_HARVEST_STUDY_STATE_DIR`, which defaults to `.state`.
Typical files are:

```text
.state/<name>.jsonl
.state/<name>.state.json
.state/messages.jsonl
.state/agent-view/
.state/messages.toon
.state/media/
```

When `--download-media` is enabled, study `dump` and `sync` use the same caps as daily. If a skipped
file is needed later, use the study-account manual download command:

```bash
go run ./cmd/telegram-harvest download-media \
  --chat 1234567890 \
  --message-id 777 \
  --index 1 \
  --out-dir media-manual
```

## Agent Views

JSONL is the canonical lossless dump. Compact files are derived and can be regenerated:

```bash
go run ./cmd/telegram-harvest agent-view --in messages.jsonl --out-dir agent-view
go run ./cmd/telegram-harvest compact --in messages.jsonl --out messages.toon
```

`agent-view/README.md` is the default agent entrypoint. Generated `agent-view/AGENTS.md` contains
the local reading rules for agents. The generated view points to `all-recent.md`, chat indexes,
topic indexes, and day files:

```text
agent-view/
  README.md
  all-recent.md
  chats/chat-1234567890/README.md
  chats/chat-1234567890/topics/topic-3/2026-05-12.md
```

Normal agent read path:

1. Open `agent-view/README.md`.
2. Use `all-recent.md` only for vague or latest-message questions.
3. If the chat or subject is known, search inside one chat/topic directory before opening files.
4. Open one topic README, then one `YYYY-MM-DD.md` day file.
5. Use raw JSONL only when Markdown lacks a field needed for audit/debug.

After sync, refresh generated study-agent files:

```bash
make refresh-agent-view
```

## Safety

- `.env`, `.sessions/`, `.state/`, generated chat dumps, and local binaries are ignored by git.
- `sync`, `dump`, and `daily` cap automatic media downloads by kind. Downloaded files stay in private local state and are referenced from JSONL/agent views through `local_path`; skipped files include a manual download hint.
- Study reads are constrained by `TG_HARVEST_STUDY_ALLOWED_CHATS` when set.
- Daily full-account scans are limited to outgoing/self messages and should use `.state/daily` or another private state directory.
- Full-account broad dumps of other people's messages do not belong in this repository.
- Live Telegram access is validated manually; automated tests use local fixtures.

## Testing

```bash
make test
make fmt
```

## Repository Map

| Path | Purpose |
| --- | --- |
| `cmd/telegram-harvest` | CLI entrypoint and command wiring. |
| `internal/config` | Env loading, study and daily mode defaults, and study-chat allowlist. |
| `internal/mtproto` | Telegram transport, Telegram Desktop import, history, topic, and daily outgoing reads. |
| `internal/harvest` | JSONL model, sync state, resumable backfill, compact views, daily Markdown, and agent views. |
| `internal/runlock` | Per-session runtime lock. |

## Vosk Runtime Options

The harvester calls an external `vosk-transcribe` command after `ffmpeg` has produced mono 16 kHz
PCM WAV. Practical options:

| Runtime | Tradeoff |
| --- | --- |
| Native macOS helper | Best per-file latency after setup. Requires a macOS-compatible Vosk library and helper binary, or an official Python Vosk wrapper exposed through `TG_HARVEST_TRANSCRIBE_CMD`. |
| Shelfy Docker runtime | Reuses Shelfy's Linux/cgo `vosk-transcribe` stack, but each short transcription pays Docker/container overhead unless a long-lived service is added. |
| Custom command hook | Useful for `vosk-transcriber`, Whisper, or another local ASR; set `TG_HARVEST_TRANSCRIBE_CMD` and keep the same cache/delete behavior. |

For frequent daily runs, native macOS helper or a long-lived local ASR service is better than spawning
Docker for every voice/video. Docker is acceptable for occasional checks, but startup and volume mounts
will dominate short voice messages.
