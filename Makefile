CLI := go run ./cmd/telegram-study-harvest

.DEFAULT_GOAL := help

.PHONY: help setup fmt test doctor login daily-doctor daily-login daily-me daily chats topics sync compact agent-view refresh-agent-view clean

help:
	@printf "Available commands:\\n"
	@printf "  make setup   # go mod tidy\\n"
	@printf "  make fmt     # gofmt project files\\n"
	@printf "  make test    # go test ./...\\n"
	@printf "  make doctor  # show config/session status\\n"
	@printf "  make login   # create MTProto user session\\n"
	@printf "  make daily-doctor # show daily mode config/session status\\n"
	@printf "  make daily-login  # create daily mode MTProto user session\\n"
	@printf "  make daily   # export outgoing/self messages for DATE=today|yesterday|YYYY-MM-DD\\n"
	@printf "  make chats   # list allowed study dialogs; pass QUERY='вшэ' to filter\\n"
	@printf "  make topics  # list topics for CHAT=<allowed forum id>\\n"
	@printf "  make sync    # incremental sync for CHAT=... NAME=...\\n"
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

doctor:
	$(CLI) doctor

login:
	$(CLI) login

daily-doctor:
	$(CLI) daily-doctor

daily-login:
	$(CLI) daily-login

daily-me:
	$(CLI) daily-me

daily:
	$(CLI) daily --date "$(or $(DATE),today)" $(if $(strip $(OUT)),--out "$(OUT)",) $(if $(strip $(MARKDOWN_OUT)),--markdown-out "$(MARKDOWN_OUT)",) $(if $(strip $(DIALOG_LIMIT)),--dialog-limit "$(DIALOG_LIMIT)",) $(if $(strip $(DOWNLOAD_MEDIA)),--download-media="$(DOWNLOAD_MEDIA)",) $(if $(strip $(TRANSCRIBE)),--transcribe="$(TRANSCRIBE)",) $(if $(strip $(PROGRESS)),--progress,)

chats:
	$(CLI) chats $(if $(strip $(QUERY)),--query "$(QUERY)",)

topics:
	$(CLI) topics --chat "$(CHAT)"

sync:
	$(CLI) sync --chat "$(CHAT)" --name "$(NAME)" $(if $(strip $(MERGED_OUT)),--merged-out "$(MERGED_OUT)",)

compact:
	$(CLI) compact --in "$(or $(IN),messages.jsonl)" --out "$(or $(OUT),messages.toon)" $(if $(strip $(SINCE)),--since "$(SINCE)",) $(if $(strip $(LIMIT)),--limit "$(LIMIT)",) $(if $(strip $(INCLUDE_SERVICE)),--include-service,)

agent-view:
	$(CLI) agent-view --in "$(or $(IN),messages.jsonl)" --out-dir "$(or $(OUT_DIR),agent-view)" $(if $(strip $(SINCE)),--since "$(SINCE)",) $(if $(strip $(RECENT)),--recent "$(RECENT)",) $(if $(strip $(INCLUDE_SERVICE)),--include-service,) $(if $(strip $(REBUILD)),--rebuild,)

refresh-agent-view: agent-view compact

clean:
	rm -rf .state artifacts telegram-study-harvest agent-view media media-refresh
	rm -f messages.jsonl messages.toon *.log
	rm -f .sessions/runtime.lock
