package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/chupakobra6/telegram-study-harvest/internal/config"
	"github.com/chupakobra6/telegram-study-harvest/internal/harvest"
	"github.com/chupakobra6/telegram-study-harvest/internal/mtproto"
	"github.com/chupakobra6/telegram-study-harvest/internal/runlock"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin, stdout, stderr *os.File) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	projectRoot := detectProjectRoot()
	if err := loadToolDotEnv(projectRoot); err != nil {
		return printError(stderr, 1, err)
	}
	cfg, err := config.Load()
	if err != nil {
		return printError(stderr, 1, err)
	}
	cfg = cfg.WithRoot(projectRoot)
	if args[0] != "login" {
		cfg = cfg.WithTelegramDesktopDefaults()
	}
	client := mtproto.New(cfg)

	switch args[0] {
	case "help", "--help", "-h":
		printUsage(stdout)
		return 0
	case "print-config":
		printConfig(cfg, stdout)
		return 0
	case "doctor":
		printDoctor(cfg, stdout, client)
		return 0
	case "login":
		if err := withRuntimeLock(cfg, func() error {
			return client.Login(context.Background(), stdin, stdout)
		}); err != nil {
			return printError(stderr, 1, err)
		}
		return 0
	case "import-tdesktop":
		if err := runImportTDesktop(cfg, args[1:], stdout); err != nil {
			return printError(stderr, 1, err)
		}
		return 0
	case "me":
		if err := runMe(cfg, client, args[1:], stdout); err != nil {
			return printError(stderr, 1, err)
		}
		return 0
	case "chats":
		if err := runChats(cfg, client, args[1:], stdout); err != nil {
			return printError(stderr, 1, err)
		}
		return 0
	case "topics":
		if err := runTopics(cfg, client, args[1:], stdout); err != nil {
			return printError(stderr, 1, err)
		}
		return 0
	case "dump":
		if err := runDump(cfg, client, args[1:], stdout); err != nil {
			return printError(stderr, 1, err)
		}
		return 0
	case "sync":
		if err := runSync(cfg, client, args[1:], stdout); err != nil {
			return printError(stderr, 1, err)
		}
		return 0
	case "compact":
		if err := runCompact(cfg, args[1:], stdout); err != nil {
			return printError(stderr, 1, err)
		}
		return 0
	case "agent-view":
		if err := runAgentView(cfg, args[1:], stdout); err != nil {
			return printError(stderr, 1, err)
		}
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func printUsage(out io.Writer) {
	fmt.Fprintln(out, "usage: telegram-study-harvest <help|doctor|print-config|login|import-tdesktop|me|chats|topics|dump|sync|compact|agent-view> [options]")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "commands are read-only except login/session file creation")
	fmt.Fprintln(out, "  import-tdesktop --tdata ~/Library/Application\\ Support/Telegram\\ Desktop/tdata")
	fmt.Fprintln(out, "  me [--json]")
	fmt.Fprintln(out, "  chats --query вшэ --limit 300 [--json]  # output is filtered by TG_STUDY_ALLOWED_CHATS when set")
	fmt.Fprintln(out, "  topics --chat <allowed-id-or-username> --limit 200 [--json]")
	fmt.Fprintln(out, "  dump --chat <allowed-id-or-username> --limit 500 --out hse-main.jsonl [--download-media --media-dir media]")
	fmt.Fprintln(out, "  sync --chat <allowed-id-or-username> --name hse-main [--all --reset] [--merged-out messages.jsonl] [--download-media --media-dir media]")
	fmt.Fprintln(out, "  compact --in messages.jsonl --out messages.toon [--since 2026-05-01] [--limit 500]")
	fmt.Fprintln(out, "  agent-view --in messages.jsonl --out-dir agent-view [--recent 300] [--rebuild]")
}

func printError(stderr io.Writer, code int, err error) int {
	fmt.Fprintln(stderr, err)
	return code
}

func printConfig(cfg config.Config, out io.Writer) {
	fmt.Fprintf(out, "app_id_set=%t\n", cfg.AppID != 0)
	fmt.Fprintf(out, "app_hash_set=%t\n", strings.TrimSpace(cfg.AppHash) != "")
	fmt.Fprintf(out, "phone_set=%t\n", strings.TrimSpace(cfg.Phone) != "")
	fmt.Fprintf(out, "session=%s\n", cfg.SessionPath)
	fmt.Fprintf(out, "runtime_lock=%s\n", cfg.RuntimeLockPath())
	fmt.Fprintf(out, "state_dir=%s\n", cfg.StateDir)
	fmt.Fprintf(out, "allowed_chats=%d\n", cfg.AllowedChatCount())
	fmt.Fprintf(out, "rpc_spacing=%s\n", cfg.RPCSpacing)
	fmt.Fprintf(out, "history_batch_size=%d\n", cfg.BatchSize)
	fmt.Fprintf(out, "history_limit=%d\n", cfg.HistoryLimit)
	fmt.Fprintf(out, "max_batches=%d\n", cfg.MaxBatches)
}

func printDoctor(cfg config.Config, out io.Writer, client *mtproto.Client) {
	fmt.Fprintf(out, "app_id_set=%t\n", cfg.AppID != 0)
	fmt.Fprintf(out, "app_hash_set=%t\n", strings.TrimSpace(cfg.AppHash) != "")
	fmt.Fprintf(out, "phone_set=%t\n", strings.TrimSpace(cfg.Phone) != "")
	fmt.Fprintf(out, "session_path=%s\n", cfg.SessionPath)
	fmt.Fprintf(out, "session_exists=%t\n", fileExists(cfg.SessionPath))
	fmt.Fprintf(out, "runtime_lock_path=%s\n", cfg.RuntimeLockPath())
	fmt.Fprintf(out, "state_dir=%s\n", cfg.StateDir)
	fmt.Fprintf(out, "state_dir_exists=%t\n", fileExists(cfg.StateDir))
	fmt.Fprintf(out, "allowed_chats=%d\n", cfg.AllowedChatCount())
	fmt.Fprintf(out, "read_only=true\n")
	authStatus, authDetail := doctorAuthStatus(cfg, client)
	fmt.Fprintf(out, "auth_status=%s\n", authStatus)
	if authDetail != "" {
		fmt.Fprintf(out, "auth_status_detail=%s\n", authDetail)
	}
}

func doctorAuthStatus(cfg config.Config, client *mtproto.Client) (string, string) {
	if cfg.AppID == 0 || strings.TrimSpace(cfg.AppHash) == "" {
		return "skipped", "set TG_STUDY_APP_ID and TG_STUDY_APP_HASH to verify live Telegram authorization"
	}
	if !fileExists(cfg.SessionPath) {
		return "reauth_required", "session file is missing; run `telegram-study-harvest login`"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	status, err := client.AuthStatus(ctx)
	if err != nil {
		return "check_failed", oneLine(err.Error())
	}
	if status.Authorized {
		return "authorized", "Telegram accepted the current session"
	}
	return "reauth_required", "Telegram requires re-login"
}

func runChats(cfg config.Config, client *mtproto.Client, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("chats", flag.ContinueOnError)
	fs.SetOutput(out)
	limit := fs.Int("limit", 300, "maximum dialogs to scan")
	query := fs.String("query", "", "case-insensitive title/username/id filter")
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return withRuntimeLock(cfg, func() error {
		return client.RunAuthorized(context.Background(), func(ctx context.Context, session *mtproto.Session) error {
			chats, err := session.ListDialogs(ctx, *limit, *query)
			if err != nil {
				return err
			}
			chats = filterAllowedChats(cfg, chats)
			if *jsonOut {
				encoder := json.NewEncoder(out)
				encoder.SetIndent("", "  ")
				return encoder.Encode(chats)
			}
			for _, chat := range chats {
				fmt.Fprintf(out, "%d\t%s\t%s", chat.ID, chat.Type, chat.Display)
				if chat.Username != "" {
					fmt.Fprintf(out, "\t@%s", chat.Username)
				}
				if !chat.LastMessageAt.IsZero() {
					fmt.Fprintf(out, "\tlast=%s", chat.LastMessageAt.Format(time.RFC3339))
				}
				if chat.UnreadCount > 0 {
					fmt.Fprintf(out, "\tunread=%d", chat.UnreadCount)
				}
				fmt.Fprintln(out)
			}
			return nil
		})
	})
}

func runTopics(cfg config.Config, client *mtproto.Client, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("topics", flag.ContinueOnError)
	fs.SetOutput(out)
	chat := fs.String("chat", "", "forum chat id or @username")
	limit := fs.Int("limit", 200, "maximum topics to list")
	query := fs.String("query", "", "optional topic title search")
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*chat) == "" {
		return fmt.Errorf("--chat is required")
	}
	if err := ensureAllowedChat(cfg, *chat); err != nil {
		return err
	}
	return withRuntimeLock(cfg, func() error {
		return client.RunAuthorized(context.Background(), func(ctx context.Context, session *mtproto.Session) error {
			topics, err := session.ListTopics(ctx, *chat, *limit, *query)
			if err != nil {
				return err
			}
			if *jsonOut {
				encoder := json.NewEncoder(out)
				encoder.SetIndent("", "  ")
				return encoder.Encode(topics)
			}
			for _, topic := range topics {
				fmt.Fprintf(out, "%d\t%s", topic.ID, topic.Title)
				if topic.TopMessageID > 0 {
					fmt.Fprintf(out, "\ttop_message=%d", topic.TopMessageID)
				}
				if !topic.LastMessageAt.IsZero() {
					fmt.Fprintf(out, "\tlast=%s", topic.LastMessageAt.Format(time.RFC3339))
				}
				if topic.UnreadCount > 0 {
					fmt.Fprintf(out, "\tunread=%d", topic.UnreadCount)
				}
				if topic.Pinned {
					fmt.Fprint(out, "\tpinned")
				}
				if topic.Closed {
					fmt.Fprint(out, "\tclosed")
				}
				if topic.Hidden {
					fmt.Fprint(out, "\thidden")
				}
				fmt.Fprintln(out)
			}
			return nil
		})
	})
}

func runImportTDesktop(cfg config.Config, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("import-tdesktop", flag.ContinueOnError)
	fs.SetOutput(out)
	tdata := fs.String("tdata", defaultTDataPath(), "Telegram Desktop tdata directory")
	accountIndex := fs.Int("account-index", 0, "Telegram Desktop account index")
	passcodeEnv := fs.String("passcode-env", "", "optional env var containing local Telegram Desktop passcode")
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	passcode := []byte(nil)
	if strings.TrimSpace(*passcodeEnv) != "" {
		passcode = []byte(os.Getenv(*passcodeEnv))
	}
	result, err := mtproto.ImportTDesktopSession(context.Background(), *tdata, *accountIndex, cfg.SessionPath, passcode)
	if err != nil {
		return err
	}
	if *jsonOut {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	fmt.Fprintf(out, "imported_tdesktop_session=true\n")
	fmt.Fprintf(out, "accounts_found=%d\n", result.AccountCount)
	fmt.Fprintf(out, "account_index=%d\n", result.AccountIndex)
	fmt.Fprintf(out, "session_path=%s\n", result.SessionPath)
	fmt.Fprintf(out, "dc=%d\n", result.DC)
	return nil
}

func runMe(cfg config.Config, client *mtproto.Client, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("me", flag.ContinueOnError)
	fs.SetOutput(out)
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return withRuntimeLock(cfg, func() error {
		return client.RunAuthorized(context.Background(), func(ctx context.Context, session *mtproto.Session) error {
			profile, err := session.SelfProfile(ctx)
			if err != nil {
				return err
			}
			if *jsonOut {
				encoder := json.NewEncoder(out)
				encoder.SetIndent("", "  ")
				return encoder.Encode(profile)
			}
			fmt.Fprintf(out, "id=%d\n", profile.ID)
			if profile.Username != "" {
				fmt.Fprintf(out, "username=@%s\n", profile.Username)
			}
			if profile.Display != "" {
				fmt.Fprintf(out, "display=%s\n", profile.Display)
			}
			if profile.Phone != "" {
				fmt.Fprintf(out, "phone=%s\n", maskCLIPhone(profile.Phone))
			}
			return nil
		})
	})
}

func maskCLIPhone(phone string) string {
	trimmed := strings.TrimSpace(phone)
	if trimmed == "" {
		return ""
	}
	if !strings.HasPrefix(trimmed, "+") {
		trimmed = "+" + trimmed
	}
	if len(trimmed) <= 5 {
		return trimmed
	}
	return trimmed[:2] + strings.Repeat("*", len(trimmed)-4) + trimmed[len(trimmed)-2:]
}

func runDump(cfg config.Config, client *mtproto.Client, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("dump", flag.ContinueOnError)
	fs.SetOutput(out)
	chat := fs.String("chat", "", "chat id or @username")
	output := fs.String("out", "", "JSONL output path")
	limit := fs.Int("limit", cfg.HistoryLimit, "maximum records")
	batchSize := fs.Int("batch-size", cfg.BatchSize, "messages.getHistory batch size, max 100")
	maxBatches := fs.Int("max-batches", cfg.MaxBatches, "maximum getHistory batches")
	sinceID := fs.Int("since-id", 0, "only export messages with id greater than this")
	all := fs.Bool("all", false, "export all available history")
	topicID := fs.Int("topic", 0, "forum topic id to export via replies")
	topicTitle := fs.String("topic-title", "", "optional topic title stored in output metadata")
	downloadMedia := fs.Bool("download-media", false, "download supported photo/document attachments while exporting")
	mediaDir := fs.String("media-dir", "media", "media output directory, relative to state dir unless absolute")
	maxMediaBytes := fs.Int64("max-media-bytes", 50*1024*1024, "maximum document bytes to download; 0 disables the size cap")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*chat) == "" {
		return fmt.Errorf("--chat is required")
	}
	if err := ensureAllowedChat(cfg, *chat); err != nil {
		return err
	}
	if strings.TrimSpace(*output) == "" {
		return fmt.Errorf("--out is required")
	}
	outputPath := resolveOutputPath(cfg.StateDir, *output)
	history := harvest.HistoryOptions{
		Limit:      *limit,
		BatchSize:  *batchSize,
		MaxBatches: *maxBatches,
		MinID:      *sinceID,
		All:        *all,
		TopicID:    *topicID,
		TopicTitle: *topicTitle,
	}
	if *downloadMedia {
		history.DownloadMedia = true
		history.MediaDir = resolveOutputPath(cfg.StateDir, *mediaDir)
		history.MaxMediaBytes = *maxMediaBytes
	}
	if *all {
		history.Limit = 0
		if !flagWasSet(fs, "max-batches") {
			history.MaxBatches = 0
		}
	}
	return withRuntimeLock(cfg, func() error {
		return client.RunAuthorized(context.Background(), func(ctx context.Context, session *mtproto.Session) error {
			encoder, file, err := harvest.OpenJSONL(outputPath, false)
			if err != nil {
				return err
			}
			defer file.Close()
			_, stats, err := session.DumpHistory(ctx, *chat, history, func(record harvest.MessageRecord) error {
				return encoder.Encode(record)
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "wrote=%d out=%s first_id=%d last_id=%d batches=%d flood_waits=%d\n",
				stats.Records,
				outputPath,
				stats.FirstID,
				stats.LastID,
				stats.Batches,
				stats.FloodWaits,
			)
			return nil
		})
	})
}

func runSync(cfg config.Config, client *mtproto.Client, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(out)
	chat := fs.String("chat", "", "chat id or @username")
	name := fs.String("name", "", "local stream name")
	limit := fs.Int("limit", cfg.HistoryLimit, "maximum new records")
	batchSize := fs.Int("batch-size", cfg.BatchSize, "messages.getHistory batch size, max 100")
	maxBatches := fs.Int("max-batches", cfg.MaxBatches, "maximum getHistory batches")
	mergedOut := fs.String("merged-out", "", "optional append-only merged JSONL output")
	all := fs.Bool("all", false, "sync all available history")
	reset := fs.Bool("reset", false, "truncate this stream and reset its state before syncing")
	resetMerged := fs.Bool("reset-merged", false, "truncate merged output before writing")
	topicID := fs.Int("topic", 0, "forum topic id to sync via replies")
	topicTitle := fs.String("topic-title", "", "optional topic title stored in state metadata")
	downloadMedia := fs.Bool("download-media", false, "download supported photo/document attachments while syncing")
	mediaDir := fs.String("media-dir", "media", "media output directory, relative to state dir unless absolute")
	maxMediaBytes := fs.Int64("max-media-bytes", 50*1024*1024, "maximum document bytes to download; 0 disables the size cap")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*chat) == "" {
		return fmt.Errorf("--chat is required")
	}
	if err := ensureAllowedChat(cfg, *chat); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" {
		return fmt.Errorf("--name is required")
	}
	streamPath := filepath.Join(cfg.StateDir, *name+".jsonl")
	statePath := filepath.Join(cfg.StateDir, *name+".state.json")
	mergedPath := ""
	if strings.TrimSpace(*mergedOut) != "" {
		mergedPath = resolveOutputPath(cfg.StateDir, *mergedOut)
	}
	history := harvest.HistoryOptions{
		Limit:      *limit,
		BatchSize:  *batchSize,
		MaxBatches: *maxBatches,
		All:        *all,
		TopicID:    *topicID,
		TopicTitle: *topicTitle,
	}
	if *downloadMedia {
		history.DownloadMedia = true
		history.MediaDir = resolveOutputPath(cfg.StateDir, *mediaDir)
		history.MaxMediaBytes = *maxMediaBytes
	}
	if *all {
		history.Limit = 0
		if !flagWasSet(fs, "max-batches") {
			history.MaxBatches = 0
		}
	}
	return withRuntimeLock(cfg, func() error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return client.RunAuthorized(ctx, func(ctx context.Context, session *mtproto.Session) error {
			result, err := harvest.RunSync(ctx, session, harvest.SyncOptions{
				Chat:        *chat,
				StreamPath:  streamPath,
				StatePath:   statePath,
				MergedPath:  mergedPath,
				History:     history,
				Reset:       *reset,
				ResetMerged: *resetMerged,
				Progress: func(progress harvest.SyncProgress) {
					printSyncProgress(out, progress)
				},
			})
			if err != nil {
				if errors.Is(err, context.Canceled) {
					printInterruptedSync(out, statePath)
					return nil
				}
				return err
			}
			mode := "incremental"
			if *all {
				mode = "all"
			}
			if *all && result.State.Backfill != nil {
				fmt.Fprintf(out, "mode=%s complete=%t synced=%d total_records=%d stream=%s state=%s last_id=%d next_offset_id=%d batches=%d flood_waits=%d\n",
					mode,
					result.State.Backfill.Complete,
					result.Stats.Records,
					result.State.Backfill.Records,
					result.StreamPath,
					result.StatePath,
					result.State.LastID,
					result.State.Backfill.NextOffsetID,
					result.State.Backfill.Batches,
					result.Stats.FloodWaits,
				)
				return nil
			}
			fmt.Fprintf(out, "mode=%s synced=%d stream=%s state=%s last_id=%d batches=%d flood_waits=%d\n",
				mode,
				result.Stats.Records,
				result.StreamPath,
				result.StatePath,
				result.State.LastID,
				result.Stats.Batches,
				result.Stats.FloodWaits,
			)
			return nil
		})
	})
}

func runCompact(cfg config.Config, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("compact", flag.ContinueOnError)
	fs.SetOutput(out)
	input := fs.String("in", "messages.jsonl", "JSONL input path")
	output := fs.String("out", "messages.toon", "compact TOON-style output path")
	sinceRaw := fs.String("since", "", "optional lower date bound, YYYY-MM-DD or RFC3339")
	limit := fs.Int("limit", 0, "maximum newest records to write after filtering; 0 means all")
	includeService := fs.Bool("include-service", false, "include Telegram service messages")
	if err := fs.Parse(args); err != nil {
		return err
	}
	since, err := parseCompactSince(*sinceRaw)
	if err != nil {
		return err
	}
	inputPath := resolveOutputPath(cfg.StateDir, *input)
	outputPath := resolveOutputPath(cfg.StateDir, *output)
	stats, err := harvest.WriteCompactTOON(harvest.CompactOptions{
		InputPath:      inputPath,
		OutputPath:     outputPath,
		Since:          since,
		Limit:          *limit,
		IncludeService: *includeService,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "read=%d wrote=%d skipped=%d in=%s out=%s\n",
		stats.Records,
		stats.Written,
		stats.Skipped,
		inputPath,
		outputPath,
	)
	return nil
}

func runAgentView(cfg config.Config, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("agent-view", flag.ContinueOnError)
	fs.SetOutput(out)
	input := fs.String("in", "messages.jsonl", "JSONL input path")
	outputDir := fs.String("out-dir", "agent-view", "Markdown agent-view output directory")
	sinceRaw := fs.String("since", "", "optional lower date bound, YYYY-MM-DD or RFC3339")
	recent := fs.Int("recent", 300, "number of newest messages to include in all-recent.md")
	includeService := fs.Bool("include-service", false, "include Telegram service messages")
	rebuild := fs.Bool("rebuild", false, "force full agent-view rebuild instead of incremental update")
	if err := fs.Parse(args); err != nil {
		return err
	}
	since, err := parseCompactSince(*sinceRaw)
	if err != nil {
		return err
	}
	inputPath := resolveOutputPath(cfg.StateDir, *input)
	outputPath := resolveOutputPath(cfg.StateDir, *outputDir)
	opts := harvest.AgentViewOptions{
		InputPath:      inputPath,
		OutputDir:      outputPath,
		Since:          since,
		RecentLimit:    *recent,
		IncludeService: *includeService,
	}
	var stats harvest.AgentViewStats
	if *rebuild {
		stats, err = harvest.WriteAgentMarkdownView(opts)
	} else {
		stats, err = harvest.UpdateAgentMarkdownView(opts)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "mode=%s read=%d wrote=%d skipped=%d raw_added=%d visible_added=%d chats=%d topics=%d files=%d in=%s out=%s\n",
		stats.Mode,
		stats.Records,
		stats.Written,
		stats.Skipped,
		stats.RawAdded,
		stats.VisibleAdded,
		stats.Chats,
		stats.Topics,
		stats.Files,
		inputPath,
		outputPath,
	)
	return nil
}

func printSyncProgress(out io.Writer, progress harvest.SyncProgress) {
	backfill := progress.State.Backfill
	if backfill == nil {
		return
	}
	fmt.Fprintf(out, "progress complete=%t records=%d batch_records=%d batches=%d oldest_id=%d latest_id=%d next_offset_id=%d flood_waits=%d\n",
		backfill.Complete,
		backfill.Records,
		progress.History.BatchRecords,
		backfill.Batches,
		backfill.OldestID,
		backfill.LatestID,
		backfill.NextOffsetID,
		progress.History.FloodWaits,
	)
}

func printInterruptedSync(out io.Writer, statePath string) {
	state, err := harvest.LoadSyncState(statePath)
	if err != nil || state.Backfill == nil {
		fmt.Fprintln(out, "interrupted=true")
		return
	}
	fmt.Fprintf(out, "interrupted=true complete=%t records=%d batches=%d next_offset_id=%d resume=\"sync --all --chat <same> --name <same>\"\n",
		state.Backfill.Complete,
		state.Backfill.Records,
		state.Backfill.Batches,
		state.Backfill.NextOffsetID,
	)
}

func filterAllowedChats(cfg config.Config, chats []harvest.Chat) []harvest.Chat {
	if cfg.AllowedChatCount() == 0 {
		return chats
	}
	filtered := make([]harvest.Chat, 0, len(chats))
	for _, chat := range chats {
		if cfg.ChatAllowed(fmt.Sprintf("%d", chat.ID)) || (chat.Username != "" && cfg.ChatAllowed(chat.Username)) {
			filtered = append(filtered, chat)
		}
	}
	return filtered
}

func ensureAllowedChat(cfg config.Config, chat string) error {
	if cfg.ChatAllowed(chat) {
		return nil
	}
	return fmt.Errorf("chat %q is outside TG_STUDY_ALLOWED_CHATS; refusing to read outside study scope", chat)
}

func withRuntimeLock(cfg config.Config, fn func() error) error {
	lock, err := runlock.Acquire(cfg.RuntimeLockPath())
	if err != nil {
		return err
	}
	defer lock.Release()
	return fn()
}

func resolveOutputPath(stateDir string, output string) string {
	if filepath.IsAbs(output) {
		return output
	}
	return filepath.Join(stateDir, output)
}

func flagWasSet(fs *flag.FlagSet, name string) bool {
	wasSet := false
	fs.Visit(func(flag *flag.Flag) {
		if flag.Name == name {
			wasSet = true
		}
	})
	return wasSet
}

func parseCompactSince(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, nil
	}
	moscow := time.FixedZone("Europe/Moscow", 3*60*60)
	if parsed, err := time.ParseInLocation("2006-01-02", value, moscow); err == nil {
		return parsed, nil
	}
	return time.Time{}, fmt.Errorf("--since must be YYYY-MM-DD or RFC3339")
}

func defaultTDataPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Application Support", "Telegram Desktop", "tdata")
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func detectProjectRoot() string {
	if _, file, _, ok := runtime.Caller(0); ok {
		root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
		if fileExists(filepath.Join(root, "go.mod")) {
			return root
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

func loadToolDotEnv(projectRoot string) error {
	cwd, err := os.Getwd()
	if err == nil {
		if err := config.LoadDotEnv(filepath.Join(cwd, ".env")); err != nil {
			return err
		}
	}
	if projectRoot == "" {
		return nil
	}
	rootEnv := filepath.Join(projectRoot, ".env")
	if err == nil && samePath(cwd, projectRoot) {
		return nil
	}
	return config.LoadDotEnv(rootEnv)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func samePath(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	leftAbs, err := filepath.Abs(left)
	if err != nil {
		return false
	}
	rightAbs, err := filepath.Abs(right)
	if err != nil {
		return false
	}
	return leftAbs == rightAbs
}
