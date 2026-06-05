package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/chupakobra6/telegram-study-harvest/internal/config"
	"github.com/chupakobra6/telegram-study-harvest/internal/harvest"
	"github.com/chupakobra6/telegram-study-harvest/internal/mtproto"
	"github.com/chupakobra6/telegram-study-harvest/internal/runlock"
	"github.com/chupakobra6/telegram-study-harvest/internal/transcribe"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin, stdout, stderr *os.File) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printUsage(stdout)
		return 0
	}
	projectRoot := detectProjectRoot()
	if err := loadToolDotEnv(projectRoot); err != nil {
		return printError(stderr, 1, err)
	}
	cfg, err := loadCommandConfig(args[0])
	if err != nil {
		return printError(stderr, 1, err)
	}
	cfg = cfg.WithRoot(projectRoot)
	if shouldUseTelegramDesktopDefaults(args[0]) {
		cfg = cfg.WithTelegramDesktopDefaults()
	}
	client := mtproto.New(cfg)

	switch args[0] {
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
	case "daily-config":
		printConfig(cfg, stdout)
		return 0
	case "daily-doctor":
		printDoctor(cfg, stdout, client)
		return 0
	case "daily-login":
		if err := withRuntimeLock(cfg, func() error {
			return client.Login(context.Background(), stdin, stdout)
		}); err != nil {
			return printError(stderr, 1, err)
		}
		return 0
	case "daily-me":
		if err := runMe(cfg, client, args[1:], stdout); err != nil {
			return printError(stderr, 1, err)
		}
		return 0
	case "daily":
		if err := runDaily(cfg, client, args[1:], stdout); err != nil {
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

func loadCommandConfig(command string) (config.Config, error) {
	if isDailyCommand(command) {
		return config.LoadDaily()
	}
	return config.LoadStudy()
}

func isDailyCommand(command string) bool {
	return command == "daily" || strings.HasPrefix(command, "daily-")
}

func shouldUseTelegramDesktopDefaults(command string) bool {
	return command != "login" && !isDailyCommand(command)
}

func printUsage(out io.Writer) {
	fmt.Fprintln(out, "usage: telegram-study-harvest <help|doctor|print-config|login|import-tdesktop|me|chats|topics|dump|sync|compact|agent-view|daily> [options]")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "commands are read-only except login/session file creation")
	fmt.Fprintln(out, "  import-tdesktop --tdata ~/Library/Application\\ Support/Telegram\\ Desktop/tdata")
	fmt.Fprintln(out, "  me [--json]")
	fmt.Fprintln(out, "  chats --query вшэ --limit 300 [--json]  # output is filtered by the study allowlist when set")
	fmt.Fprintln(out, "  topics --chat <allowed-id-or-username> --limit 200 [--json]")
	fmt.Fprintln(out, "  dump --chat <allowed-id-or-username> --limit 500 --out hse-main.jsonl [--download-media --media-dir media]")
	fmt.Fprintln(out, "  sync --chat <allowed-id-or-username> --name hse-main [--all --reset] [--merged-out messages.jsonl] [--download-media --media-dir media]")
	fmt.Fprintln(out, "  compact --in messages.jsonl --out messages.toon [--since 2026-05-01] [--limit 500]")
	fmt.Fprintln(out, "  agent-view --in messages.jsonl --out-dir agent-view [--recent 300] [--rebuild]")
	fmt.Fprintln(out, "  daily --date today [--out days/YYYY-MM-DD.jsonl] [--markdown-out days/YYYY-MM-DD.md] [--download-media=false]")
	fmt.Fprintln(out, "  daily-login | daily-doctor | daily-me  # use TG_HARVEST_* account settings")
}

func printError(stderr io.Writer, code int, err error) int {
	fmt.Fprintln(stderr, err)
	return code
}

func printConfig(cfg config.Config, out io.Writer) {
	fmt.Fprintf(out, "mode=%s\n", defaultModeName(cfg.Mode))
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
	if cfg.Mode == config.ModeDaily {
		printDailyRuntimeConfig(out, false)
	}
}

func printDoctor(cfg config.Config, out io.Writer, client *mtproto.Client) {
	fmt.Fprintf(out, "mode=%s\n", defaultModeName(cfg.Mode))
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
	if cfg.Mode == config.ModeDaily {
		printDailyRuntimeConfig(out, true)
	}
	authStatus, authDetail := doctorAuthStatus(cfg, client)
	fmt.Fprintf(out, "auth_status=%s\n", authStatus)
	if authDetail != "" {
		fmt.Fprintf(out, "auth_status_detail=%s\n", authDetail)
	}
}

func printDailyRuntimeConfig(out io.Writer, includeChecks bool) {
	defaults := dailyRuntimeDefaults()
	fmt.Fprintf(out, "daily_transcribe_default=%t\n", defaults.TranscribeMedia)
	fmt.Fprintf(out, "daily_transcribe_command_set=%t\n", strings.TrimSpace(defaults.TranscribeCommand) != "")
	fmt.Fprintf(out, "daily_vosk_command=%s\n", defaults.VoskCommand)
	fmt.Fprintf(out, "daily_vosk_model_path=%s\n", defaults.VoskModelPath)
	fmt.Fprintf(out, "daily_vosk_grammar_path=%s\n", defaults.VoskGrammarPath)
	fmt.Fprintf(out, "daily_ffmpeg_command=%s\n", defaults.FFmpegCommand)
	fmt.Fprintf(out, "daily_retention_days=%d\n", defaults.RetainDays)
	if !includeChecks {
		return
	}
	if resolved, ok := resolveCommand(defaults.FFmpegCommand); ok {
		fmt.Fprintf(out, "daily_ffmpeg_status=ok:%s\n", resolved)
	} else {
		fmt.Fprintf(out, "daily_ffmpeg_status=missing\n")
	}
	if strings.TrimSpace(defaults.VoskCommand) == "" {
		fmt.Fprintf(out, "daily_vosk_command_status=missing\n")
	} else if resolved, ok := resolveCommand(defaults.VoskCommand); ok {
		fmt.Fprintf(out, "daily_vosk_command_status=ok:%s\n", resolved)
	} else {
		fmt.Fprintf(out, "daily_vosk_command_status=missing\n")
	}
	if strings.TrimSpace(defaults.VoskModelPath) == "" {
		fmt.Fprintf(out, "daily_vosk_model_status=missing\n")
	} else if fileExists(defaults.VoskModelPath) {
		fmt.Fprintf(out, "daily_vosk_model_status=ok\n")
	} else {
		fmt.Fprintf(out, "daily_vosk_model_status=missing\n")
	}
	if strings.TrimSpace(defaults.VoskGrammarPath) == "" {
		fmt.Fprintf(out, "daily_vosk_grammar_status=not_set\n")
	} else if fileExists(defaults.VoskGrammarPath) {
		fmt.Fprintf(out, "daily_vosk_grammar_status=ok\n")
	} else {
		fmt.Fprintf(out, "daily_vosk_grammar_status=missing\n")
	}
}

func doctorAuthStatus(cfg config.Config, client *mtproto.Client) (string, string) {
	if cfg.AppID == 0 || strings.TrimSpace(cfg.AppHash) == "" {
		return "skipped", fmt.Sprintf("set %s and %s to verify live Telegram authorization", cfg.EnvNames("APP_ID"), cfg.EnvNames("APP_HASH"))
	}
	if !fileExists(cfg.SessionPath) {
		return "reauth_required", fmt.Sprintf("session file is missing; run `%s`", cfg.LoginCommand())
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
	return "reauth_required", fmt.Sprintf("Telegram requires re-login; run `%s`", cfg.LoginCommand())
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

func runDaily(cfg config.Config, client *mtproto.Client, args []string, out io.Writer) error {
	dateDefault := "today"
	dateLabelDefault, _, _, _ := parseDailyDate(dateDefault, time.Now())
	defaultJSONL, defaultMarkdown := harvest.DailyDefaultOutputPaths(cfg.StateDir, dateLabelDefault)
	defaults := dailyRuntimeDefaults()

	fs := flag.NewFlagSet("daily", flag.ContinueOnError)
	fs.SetOutput(out)
	dateRaw := fs.String("date", dateDefault, "day to harvest: today, yesterday, or YYYY-MM-DD in Europe/Moscow")
	output := fs.String("out", defaultJSONL, "JSONL output path; relative paths are resolved under state dir")
	markdownOut := fs.String("markdown-out", defaultMarkdown, "Markdown output path; relative paths are resolved under state dir")
	dialogLimit := fs.Int("dialog-limit", dailyDialogLimitDefault(), "maximum dialogs to scan")
	limit := fs.Int("limit", 0, "maximum newest records to write after filtering; 0 means all")
	batchSize := fs.Int("batch-size", cfg.BatchSize, "Telegram history/search batch size, max 100")
	maxBatches := fs.Int("max-batches", cfg.MaxBatches, "maximum batches per dialog; 0 disables the per-dialog cap")
	includeService := fs.Bool("include-service", false, "include Telegram service messages")
	downloadMedia := fs.Bool("download-media", true, "download photos and image documents; audio/video is downloaded temporarily for transcription")
	mediaDir := fs.String("media-dir", "media", "media output directory, relative to state dir unless absolute")
	maxMediaBytes := fs.Int64("max-media-bytes", 50*1024*1024, "maximum document bytes to download; 0 disables the size cap")
	transcribeMedia := fs.Bool("transcribe", defaults.TranscribeMedia, "transcribe voice/audio/video media; cached transcripts skip media download")
	transcribeCommand := fs.String("transcribe-cmd", defaults.TranscribeCommand, "custom shell command template override; supports {input}, {output}, {output_dir}, {output_base}")
	voskCommand := fs.String("vosk-command", defaults.VoskCommand, "Vosk helper command, called as: command <model> <wav> [grammar]")
	voskModelPath := fs.String("vosk-model", defaults.VoskModelPath, "Vosk model directory")
	voskGrammarPath := fs.String("vosk-grammar", defaults.VoskGrammarPath, "optional Vosk grammar JSON path")
	ffmpegCommand := fs.String("ffmpeg-command", defaults.FFmpegCommand, "ffmpeg command for audio extraction and WAV conversion")
	transcriptDir := fs.String("transcript-dir", "transcripts", "transcript output directory, relative to state dir unless absolute")
	retainDays := fs.Int("retain-days", defaults.RetainDays, "daily state retention window in days; <=0 disables pruning")
	progressOut := fs.Bool("progress", false, "print per-dialog progress")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dateLabel, start, end, err := parseDailyDate(*dateRaw, time.Now())
	if err != nil {
		return err
	}
	if !flagWasSet(fs, "out") {
		jsonl, _ := harvest.DailyDefaultOutputPaths(cfg.StateDir, dateLabel)
		*output = jsonl
	}
	if !flagWasSet(fs, "markdown-out") {
		_, markdown := harvest.DailyDefaultOutputPaths(cfg.StateDir, dateLabel)
		*markdownOut = markdown
	}
	outputPath := resolveOutputPath(cfg.StateDir, *output)
	markdownPath := ""
	if strings.TrimSpace(*markdownOut) != "" {
		markdownPath = resolveOutputPath(cfg.StateDir, *markdownOut)
	}
	history := harvest.HistoryOptions{
		Limit:             *limit,
		BatchSize:         *batchSize,
		MaxBatches:        *maxBatches,
		DownloadMedia:     *downloadMedia,
		MaxMediaBytes:     *maxMediaBytes,
		TranscribeMedia:   *transcribeMedia,
		TranscribeCommand: *transcribeCommand,
		VoskCommand:       *voskCommand,
		VoskModelPath:     *voskModelPath,
		VoskGrammarPath:   *voskGrammarPath,
		FFmpegCommand:     *ffmpegCommand,
	}
	if *downloadMedia {
		history.MediaDir = resolveOutputPath(cfg.StateDir, *mediaDir)
	}
	if *transcribeMedia {
		history.TranscriptDir = resolveOutputPath(cfg.StateDir, *transcriptDir)
	}
	records := make([]harvest.MessageRecord, 0)
	var stats harvest.OutgoingDayStats
	if err := withRuntimeLock(cfg, func() error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return client.RunAuthorized(ctx, func(ctx context.Context, session *mtproto.Session) error {
			encoder, file, err := harvest.OpenJSONL(outputPath, false)
			if err != nil {
				return err
			}
			defer file.Close()
			progress := func(progress harvest.OutgoingDayProgress) error {
				if !*progressOut {
					return nil
				}
				if progress.Skipped {
					fmt.Fprintf(out, "progress skipped=true chat=%s total=%d flood_waits=%d\n", progress.Chat.Display, progress.Total, progress.FloodWaits)
					return nil
				}
				if progress.Error != "" {
					fmt.Fprintf(out, "progress error=true chat=%s detail=%s total=%d batches=%d flood_waits=%d\n", progress.Chat.Display, progress.Error, progress.Total, progress.Batches, progress.FloodWaits)
					return nil
				}
				fmt.Fprintf(out, "progress chat=%s records=%d total=%d batches=%d flood_waits=%d\n", progress.Chat.Display, progress.Records, progress.Total, progress.Batches, progress.FloodWaits)
				return nil
			}
			stats, err = session.DumpOutgoingDay(ctx, harvest.OutgoingDayOptions{
				Start:          start,
				End:            end,
				DialogLimit:    *dialogLimit,
				IncludeService: *includeService,
				History:        history,
				Progress:       progress,
			}, func(record harvest.MessageRecord) error {
				records = append(records, record)
				return encoder.Encode(record)
			})
			return err
		})
	}); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintf(out, "interrupted=true out=%s records=%d\n", outputPath, len(records))
			return nil
		}
		return err
	}
	if markdownPath != "" {
		if err := harvest.WriteDailyMarkdown(harvest.DailyMarkdownOptions{
			OutputPath: markdownPath,
			SourcePath: outputPath,
			Date:       dateLabel,
			Start:      start,
			End:        end,
			Stats:      stats,
			Records:    records,
		}); err != nil {
			return err
		}
	}
	retentionStats, err := harvest.PruneDailyState(harvest.DailyRetentionOptions{
		StateDir:   cfg.StateDir,
		RetainDays: *retainDays,
		Now:        time.Now(),
	})
	if err != nil {
		return err
	}
	for _, dialogErr := range stats.DialogErrors {
		fmt.Fprintf(out, "warning dialog_error=%s\n", dialogErr)
	}
	fmt.Fprintf(out, "date=%s wrote=%d dialogs=%d dialogs_with_records=%d attachments=%d transcripts=%d out=%s",
		dateLabel,
		stats.Records,
		stats.DialogsScanned,
		stats.DialogsWithRecords,
		stats.Attachments,
		stats.Transcripts,
		outputPath,
	)
	if markdownPath != "" {
		fmt.Fprintf(out, " markdown=%s", markdownPath)
	}
	fmt.Fprintf(out, " flood_waits=%d complete=%t pruned_files=%d pruned_dirs=%d\n",
		stats.FloodWaits,
		stats.Complete,
		retentionStats.DeletedFiles,
		retentionStats.DeletedDirs,
	)
	return nil
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
	return fmt.Errorf("chat %q is outside %s; refusing to read outside study scope", chat, cfg.EnvNames("ALLOWED_CHATS"))
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

func parseDailyDate(value string, now time.Time) (string, time.Time, time.Time, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		value = "today"
	}
	moscow := time.FixedZone("Europe/Moscow", 3*60*60)
	now = now.In(moscow)
	var day time.Time
	switch value {
	case "today":
		day = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, moscow)
	case "yesterday":
		base := now.AddDate(0, 0, -1)
		day = time.Date(base.Year(), base.Month(), base.Day(), 0, 0, 0, 0, moscow)
	default:
		parsed, err := time.ParseInLocation("2006-01-02", value, moscow)
		if err != nil {
			return "", time.Time{}, time.Time{}, fmt.Errorf("--date must be today, yesterday, or YYYY-MM-DD")
		}
		day = parsed
	}
	dateLabel := day.Format("2006-01-02")
	return dateLabel, day, day.AddDate(0, 0, 1), nil
}

func dailyDialogLimitDefault() int {
	if value, ok := intEnvValue("TG_HARVEST_DIALOG_LIMIT"); ok && value > 0 {
		return value
	}
	return 500
}

type dailyRuntimeConfig struct {
	TranscribeMedia   bool
	TranscribeCommand string
	VoskCommand       string
	VoskModelPath     string
	VoskGrammarPath   string
	FFmpegCommand     string
	RetainDays        int
}

func dailyRuntimeDefaults() dailyRuntimeConfig {
	transcribeCommand := firstEnvValue("TG_HARVEST_TRANSCRIBE_CMD")
	voskCommand := firstEnvValue("TG_HARVEST_VOSK_COMMAND")
	if voskCommand == "" {
		if resolved, err := exec.LookPath("vosk-transcribe"); err == nil {
			voskCommand = resolved
		}
	}
	voskModelPath := firstEnvValue("TG_HARVEST_VOSK_MODEL_PATH")
	if voskModelPath == "" {
		if candidate := defaultShelfyVoskModelPath(); candidate != "" {
			voskModelPath = candidate
		}
	}
	ffmpegCommand := firstEnvValue("TG_HARVEST_FFMPEG_COMMAND")
	if ffmpegCommand == "" {
		ffmpegCommand = transcribe.DefaultFFmpegCommand
	}
	retainDays := harvest.DefaultDailyRetentionDays
	if value, ok := intEnvValue("TG_HARVEST_RETENTION_DAYS"); ok {
		retainDays = value
	}
	return dailyRuntimeConfig{
		TranscribeMedia:   strings.TrimSpace(transcribeCommand) != "" || (strings.TrimSpace(voskCommand) != "" && strings.TrimSpace(voskModelPath) != ""),
		TranscribeCommand: transcribeCommand,
		VoskCommand:       voskCommand,
		VoskModelPath:     voskModelPath,
		VoskGrammarPath:   firstEnvValue("TG_HARVEST_VOSK_GRAMMAR_PATH"),
		FFmpegCommand:     ffmpegCommand,
		RetainDays:        retainDays,
	}
}

func defaultShelfyVoskModelPath() string {
	projectRoot := detectProjectRoot()
	if projectRoot == "" {
		return ""
	}
	candidate := filepath.Join(filepath.Dir(projectRoot), "shelfy", "models", "vosk-model-small-ru-0.22")
	if fileExists(candidate) {
		return candidate
	}
	return ""
}

func intEnvValue(keys ...string) (int, bool) {
	for _, key := range keys {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			continue
		}
		value, err := strconv.Atoi(raw)
		if err != nil {
			return 0, false
		}
		return value, true
	}
	return 0, false
}

func firstEnvValue(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func resolveCommand(command string) (string, bool) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", false
	}
	if resolved, err := exec.LookPath(command); err == nil {
		return resolved, true
	}
	if fileExists(command) {
		return command, true
	}
	return "", false
}

func defaultModeName(mode config.Mode) string {
	if strings.TrimSpace(string(mode)) == "" {
		return string(config.ModeStudy)
	}
	return string(mode)
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
