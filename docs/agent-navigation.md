# Telegram Agent Navigation

This document describes how study agents should read Telegram data produced by
`telegram-study-harvest`.

## Data Layers

Telegram data has three layers:

- Raw streams: `<state>/<name>.jsonl` and `<state>/messages.jsonl`.
- Markdown navigation: `<state>/agent-view/`.
- Optional compact table: `<state>/messages.toon`.

Raw JSONL is the canonical append-only source. Agents should not read it first because it contains
JSON keys and Telegram metadata that are expensive in context. `agent-view/` is the default reading
surface.

## Agent Read Path

Use this path for normal study-task extraction:

1. Run or inspect `tools/study-data inspect telegram`.
2. Open `<state>/agent-view/README.md`.
3. If the task is current or vague, open `<state>/agent-view/all-recent.md`.
4. Open one chat README from the table.
5. Open one topic README.
6. Open one `YYYY-MM-DD.md` file.

Only open `messages.jsonl` when the Markdown view lacks a field needed for audit/debug.

## Fast Search

Search Markdown first:

```bash
cd .state/agent-view
rg -n "дедлайн|deadline|домаш|дз|задал|сдать|экзамен|зачет|SmartLMS" .
```

When the chat or topic is known, narrow the search before reading:

```bash
rg -n "сдать|дз|deadline" chats/chat-1234567890
rg -n "модуль|контрольная|домаш" chats/chat-1234567890/topics/topic-100
```

## Source References

Every visible message line ends with `#message_id`. Cite Telegram facts as:

```text
agent-view/chats/chat-1234567890/topics/topic-100/2026-05-12.md #456
```

The path gives chat, topic, and date. The id points back to the raw JSONL record if audit is needed.

## Token Policy

The Markdown view intentionally omits:

- JSON keys;
- reply and thread ids;
- views;
- raw Telegram URLs;
- raw service actions;
- service messages by default.

It keeps:

- local message time in Europe/Moscow;
- chat/topic context through the file path and headings;
- sender display;
- text;
- links;
- attachment kind, file name, and local path when the wrapper downloaded the file;
- `#message_id` source reference.

## Updates

After Telegram sync, update agent files:

```bash
cd telegram-study-harvest
make refresh-agent-view
```

Equivalent explicit commands:

```bash
go run ./cmd/telegram-study-harvest agent-view --in messages.jsonl --out-dir agent-view
go run ./cmd/telegram-study-harvest compact --in messages.jsonl --out messages.toon
```

`agent-view` stores `<state>/agent-view/.agent-view-state.json` with the processed JSONL byte offset.
Normal runs read only the appended tail and update affected day files plus indexes. The command falls
back to a full rebuild when the manifest is missing, the JSONL shrank, options changed, or generated
index files are missing.

Force a full rewrite when needed:

```bash
go run ./cmd/telegram-study-harvest agent-view --in messages.jsonl --out-dir agent-view --rebuild
```

`agent-view/` is generated. Do not edit generated Markdown or `.agent-view-state.json` by hand.
