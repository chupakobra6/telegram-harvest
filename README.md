<h1 align="center">telegram-study-harvest</h1>

<p align="center">
  Read-only MTProto harvester for selected study chats and compact agent views.
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
  <a href="#agent-views">Agent views</a> ·
  <a href="#safety">Safety</a> ·
  <a href="#repository-map">Repository map</a>
</p>

## Why

`telegram-study-harvest` reads selected Telegram study chats through MTProto user authorization and
writes local data for later assignment extraction, update checks, and low-context agent reads.

It is scoped by an explicit allowlist. The tool does not send messages, click buttons, delete
content, join chats, pin/unpin messages, or mark chats read.

| Capability | What it gives |
| --- | --- |
| Allowlisted reads | Commands refuse chats outside `TG_STUDY_ALLOWED_CHATS` when the allowlist is set. |
| Resumable sync | Full backfills can resume from `backfill.next_offset_id` after interruption. |
| JSONL source layer | Every message record keeps chat, message id, date, sender, text, links, attachments, and optional local attachment paths. |
| Topic awareness | Forum chats preserve `topic` and `thread_top_message_id`. |
| Compact agent views | Markdown navigation and TOON-style summaries are generated from the same JSONL source. |
| Conservative pacing | RPC calls are sequential, spaced, and flood-wait aware. |

## Quick Start

```bash
cd telegram-study-harvest
cp .env.example .env
make setup
make doctor
make login
```

For fresh login, create Telegram app credentials at <https://my.telegram.org>. If Telegram Desktop
is already logged in locally, you can import its `tdata` session instead:

```bash
go run ./cmd/telegram-study-harvest import-tdesktop --account-index 1
go run ./cmd/telegram-study-harvest me
```

The CLI auto-loads `.env` from the current directory and the project root. Keep the allowlist narrow:

```dotenv
TG_STUDY_STATE_DIR=.state
TG_STUDY_ALLOWED_CHATS=1234567890,@study_chat
```

Useful commands:

```bash
make help
make test
make doctor
make chats QUERY=study
make topics CHAT=1234567890
```

## Sync

Start a full history rebuild with `--all --reset`. Use `--reset-merged` only on the first stream
when rebuilding a shared `messages.jsonl`:

```bash
go run ./cmd/telegram-study-harvest sync \
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
go run ./cmd/telegram-study-harvest sync \
  --chat 1234567890 \
  --name study-main \
  --all \
  --batch-size 100 \
  --merged-out messages.jsonl
```

Weekly incremental sync omits `--all` and uses `last_id`:

```bash
go run ./cmd/telegram-study-harvest sync \
  --chat 1234567890 \
  --name study-main \
  --merged-out messages.jsonl
```

## Agent Views

JSONL is the canonical lossless dump. Compact files are derived and can be regenerated:

```bash
go run ./cmd/telegram-study-harvest agent-view --in messages.jsonl --out-dir agent-view
go run ./cmd/telegram-study-harvest compact --in messages.jsonl --out messages.toon
```

`agent-view/README.md` is the default agent entrypoint. It points to `all-recent.md`, chat indexes,
topic indexes, and day files:

```text
agent-view/
  README.md
  all-recent.md
  chats/chat-1234567890/README.md
  chats/chat-1234567890/topics/topic-3/2026-05-12.md
```

For the full read protocol, see [docs/agent-navigation.md](docs/agent-navigation.md).

## Safety

- `.env`, `.sessions/`, `.state/`, generated chat dumps, and local binaries are ignored by git.
- `sync` and `dump` can download supported photo/document attachments with `--download-media --media-dir <dir>`. Downloaded files stay in private local state and are referenced from JSONL/agent views through `local_path`.
- Runtime reads are constrained by `TG_STUDY_ALLOWED_CHATS` when set.
- Full-account broad dialog dumps do not belong in this repository.
- Live Telegram access is validated manually; automated tests use local fixtures.

## Testing

```bash
make test
make fmt
```

## Repository Map

| Path | Purpose |
| --- | --- |
| `cmd/telegram-study-harvest` | CLI entrypoint and command wiring. |
| `docs/agent-navigation.md` | Rules for fast, low-context Telegram reads by agents. |
| `internal/config` | Env loading, defaults, and study-chat allowlist. |
| `internal/mtproto` | Telegram transport, Telegram Desktop import, history and topic reads. |
| `internal/harvest` | JSONL model, sync state, resumable backfill, compact views, and Markdown agent views. |
| `internal/runlock` | Per-session runtime lock. |
