# telegram-study-harvest

Read-only Telegram study chat harvester built on MTProto user authorization.

It is a local data-source wrapper for the HSE study automation loop. Its runtime scope is the
configured study-chat allowlist, not the whole Telegram account. It does not send messages, click
buttons, join chats, delete content, or mark chats read.

## Setup

```bash
cd telegram-study-harvest
cp .env.example .env
make setup
make doctor
make login
```

You need Telegram app credentials from <https://my.telegram.org> for fresh code login. If Telegram
Desktop is already logged in locally, you can import its `tdata` session instead:

```bash
go run ./cmd/telegram-study-harvest import-tdesktop --account-index 1
go run ./cmd/telegram-study-harvest me
```

The CLI auto-loads `.env` from the current directory and the project root. In this local setup,
`TG_STUDY_ALLOWED_CHATS` should contain only the study chat IDs or usernames you explicitly allow:

```dotenv
TG_STUDY_STATE_DIR=.state
TG_STUDY_ALLOWED_CHATS=1234567890,@study_chat
```

## Commands

```bash
make help
make setup
make test
make doctor
make chats QUERY=вшэ
make topics CHAT=1234567890
make compact
make agent-view
make refresh-agent-view
```

The direct CLI is still available when a command needs flags not wrapped by `make`:

```bash
go run ./cmd/telegram-study-harvest print-config
go run ./cmd/telegram-study-harvest me
go run ./cmd/telegram-study-harvest sync --chat 1234567890 --name study-main --merged-out messages.jsonl
go run ./cmd/telegram-study-harvest compact --in messages.jsonl --out messages.toon
go run ./cmd/telegram-study-harvest agent-view --in messages.jsonl --out-dir agent-view
make refresh-agent-view
```

Sync writes under `TG_STUDY_STATE_DIR`:

- `<state>/<name>.jsonl` — appended Telegram message records;
- `<state>/<name>.state.json` — last synced message id and metadata;
- `<state>/messages.jsonl` — optional merged stream when using `--merged-out`.
- `<state>/messages.toon` — rebuildable compact agent view generated from `messages.jsonl`.
- `<state>/agent-view/README.md` — Markdown navigation for agents.

Raw JSONL is the append-only storage layer. `messages.toon` and `agent-view/` are derived files.
`agent-view` updates incrementally from the last processed JSONL byte offset when possible; pass
`--rebuild` to force a full rewrite.

For the full agent reading protocol, see [docs/agent-navigation.md](docs/agent-navigation.md).

## Full Backfill and Resume

Start a full history rebuild with `--all --reset`. Use `--reset-merged` only on the first stream
when rebuilding a shared `messages.jsonl`:

```bash
go run ./cmd/telegram-study-harvest sync --chat 1234567890 --name study-main --all --reset --reset-merged --batch-size 100 --merged-out messages.jsonl
go run ./cmd/telegram-study-harvest sync --chat @study_chat --name study-chat --all --reset --batch-size 100 --merged-out messages.jsonl
```

Full sync prints progress after each committed batch. If it is interrupted, the state file keeps
`backfill.next_offset_id`; continue without `--reset`:

```bash
go run ./cmd/telegram-study-harvest sync --chat 1234567890 --name study-main --all --batch-size 100 --merged-out messages.jsonl
```

After `complete=true`, weekly incremental sync uses `last_id`:

```bash
go run ./cmd/telegram-study-harvest sync --chat 1234567890 --name study-main --merged-out messages.jsonl
```

Forum chats include topic metadata on message records. Service messages that Telegram exposes
without any topic/reply context can remain topic-less.

## JSONL Contract

JSONL is the canonical lossless dump. Keep automation, tests, and future transforms pointed at
JSONL first. For agent context windows, update Markdown navigation and, when useful, a compact
TOON-style table from the same data:

```bash
go run ./cmd/telegram-study-harvest agent-view --in messages.jsonl --out-dir agent-view
go run ./cmd/telegram-study-harvest agent-view --in messages.jsonl --out-dir agent-view --rebuild
go run ./cmd/telegram-study-harvest compact --in messages.jsonl --out messages.toon
go run ./cmd/telegram-study-harvest compact --in messages.jsonl --out recent.toon --since 2026-05-01 --limit 500
```

`make refresh-agent-view` runs both the Markdown navigation update and `messages.toon` generation
with the default paths.

`agent-view/README.md` is the default entrypoint for agents. It points to `all-recent.md`, then to
per-chat, per-topic, per-day files:

```text
agent-view/
  README.md
  all-recent.md
  chats/chat-1234567890/README.md
  chats/chat-1234567890/topics/topic-3/2026-05-12.md
```

The Markdown view is optimized for context economy: it skips service messages by default and omits
JSON keys, reply ids, thread ids, views, raw Telegram URLs, and raw service actions. Each message
line keeps a short `#message_id` reference for source lookup; the chat/topic/day path supplies the
rest of the context.

Incremental `agent-view` state is stored in `<state>/agent-view/.agent-view-state.json`. If the raw
JSONL shrinks, the manifest is missing, options change, or generated indexes are missing, the command
falls back to a full rebuild automatically.

The compact view is sorted newest-first, skips Telegram service messages by default, preserves
`chat_id`, `message_id`, `topic_id`, sender, links, and attachment metadata, and can be regenerated
at any time. Use `--include-service` only when join/pin/service events are relevant.

Each message record is one JSON object:

```json
{
  "source": "telegram",
  "source_url": "https://t.me/c/123/456",
  "chat": {"id": 123, "type": "supergroup", "title": "Study chat"},
  "message_id": 456,
  "date": "2026-05-13T10:00:00Z",
  "sender": {"id": 789, "type": "user", "display": "Name"},
  "topic": {"id": 3, "title": "Seminars"},
  "thread_top_message_id": 3,
  "text": "message text",
  "links": ["https://example.com"],
  "attachments": [{"kind": "document", "file_name": "task.pdf", "mime_type": "application/pdf"}]
}
```

The harvester stores attachment metadata only. File download can be added later as an explicit
read-only command, but it is intentionally not part of the first sync path.

## Repository Layout

- `cmd/telegram-study-harvest` — CLI entrypoint and command wiring.
- `docs/agent-navigation.md` — rules for fast, low-context Telegram reads by agents.
- `internal/config` — env loading, defaults, and study-chat allowlist.
- `internal/mtproto` — Telegram transport, Telegram Desktop import, history/topic reads.
- `internal/harvest` — JSONL record model, sync state, resumable backfill logic, agent views.
- `internal/runlock` — per-session runtime lock.

Local private data is ignored by git:

- `.env`
- `.sessions/`
- `.state/`
- generated chat dumps and local binaries

## Limits

Defaults are conservative:

- one RPC at a time;
- `TG_STUDY_RPC_SPACING_MS=1500`;
- `TG_STUDY_HISTORY_BATCH_SIZE=80`;
- `TG_STUDY_HISTORY_LIMIT=500`;
- `TG_STUDY_MAX_BATCHES=20`;
- flood-wait responses are respected and retried up to a small limit.
