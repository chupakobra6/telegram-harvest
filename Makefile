CLI := go run ./cmd/telegram-harvest
PROFILE ?=
PROFILE_ARG = $(if $(strip $(PROFILE)), --profile "$(PROFILE)",)
MEDIA_LIMIT_FLAGS = \
	$(if $(strip $(MAX_PHOTO_BYTES)),--max-photo-bytes "$(MAX_PHOTO_BYTES)",) \
	$(if $(strip $(MAX_DOCUMENT_BYTES)),--max-document-bytes "$(MAX_DOCUMENT_BYTES)",) \
	$(if $(strip $(MAX_AUDIO_BYTES)),--max-audio-bytes "$(MAX_AUDIO_BYTES)",) \
	$(if $(strip $(MAX_VIDEO_BYTES)),--max-video-bytes "$(MAX_VIDEO_BYTES)",)

.DEFAULT_GOAL := help

.PHONY: help setup fmt test vosk-transcribe doctor login daily daily-download-media chats topics dump sync download-media compact agent-view refresh-agent-view clean

help:
	@printf "Available commands:\\n"
	@printf "  make setup   # go mod tidy\\n"
	@printf "  make fmt     # gofmt project files\\n"
	@printf "  make test    # go test ./...\\n"
	@printf "  make vosk-transcribe # build local Vosk helper into bin/\\n"
	@printf "  make doctor  # show config/session status; pass PROFILE=study|main to override CLI default\\n"
	@printf "  make login   # create MTProto user session; pass PROFILE=study|main to override CLI default\\n"
	@printf "  make daily   # export outgoing/self messages for DATE=today|yesterday|YYYY-MM-DD\\n"
	@printf "  make daily-download-media # manual uncapped daily media fetch; CHAT=... MESSAGE_ID=...\\n"
	@printf "  make chats   # list allowed study dialogs; pass QUERY='вшэ' to filter\\n"
	@printf "  make topics  # list topics for CHAT=<allowed forum id>\\n"
	@printf "  make dump    # dump allowed study chat; CHAT=... OUT=...\\n"
	@printf "  make sync    # incremental sync for CHAT=... NAME=...\\n"
	@printf "  make download-media # manual uncapped study media fetch; CHAT=... MESSAGE_ID=...\\n"
	@printf "  make compact # generate messages.toon from messages.jsonl\\n"
	@printf "  make agent-view # update Markdown navigation for agents\\n"
	@printf "  make refresh-agent-view # update Markdown navigation and messages.toon\\n"
	@printf "  make clean   # remove generated local artifacts except .env and session\\n"

setup:
	go mod tidy

fmt:
	gofmt -w ./cmd ./internal

test:
	go test ./...

vosk-transcribe:
	go build -o bin/vosk-transcribe ./cmd/vosk-transcribe

doctor:
	$(CLI)$(PROFILE_ARG) doctor

login:
	$(CLI)$(PROFILE_ARG) login

daily:
	$(CLI)$(PROFILE_ARG) daily --date "$(or $(DATE),today)" $(if $(strip $(OUT)),--out "$(OUT)",) $(if $(strip $(MARKDOWN_OUT)),--markdown-out "$(MARKDOWN_OUT)",) $(if $(strip $(DIALOG_LIMIT)),--dialog-limit "$(DIALOG_LIMIT)",) $(if $(strip $(DOWNLOAD_MEDIA)),--download-media="$(DOWNLOAD_MEDIA)",) $(if $(strip $(MEDIA_DIR)),--media-dir "$(MEDIA_DIR)",) $(MEDIA_LIMIT_FLAGS) $(if $(strip $(TRANSCRIBE)),--transcribe="$(TRANSCRIBE)",) $(if $(strip $(RETAIN_DAYS)),--retain-days "$(RETAIN_DAYS)",) $(if $(strip $(PROGRESS)),--progress,)

daily-download-media:
	$(CLI)$(PROFILE_ARG) daily-download-media --chat "$(CHAT)" --message-id "$(MESSAGE_ID)" $(if $(strip $(INDEX)),--index "$(INDEX)",) $(if $(strip $(OUT_DIR)),--out-dir "$(OUT_DIR)",) $(if $(strip $(OVERWRITE)),--overwrite,) $(if $(strip $(JSON)),--json,)

chats:
	$(CLI)$(PROFILE_ARG) chats $(if $(strip $(QUERY)),--query "$(QUERY)",)

topics:
	$(CLI)$(PROFILE_ARG) topics --chat "$(CHAT)"

dump:
	$(CLI)$(PROFILE_ARG) dump --chat "$(CHAT)" --out "$(or $(OUT),chat.jsonl)" $(if $(strip $(LIMIT)),--limit "$(LIMIT)",) $(if $(strip $(DOWNLOAD_MEDIA)),--download-media="$(DOWNLOAD_MEDIA)",) $(if $(strip $(MEDIA_DIR)),--media-dir "$(MEDIA_DIR)",) $(MEDIA_LIMIT_FLAGS)

sync:
	$(CLI)$(PROFILE_ARG) sync --chat "$(CHAT)" --name "$(NAME)" $(if $(strip $(ALL)),--all,) $(if $(strip $(RESET)),--reset,) $(if $(strip $(RESET_MERGED)),--reset-merged,) $(if $(strip $(BATCH_SIZE)),--batch-size "$(BATCH_SIZE)",) $(if $(strip $(MERGED_OUT)),--merged-out "$(MERGED_OUT)",) $(if $(strip $(DOWNLOAD_MEDIA)),--download-media="$(DOWNLOAD_MEDIA)",) $(if $(strip $(MEDIA_DIR)),--media-dir "$(MEDIA_DIR)",) $(MEDIA_LIMIT_FLAGS)

download-media:
	$(CLI)$(PROFILE_ARG) download-media --chat "$(CHAT)" --message-id "$(MESSAGE_ID)" $(if $(strip $(INDEX)),--index "$(INDEX)",) $(if $(strip $(OUT_DIR)),--out-dir "$(OUT_DIR)",) $(if $(strip $(OVERWRITE)),--overwrite,) $(if $(strip $(JSON)),--json,)

compact:
	$(CLI)$(PROFILE_ARG) compact --in "$(or $(IN),messages.jsonl)" --out "$(or $(OUT),messages.toon)" $(if $(strip $(SINCE)),--since "$(SINCE)",) $(if $(strip $(LIMIT)),--limit "$(LIMIT)",) $(if $(strip $(INCLUDE_SERVICE)),--include-service,)

agent-view:
	$(CLI)$(PROFILE_ARG) agent-view --in "$(or $(IN),messages.jsonl)" --out-dir "$(or $(OUT_DIR),agent-view)" $(if $(strip $(SINCE)),--since "$(SINCE)",) $(if $(strip $(RECENT)),--recent "$(RECENT)",) $(if $(strip $(INCLUDE_SERVICE)),--include-service,) $(if $(strip $(REBUILD)),--rebuild,)

refresh-agent-view: agent-view compact

clean:
	rm -rf .state artifacts telegram-harvest agent-view media media-refresh bin
	rm -f messages.jsonl messages.toon *.log
	rm -f .sessions/runtime.lock
