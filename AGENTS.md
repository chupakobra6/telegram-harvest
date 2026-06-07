# AGENTS.md

## Project Overview
- Local Go CLI for read-only Telegram harvesting through MTProto user authorization.
- The tool exports selected study chat data and daily outgoing personal context for downstream automation and agent reads.
- Keep runtime credentials, sessions, state, dumps, and generated agent views out of git.
- Study runtime scope is the configured study-chat allowlist; main-profile daily harvest scope is outgoing/self messages for one day under the main account config.

## Safety
- Read-only is a hard boundary: do not add commands that send messages, click buttons, delete messages, pin/unpin, join chats, mark chats read, or mutate Telegram state.
- Keep Telegram API calls sequential and paced. Do not add concurrent history crawlers.
- Treat `.env`, `.sessions/`, `.state/`, dumps, and chat exports as private local data.
- Never print app hashes, passwords, session data, or full phone numbers.
- Do not keep broad dumps of other people's messages in repo-local `.state/`; daily full-dialog scans must emit only outgoing/self records into the configured daily state directory.

## Commands
- Install/update dependencies: `go mod tidy`
- Format: `gofmt -w ./cmd ./internal`
- Tests: `go test ./...`
- Doctor: `go run ./cmd/telegram-harvest doctor`
- Login: `go run ./cmd/telegram-harvest login`
- Daily outgoing harvest: `go run ./cmd/telegram-harvest daily --date yesterday`
- List chats: `go run ./cmd/telegram-harvest chats --query вшэ`
- List forum topics: `go run ./cmd/telegram-harvest topics --chat <forum-id-or-username>`
- Dump chat: `go run ./cmd/telegram-harvest dump --chat <id-or-username> --out .state/chat.jsonl`
- Start full sync: `go run ./cmd/telegram-harvest sync --chat <id-or-username> --name hse-main --all --reset --batch-size 100`
- Resume interrupted full sync: rerun the same `sync --all` command without `--reset`; state keeps `backfill.next_offset_id`.
- Incremental sync after full sync completion: `go run ./cmd/telegram-harvest sync --chat <id-or-username> --name hse-main`
- Compact agent view: `go run ./cmd/telegram-harvest compact --in messages.jsonl --out messages.toon`
- Markdown navigation for agents: `go run ./cmd/telegram-harvest agent-view --in messages.jsonl --out-dir agent-view`; it updates incrementally when possible, pass `--rebuild` to force a full rewrite.

## Code Policy
- Prefer small, testable helpers for env loading, MTProto auth, runtime locks, and flood-wait handling.
- Keep JSONL output stable and source-rich: every record should include chat, message id, date, sender, text/media metadata, and Telegram source pointer.
- Treat `.toon` outputs as rebuildable agent views only; JSONL remains the canonical lossless dump.
- Treat `agent-view/README.md` as the first file agents should open. It points to `all-recent.md`, then chat/topic/day Markdown files so agents do not read raw JSONL by default.
- Keep `agent-view/.agent-view-state.json` private/generated; it tracks the processed JSONL byte offset for incremental updates and should not be edited by hand.
- When generated `agent-view` templates or manifest semantics change, bump `agentViewManifestVersion` and keep rebuild/noop/incremental tests aligned.
- Keep generated `agent-view/AGENTS.md` and `agent-view/README.md` aligned whenever changing the agent read path; they are the agent-facing navigation source of truth.
- For forum chats, preserve `topic` and `thread_top_message_id`; do not merge topic streams only by chat title.
- Main profile uses `TG_HARVEST_DAILY_*`. Study profile uses `TG_HARVEST_STUDY_*`. Do not add alternate env aliases.
- Both profiles use explicit Telegram API credentials and CLI `login`; do not read or import Telegram Desktop `tdata`.
- Study `dump` and `sync` do not transcribe audio/video. They save inspectable study materials such as photos, image documents, and generic documents; audio/video transcription is a daily-harvest feature only.
- Daily audio/video media is transcript-only: cache by Telegram media id when possible, delete temporary source media after transcription, and keep saved `local_path` only for images/documents agents need to inspect.
- Default media caps are deliberate: photo/image and generic documents 10 MiB, audio/voice 50 MiB, video/round-video 200 MiB. If a cap is exceeded, keep the skip reason and manual `download-media` hint in output.
- When changing CLI commands or flags, update CLI help, Makefile shortcuts, README/.env examples when relevant, and command tests in the same pass.
- Do not add backwards-compatibility command aliases, profile aliases, env aliases, shims, or fallback code paths unless the user explicitly requests them. If compatibility seems useful, raise it as a question first; otherwise remove the old path when replacing it.
- Add tests for parsing/config/state behavior; live Telegram behavior is validated manually after login.
