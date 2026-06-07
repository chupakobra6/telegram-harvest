CLI := go run ./cmd/telegram-harvest
PROFILE ?=
PROFILE_ARG = --profile "$(PROFILE)"
REQUIRE_PROFILE = @test -n "$(strip $(PROFILE))" || { printf "PROFILE=main|study is required\n"; exit 2; }
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
	@printf "  make doctor PROFILE=main|study # show config/session status\\n"
	@printf "  make login PROFILE=main|study  # create MTProto user session\\n"
	@printf "  make daily PROFILE=main DATE=today|yesterday|YYYY-MM-DD # export outgoing/self messages\\n"
	@printf "  make daily-download-media PROFILE=main # manual uncapped daily media fetch; CHAT=... MESSAGE_ID=...\\n"
	@printf "  make chats PROFILE=study # list dialogs; pass QUERY='вшэ' to filter\\n"
	@printf "  make topics PROFILE=study # list topics for CHAT=<allowed forum id>\\n"
	@printf "  make dump PROFILE=study # dump allowed study chat; CHAT=... OUT=...\\n"
	@printf "  make sync PROFILE=study # incremental sync for CHAT=... NAME=...\\n"
	@printf "  make download-media PROFILE=study # manual uncapped media fetch; CHAT=... MESSAGE_ID=...\\n"
	@printf "  make compact PROFILE=study # generate messages.toon from messages.jsonl\\n"
	@printf "  make agent-view PROFILE=study # update Markdown navigation for agents\\n"
	@printf "  make refresh-agent-view PROFILE=study # update Markdown navigation and messages.toon\\n"
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
	$(REQUIRE_PROFILE)
	$(CLI) $(PROFILE_ARG) doctor

login:
	$(REQUIRE_PROFILE)
	$(CLI) $(PROFILE_ARG) login

daily:
	$(REQUIRE_PROFILE)
	$(CLI) $(PROFILE_ARG) daily --date "$(or $(DATE),today)" $(if $(strip $(OUT)),--out "$(OUT)",) $(if $(strip $(MARKDOWN_OUT)),--markdown-out "$(MARKDOWN_OUT)",) $(if $(strip $(DIALOG_LIMIT)),--dialog-limit "$(DIALOG_LIMIT)",) $(if $(strip $(DOWNLOAD_MEDIA)),--download-media="$(DOWNLOAD_MEDIA)",) $(if $(strip $(MEDIA_DIR)),--media-dir "$(MEDIA_DIR)",) $(MEDIA_LIMIT_FLAGS) $(if $(strip $(TRANSCRIBE)),--transcribe="$(TRANSCRIBE)",) $(if $(strip $(RETAIN_DAYS)),--retain-days "$(RETAIN_DAYS)",) $(if $(strip $(PROGRESS)),--progress,)

daily-download-media:
	$(REQUIRE_PROFILE)
	$(CLI) $(PROFILE_ARG) daily-download-media --chat "$(CHAT)" --message-id "$(MESSAGE_ID)" $(if $(strip $(INDEX)),--index "$(INDEX)",) $(if $(strip $(OUT_DIR)),--out-dir "$(OUT_DIR)",) $(if $(strip $(OVERWRITE)),--overwrite,) $(if $(strip $(JSON)),--json,)

chats:
	$(REQUIRE_PROFILE)
	$(CLI) $(PROFILE_ARG) chats $(if $(strip $(QUERY)),--query "$(QUERY)",)

topics:
	$(REQUIRE_PROFILE)
	$(CLI) $(PROFILE_ARG) topics --chat "$(CHAT)"

dump:
	$(REQUIRE_PROFILE)
	$(CLI) $(PROFILE_ARG) dump --chat "$(CHAT)" --out "$(or $(OUT),chat.jsonl)" $(if $(strip $(LIMIT)),--limit "$(LIMIT)",) $(if $(strip $(DOWNLOAD_MEDIA)),--download-media="$(DOWNLOAD_MEDIA)",) $(if $(strip $(MEDIA_DIR)),--media-dir "$(MEDIA_DIR)",) $(MEDIA_LIMIT_FLAGS)

sync:
	$(REQUIRE_PROFILE)
	$(CLI) $(PROFILE_ARG) sync --chat "$(CHAT)" --name "$(NAME)" $(if $(strip $(ALL)),--all,) $(if $(strip $(RESET)),--reset,) $(if $(strip $(RESET_MERGED)),--reset-merged,) $(if $(strip $(BATCH_SIZE)),--batch-size "$(BATCH_SIZE)",) $(if $(strip $(MERGED_OUT)),--merged-out "$(MERGED_OUT)",) $(if $(strip $(DOWNLOAD_MEDIA)),--download-media="$(DOWNLOAD_MEDIA)",) $(if $(strip $(MEDIA_DIR)),--media-dir "$(MEDIA_DIR)",) $(MEDIA_LIMIT_FLAGS)

download-media:
	$(REQUIRE_PROFILE)
	$(CLI) $(PROFILE_ARG) download-media --chat "$(CHAT)" --message-id "$(MESSAGE_ID)" $(if $(strip $(INDEX)),--index "$(INDEX)",) $(if $(strip $(OUT_DIR)),--out-dir "$(OUT_DIR)",) $(if $(strip $(OVERWRITE)),--overwrite,) $(if $(strip $(JSON)),--json,)

compact:
	$(REQUIRE_PROFILE)
	$(CLI) $(PROFILE_ARG) compact --in "$(or $(IN),messages.jsonl)" --out "$(or $(OUT),messages.toon)" $(if $(strip $(SINCE)),--since "$(SINCE)",) $(if $(strip $(LIMIT)),--limit "$(LIMIT)",) $(if $(strip $(INCLUDE_SERVICE)),--include-service,)

agent-view:
	$(REQUIRE_PROFILE)
	$(CLI) $(PROFILE_ARG) agent-view --in "$(or $(IN),messages.jsonl)" --out-dir "$(or $(OUT_DIR),agent-view)" $(if $(strip $(SINCE)),--since "$(SINCE)",) $(if $(strip $(RECENT)),--recent "$(RECENT)",) $(if $(strip $(INCLUDE_SERVICE)),--include-service,) $(if $(strip $(REBUILD)),--rebuild,)

refresh-agent-view: agent-view compact

clean:
	rm -rf .state artifacts telegram-harvest agent-view media media-refresh bin
	rm -f messages.jsonl messages.toon *.log
	rm -f .sessions/runtime.lock
