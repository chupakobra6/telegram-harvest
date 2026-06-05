# AGENTS.md

## Project Overview
- Local Go CLI for read-only Telegram harvesting through MTProto user authorization.
- The tool exports selected study chat data and daily outgoing personal context for downstream automation and agent reads.
- Keep runtime credentials, sessions, state, dumps, and generated agent views out of git.
- Study runtime scope is the configured study-chat allowlist; daily runtime scope is outgoing/self messages for one day under the daily profile.

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
- Doctor: `go run ./cmd/telegram-study-harvest doctor`
- Login: `go run ./cmd/telegram-study-harvest login`
- Daily doctor: `go run ./cmd/telegram-study-harvest daily-doctor`
- Daily login: `go run ./cmd/telegram-study-harvest daily-login`
- Daily outgoing harvest: `go run ./cmd/telegram-study-harvest daily --date yesterday`
- Import Telegram Desktop session: `go run ./cmd/telegram-study-harvest import-tdesktop --account-index <n>`, then verify with `go run ./cmd/telegram-study-harvest me`
- List chats: `go run ./cmd/telegram-study-harvest chats --query вшэ`
- List forum topics: `go run ./cmd/telegram-study-harvest topics --chat <forum-id-or-username>`
- Dump chat: `go run ./cmd/telegram-study-harvest dump --chat <id-or-username> --out .state/chat.jsonl`
- Start full sync: `go run ./cmd/telegram-study-harvest sync --chat <id-or-username> --name hse-main --all --reset --batch-size 100`
- Resume interrupted full sync: rerun the same `sync --all` command without `--reset`; state keeps `backfill.next_offset_id`.
- Incremental sync after full sync completion: `go run ./cmd/telegram-study-harvest sync --chat <id-or-username> --name hse-main`
- Compact agent view: `go run ./cmd/telegram-study-harvest compact --in messages.jsonl --out messages.toon`
- Markdown navigation for agents: `go run ./cmd/telegram-study-harvest agent-view --in messages.jsonl --out-dir agent-view`; it updates incrementally when possible, pass `--rebuild` to force a full rewrite.

## Code Policy
- Prefer small, testable helpers for env loading, MTProto auth, runtime locks, and flood-wait handling.
- Keep JSONL output stable and source-rich: every record should include chat, message id, date, sender, text/media metadata, and Telegram source pointer.
- Treat `.toon` outputs as rebuildable agent views only; JSONL remains the canonical lossless dump.
- Treat `agent-view/README.md` as the first file agents should open. It points to `all-recent.md`, then chat/topic/day Markdown files so agents do not read raw JSONL by default.
- Keep `agent-view/.agent-view-state.json` private/generated; it tracks the processed JSONL byte offset for incremental updates and should not be edited by hand.
- When generated `agent-view` templates or manifest semantics change, bump `agentViewManifestVersion` and keep rebuild/noop/incremental tests aligned.
- Keep `docs/agent-navigation.md` aligned with generated `agent-view/AGENTS.md` and `agent-view/README.md` whenever changing the agent read path.
- For forum chats, preserve `topic` and `thread_top_message_id`; do not merge topic streams only by chat title.
- Daily profile config uses `TG_DAILY_*` first, then `TG_HARVEST_*`; old study commands must keep `TG_STUDY_*` compatibility.
- Add tests for parsing/config/state behavior; live Telegram behavior is validated manually after login.
