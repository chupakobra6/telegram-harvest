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
	"strings"
	"syscall"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/config"
	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/chupakobra6/telegram-harvest/internal/mtproto"
	"github.com/chupakobra6/telegram-harvest/internal/runlock"
	"github.com/chupakobra6/telegram-harvest/internal/transcribe"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin, stdout, stderr *os.File) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	profile, args, err := extractProfileArg(args)
	if err != nil {
		return printError(stderr, 2, err)
	}
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printUsage(stdout)
		return 0
	}
	command := args[0]
	if !knownCommand(command) {
		fmt.Fprintf(stderr, "unknown command: %s\n\n", args[0])
		printUsage(stderr)
		return 2
	}
	if strings.TrimSpace(profile) == "" {
		return printError(stderr, 2, fmt.Errorf("--profile main|study is required"))
	}
	includeDailyRuntime := command == "daily" || command == "daily-catchup"
	projectRoot := detectProjectRoot()
	if err := loadToolDotEnv(projectRoot); err != nil {
		return printError(stderr, 1, err)
	}
	cfg, err := loadProfileConfig(profile)
	if err != nil {
		return printError(stderr, 1, err)
	}
	cfg = cfg.WithRoot(projectRoot)
	client := mtproto.New(cfg)

	switch command {
	case "print-config":
		printConfig(cfg, stdout, includeDailyRuntime)
		return 0
	case "doctor":
		printDoctor(cfg, stdout, client, includeDailyRuntime)
		return 0
	case "login":
		if err := withRuntimeLock(cfg, func() error {
			return client.Login(context.Background(), stdin, stdout)
		}); err != nil {
			return printError(stderr, 1, err)
		}
		return 0
	case "daily":
		if err := runDaily(cfg, client, args[1:], stdout); err != nil {
			return printError(stderr, 1, err)
		}
		return 0
	case "daily-catchup":
		if err := runDailyCatchup(cfg, client, args[1:], stdout); err != nil {
			return printError(stderr, 1, err)
		}
		return 0
	case "daily-download-media":
		if err := runDownloadMedia(cfg, client, args[1:], stdout); err != nil {
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
	case "download-media":
		if err := runDownloadMedia(cfg, client, args[1:], stdout); err != nil {
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

func knownCommand(command string) bool {
	switch command {
	case "print-config", "doctor", "login", "daily", "daily-catchup", "daily-download-media",
		"me", "chats", "topics", "dump", "download-media", "sync", "compact", "agent-view":
		return true
	default:
		return false
	}
}

func extractProfileArg(args []string) (string, []string, error) {
	result := make([]string, 0, len(args))
	profile := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--profile" || arg == "-profile":
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("%s requires a value", arg)
			}
			if profile != "" {
				return "", nil, fmt.Errorf("--profile specified more than once")
			}
			profile = args[i+1]
			i++
		case strings.HasPrefix(arg, "--profile="):
			if profile != "" {
				return "", nil, fmt.Errorf("--profile specified more than once")
			}
			profile = strings.TrimPrefix(arg, "--profile=")
		case strings.HasPrefix(arg, "-profile="):
			if profile != "" {
				return "", nil, fmt.Errorf("--profile specified more than once")
			}
			profile = strings.TrimPrefix(arg, "-profile=")
		default:
			result = append(result, arg)
		}
	}
	return profile, result, nil
}

func loadProfileConfig(profile string) (config.Config, error) {
	return config.LoadProfile(profile)
}

func printUsage(out io.Writer) {
	fmt.Fprintln(out, "usage: telegram-harvest --profile main|study <doctor|print-config|login|me|chats|topics|dump|sync|download-media|compact|agent-view|daily|daily-catchup|daily-download-media> [options]")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Telegram operations are read-only; commands may write local sessions, state, and exports")
	fmt.Fprintln(out, "  --profile main|study  # required account profile")
	fmt.Fprintln(out, "  me [--json]")
	fmt.Fprintln(out, "  chats --query вшэ --limit 300 [--json]  # output is filtered by the study allowlist when set")
	fmt.Fprintln(out, "  topics --chat <allowed-id-or-username> --limit 200 [--json]")
	fmt.Fprintln(out, "  dump --chat <allowed-id-or-username> --limit 500 --out hse-main.jsonl [--download-media --media-dir media]")
	fmt.Fprintln(out, "  sync --chat <allowed-id-or-username> --name hse-main [--all --reset] [--merged-out messages.jsonl] [--download-media --media-dir media]")
	fmt.Fprintln(out, "  download-media --chat <allowed-id-or-username> --message-id 123 --index 1 [--out-dir media-manual]")
	fmt.Fprintln(out, "  compact --in messages.jsonl --out messages.toon [--since 2026-05-01] [--limit 500]")
	fmt.Fprintln(out, "  agent-view --in messages.jsonl --out-dir agent-view [--recent 300] [--rebuild]")
	fmt.Fprintln(out, "  daily --date today [--markdown-out reports/daily/YYYY-MM-DD.md] [--download-media=false]")
	fmt.Fprintln(out, "  daily-catchup [--from YYYY-MM-DD] [--report-dir reports/daily] [--download-media=false]")
	fmt.Fprintln(out, "  daily-download-media --chat <id-or-username> --message-id 123 --index 1 [--out-dir media-manual]")
}

func printError(stderr io.Writer, code int, err error) int {
	fmt.Fprintln(stderr, err)
	return code
}

func printConfig(cfg config.Config, out io.Writer, includeDailyRuntime bool) {
	fmt.Fprintf(out, "profile=%s\n", config.ProfileName(cfg.Mode))
	fmt.Fprintf(out, "app_id_set=%t\n", cfg.AppID != 0)
	fmt.Fprintf(out, "app_hash_set=%t\n", strings.TrimSpace(cfg.AppHash) != "")
	fmt.Fprintf(out, "phone_set=%t\n", strings.TrimSpace(cfg.Phone) != "")
	fmt.Fprintf(out, "session=%s\n", cfg.SessionPath)
	fmt.Fprintf(out, "runtime_lock=%s\n", cfg.RuntimeLockPath())
	fmt.Fprintf(out, "state_dir=%s\n", cfg.StateDir)
	fmt.Fprintf(out, "allowed_chats=%d\n", cfg.AllowedChatCount())
	fmt.Fprintf(out, "rpc_spacing=%s\n", cfg.RPCSpacing)
	if includeDailyRuntime {
		printDailyRuntimeConfig(out, false)
	}
}

func printDoctor(cfg config.Config, out io.Writer, client *mtproto.Client, includeDailyRuntime bool) {
	fmt.Fprintf(out, "profile=%s\n", config.ProfileName(cfg.Mode))
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
	if includeDailyRuntime {
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
	limit := fs.Int("limit", config.DefaultHistoryLimit, "maximum records")
	sinceID := fs.Int("since-id", 0, "only export messages with id greater than this")
	all := fs.Bool("all", false, "export all available history")
	topicID := fs.Int("topic", 0, "forum topic id to export via replies")
	topicTitle := fs.String("topic-title", "", "optional topic title stored in output metadata")
	downloadMedia := fs.Bool("download-media", false, "download supported photo/document attachments while exporting")
	mediaDir := fs.String("media-dir", "media", "media output directory, relative to state dir unless absolute")
	mediaLimits := addMediaLimitFlags(fs)
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
		BatchSize:  config.DefaultBatchSize,
		MinID:      *sinceID,
		All:        *all,
		TopicID:    *topicID,
		TopicTitle: *topicTitle,
	}
	if *downloadMedia {
		history.DownloadMedia = true
		history.MediaDir = resolveOutputPath(cfg.StateDir, *mediaDir)
		applyMediaLimits(&history, mediaLimits)
		history.ManualDownloadCommand = "telegram-harvest download-media"
	}
	if *all {
		history.Limit = 0
		history.MaxBatches = 0
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
	limit := fs.Int("limit", config.DefaultHistoryLimit, "maximum new records")
	mergedOut := fs.String("merged-out", "", "optional append-only merged JSONL output")
	all := fs.Bool("all", false, "sync all available history")
	reset := fs.Bool("reset", false, "truncate this stream and reset its state before syncing")
	resetMerged := fs.Bool("reset-merged", false, "truncate merged output before writing")
	topicID := fs.Int("topic", 0, "forum topic id to sync via replies")
	topicTitle := fs.String("topic-title", "", "optional topic title stored in state metadata")
	downloadMedia := fs.Bool("download-media", false, "download supported photo/document attachments while syncing")
	mediaDir := fs.String("media-dir", "media", "media output directory, relative to state dir unless absolute")
	mediaLimits := addMediaLimitFlags(fs)
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
		BatchSize:  config.DefaultBatchSize,
		All:        *all,
		TopicID:    *topicID,
		TopicTitle: *topicTitle,
	}
	if *downloadMedia {
		history.DownloadMedia = true
		history.MediaDir = resolveOutputPath(cfg.StateDir, *mediaDir)
		applyMediaLimits(&history, mediaLimits)
		history.ManualDownloadCommand = "telegram-harvest download-media"
	}
	if *all {
		history.Limit = 0
		history.MaxBatches = 0
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
	markdownOut := fs.String("markdown-out", defaultMarkdown, "Markdown report output path; default writes to visible reports/daily")
	dailyFlags := addDailyOptionFlags(fs, defaults)
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
	job := dailyJob{
		Date:         dateLabel,
		Start:        start,
		End:          end,
		OutputPath:   outputPath,
		MarkdownPath: markdownPath,
	}
	return runDailyJobs(cfg, client, []dailyJob{job}, dailyFlags.values(), out)
}

func runDailyCatchup(cfg config.Config, client *mtproto.Client, args []string, out io.Writer) error {
	defaults := dailyRuntimeDefaults()
	defaultReportDir := harvest.DailyDefaultReportRoot(cfg.StateDir)

	fs := flag.NewFlagSet("daily-catchup", flag.ContinueOnError)
	fs.SetOutput(out)
	fromRaw := fs.String("from", "", "first day to generate, YYYY-MM-DD; default starts after newest Markdown report")
	reportDirRaw := fs.String("report-dir", defaultReportDir, "directory with daily Markdown reports")
	dailyFlags := addDailyOptionFlags(fs, defaults)
	if err := fs.Parse(args); err != nil {
		return err
	}

	reportDir := resolveReportDirPath(*reportDirRaw)
	plan, err := buildDailyCatchupPlan(cfg, reportDir, *fromRaw, time.Now())
	if err != nil {
		return err
	}
	if len(plan.Skipped) > 0 {
		for _, skipped := range plan.Skipped {
			fmt.Fprintf(out, "skip date=%s reason=markdown_exists\n", skipped)
		}
	}
	if len(plan.Jobs) == 0 {
		fmt.Fprintf(out, "catchup up_to_date=true last_report=%s today=%s report_dir=%s skipped=%d\n",
			plan.LastReport,
			plan.Today,
			reportDir,
			len(plan.Skipped),
		)
		return nil
	}
	fmt.Fprintf(out, "catchup start=%s end=%s today=%s report_dir=%s planned=%d skipped=%d\n",
		plan.Jobs[0].Date,
		plan.Jobs[len(plan.Jobs)-1].Date,
		plan.Today,
		reportDir,
		len(plan.Jobs),
		len(plan.Skipped),
	)
	if err := runDailyJobs(cfg, client, plan.Jobs, dailyFlags.values(), out); err != nil {
		return err
	}
	fmt.Fprintf(out, "catchup complete=true generated=%d skipped=%d\n", len(plan.Jobs), len(plan.Skipped))
	return nil
}

type dailyOptionFlags struct {
	dialogLimit       *int
	limit             *int
	includeService    *bool
	downloadMedia     *bool
	mediaDir          *string
	mediaLimits       mediaLimitFlags
	transcribeMedia   *bool
	transcribeCommand *string
	voskCommand       *string
	voskModelPath     *string
	voskGrammarPath   *string
	ffmpegCommand     *string
	transcriptDir     *string
	progress          *bool
}

func addDailyOptionFlags(fs *flag.FlagSet, defaults dailyRuntimeConfig) dailyOptionFlags {
	flags := dailyOptionFlags{
		dialogLimit:     fs.Int("dialog-limit", dailyDialogLimitDefault(), "maximum dialogs to scan"),
		limit:           fs.Int("limit", 0, "maximum newest records to write after filtering; 0 means all"),
		includeService:  fs.Bool("include-service", false, "include Telegram service messages"),
		downloadMedia:   fs.Bool("download-media", true, "download photos and image documents; audio/video is downloaded temporarily for transcription"),
		mediaDir:        fs.String("media-dir", "media", "media output directory, relative to state dir unless absolute"),
		transcribeMedia: fs.Bool("transcribe", defaults.TranscribeMedia, "transcribe voice/audio/video media; cached transcripts skip media download"),
		transcribeCommand: fs.String("transcribe-cmd", defaults.TranscribeCommand,
			"custom shell command template override; supports {input}, {output}, {output_dir}, {output_base}"),
		voskCommand:     fs.String("vosk-command", defaults.VoskCommand, "Vosk session worker command, called as: command --session <model> [grammar]"),
		voskModelPath:   fs.String("vosk-model", defaults.VoskModelPath, "Vosk model directory"),
		voskGrammarPath: fs.String("vosk-grammar", defaults.VoskGrammarPath, "optional Vosk grammar JSON path"),
		ffmpegCommand:   fs.String("ffmpeg-command", defaults.FFmpegCommand, "ffmpeg command for audio extraction and WAV conversion"),
		transcriptDir:   fs.String("transcript-dir", "transcripts", "transcript output directory, relative to state dir unless absolute"),
		progress:        fs.Bool("progress", false, "print per-dialog progress"),
	}
	flags.mediaLimits = addMediaLimitFlags(fs)
	return flags
}

func (f dailyOptionFlags) values() dailyOptions {
	return dailyOptions{
		DialogLimit:       *f.dialogLimit,
		Limit:             *f.limit,
		IncludeService:    *f.includeService,
		DownloadMedia:     *f.downloadMedia,
		MediaDir:          *f.mediaDir,
		MaxPhotoBytes:     *f.mediaLimits.photo,
		MaxDocumentBytes:  *f.mediaLimits.document,
		MaxAudioBytes:     *f.mediaLimits.audio,
		MaxVideoBytes:     *f.mediaLimits.video,
		TranscribeMedia:   *f.transcribeMedia,
		TranscribeCommand: *f.transcribeCommand,
		VoskCommand:       *f.voskCommand,
		VoskModelPath:     *f.voskModelPath,
		VoskGrammarPath:   *f.voskGrammarPath,
		FFmpegCommand:     *f.ffmpegCommand,
		TranscriptDir:     *f.transcriptDir,
		Progress:          *f.progress,
	}
}

type dailyOptions struct {
	DialogLimit       int
	Limit             int
	IncludeService    bool
	DownloadMedia     bool
	MediaDir          string
	MaxPhotoBytes     int64
	MaxDocumentBytes  int64
	MaxAudioBytes     int64
	MaxVideoBytes     int64
	TranscribeMedia   bool
	TranscribeCommand string
	VoskCommand       string
	VoskModelPath     string
	VoskGrammarPath   string
	FFmpegCommand     string
	TranscriptDir     string
	Progress          bool
}

type dailyJob struct {
	Date         string
	Start        time.Time
	End          time.Time
	OutputPath   string
	MarkdownPath string
}

type dailyCatchupPlan struct {
	Jobs       []dailyJob
	Skipped    []string
	LastReport string
	Today      string
}

func runDailyJobs(cfg config.Config, client *mtproto.Client, jobs []dailyJob, opts dailyOptions, out io.Writer) error {
	if len(jobs) == 0 {
		return nil
	}
	history := dailyHistoryOptions(cfg, opts)
	var managedTranscriber transcribe.ManagedRunner
	if opts.TranscribeMedia {
		if transcribeOpts := dailyTranscribeOptions(history); transcribeOpts.Configured() {
			managedTranscriber = transcribe.NewManagedRunner(transcribeOpts)
			history.Transcriber = managedTranscriber
		}
	}
	err := withRuntimeLock(cfg, func() error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return client.RunAuthorized(ctx, func(ctx context.Context, session *mtproto.Session) error {
			for _, job := range jobs {
				if err := runDailyJob(ctx, session, history, opts, job, out); err != nil {
					if errors.Is(err, context.Canceled) {
						fmt.Fprintf(out, "interrupted=true date=%s out=%s\n", job.Date, job.OutputPath)
						return nil
					}
					return err
				}
			}
			return nil
		})
	})
	if managedTranscriber != nil {
		if closeErr := managedTranscriber.Close(); err == nil && closeErr != nil {
			return closeErr
		}
	}
	return err
}

func dailyHistoryOptions(cfg config.Config, opts dailyOptions) harvest.HistoryOptions {
	history := harvest.HistoryOptions{
		Limit:             opts.Limit,
		BatchSize:         config.DefaultBatchSize,
		MaxBatches:        0,
		DownloadMedia:     opts.DownloadMedia,
		TranscribeMedia:   opts.TranscribeMedia,
		TranscribeCommand: opts.TranscribeCommand,
		VoskCommand:       opts.VoskCommand,
		VoskModelPath:     opts.VoskModelPath,
		VoskGrammarPath:   opts.VoskGrammarPath,
		FFmpegCommand:     opts.FFmpegCommand,
		MaxPhotoBytes:     opts.MaxPhotoBytes,
		MaxDocumentBytes:  opts.MaxDocumentBytes,
		MaxAudioBytes:     opts.MaxAudioBytes,
		MaxVideoBytes:     opts.MaxVideoBytes,
	}
	history.ManualDownloadCommand = "telegram-harvest daily-download-media"
	if opts.DownloadMedia {
		history.MediaDir = resolveOutputPath(cfg.StateDir, opts.MediaDir)
	}
	if opts.TranscribeMedia {
		history.TranscriptDir = resolveOutputPath(cfg.StateDir, opts.TranscriptDir)
	}
	return history
}

func runDailyJob(ctx context.Context, session *mtproto.Session, history harvest.HistoryOptions, opts dailyOptions, job dailyJob, out io.Writer) error {
	records := make([]harvest.MessageRecord, 0)
	encoder, file, err := harvest.OpenJSONL(job.OutputPath, false)
	if err != nil {
		return err
	}
	defer file.Close()
	progress := func(progress harvest.OutgoingDayProgress) error {
		if !opts.Progress {
			return nil
		}
		if progress.Skipped {
			fmt.Fprintf(out, "progress date=%s skipped=true chat=%s total=%d flood_waits=%d\n", job.Date, progress.Chat.Display, progress.Total, progress.FloodWaits)
			return nil
		}
		if progress.Error != "" {
			fmt.Fprintf(out, "progress date=%s error=true chat=%s detail=%s total=%d batches=%d flood_waits=%d\n", job.Date, progress.Chat.Display, progress.Error, progress.Total, progress.Batches, progress.FloodWaits)
			return nil
		}
		fmt.Fprintf(out, "progress date=%s chat=%s records=%d total=%d batches=%d flood_waits=%d\n", job.Date, progress.Chat.Display, progress.Records, progress.Total, progress.Batches, progress.FloodWaits)
		return nil
	}
	stats, err := session.DumpOutgoingDay(ctx, harvest.OutgoingDayOptions{
		Start:          job.Start,
		End:            job.End,
		DialogLimit:    opts.DialogLimit,
		IncludeService: opts.IncludeService,
		History:        history,
		Progress:       progress,
	}, func(record harvest.MessageRecord) error {
		records = append(records, record)
		return encoder.Encode(record)
	})
	if err != nil {
		return err
	}
	if job.MarkdownPath != "" {
		if err := harvest.WriteDailyMarkdown(harvest.DailyMarkdownOptions{
			OutputPath: job.MarkdownPath,
			Date:       job.Date,
			Start:      job.Start,
			End:        job.End,
			Stats:      stats,
			Records:    records,
		}); err != nil {
			return err
		}
	}
	for _, dialogErr := range stats.DialogErrors {
		fmt.Fprintf(out, "warning dialog_error=%s\n", dialogErr)
	}
	fmt.Fprintf(out, "date=%s wrote=%d dialogs=%d dialogs_with_records=%d attachments=%d transcripts=%d out=%s",
		job.Date,
		stats.Records,
		stats.DialogsScanned,
		stats.DialogsWithRecords,
		stats.Attachments,
		stats.Transcripts,
		job.OutputPath,
	)
	if job.MarkdownPath != "" {
		fmt.Fprintf(out, " markdown=%s", job.MarkdownPath)
	}
	fmt.Fprintf(out, " flood_waits=%d complete=%t\n",
		stats.FloodWaits,
		stats.Complete,
	)
	return nil
}

type mediaLimitFlags struct {
	photo    *int64
	document *int64
	audio    *int64
	video    *int64
}

func addMediaLimitFlags(fs *flag.FlagSet) mediaLimitFlags {
	return mediaLimitFlags{
		photo:    fs.Int64("max-photo-bytes", harvest.DefaultMaxPhotoBytes, "maximum photo/image bytes to download; 0 disables this cap"),
		document: fs.Int64("max-document-bytes", harvest.DefaultMaxDocumentBytes, "maximum generic document bytes to download; 0 disables this cap"),
		audio:    fs.Int64("max-audio-bytes", harvest.DefaultMaxAudioBytes, "maximum voice/audio bytes to download or transcribe; 0 disables this cap"),
		video:    fs.Int64("max-video-bytes", harvest.DefaultMaxVideoBytes, "maximum video/round-video bytes to download or transcribe; 0 disables this cap"),
	}
}

func applyMediaLimits(opts *harvest.HistoryOptions, limits mediaLimitFlags) {
	if opts == nil {
		return
	}
	opts.MaxPhotoBytes = *limits.photo
	opts.MaxDocumentBytes = *limits.document
	opts.MaxAudioBytes = *limits.audio
	opts.MaxVideoBytes = *limits.video
}

func runDownloadMedia(cfg config.Config, client *mtproto.Client, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("download-media", flag.ContinueOnError)
	fs.SetOutput(out)
	chat := fs.String("chat", "", "chat id or @username")
	messageID := fs.Int("message-id", 0, "Telegram message id")
	index := fs.Int("index", 1, "1-based attachment index")
	outDir := fs.String("out-dir", "media-manual", "download output directory, relative to state dir unless absolute")
	overwrite := fs.Bool("overwrite", false, "replace an existing downloaded file")
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*chat) == "" {
		return fmt.Errorf("--chat is required")
	}
	if cfg.Mode != config.ModeMain {
		if err := ensureAllowedChat(cfg, *chat); err != nil {
			return err
		}
	}
	if *messageID <= 0 {
		return fmt.Errorf("--message-id must be > 0")
	}
	if *index <= 0 {
		return fmt.Errorf("--index must be > 0")
	}
	mediaDir := resolveOutputPath(cfg.StateDir, *outDir)
	var result mtproto.DownloadMediaResult
	if err := withRuntimeLock(cfg, func() error {
		return client.RunAuthorized(context.Background(), func(ctx context.Context, session *mtproto.Session) error {
			var err error
			result, err = session.DownloadMessageMedia(ctx, *chat, *messageID, mtproto.DownloadMediaOptions{
				MediaDir:  mediaDir,
				Index:     *index,
				Overwrite: *overwrite,
			})
			return err
		})
	}); err != nil {
		return err
	}
	if *jsonOut {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	fmt.Fprintf(out, "downloaded=true chat=%d message_id=%d index=%d kind=%s path=%s size=%d\n",
		result.Record.Chat.ID,
		result.Record.MessageID,
		*index,
		result.Attachment.Kind,
		result.Attachment.LocalPath,
		result.Attachment.Size,
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

func resolveReportDirPath(reportDir string) string {
	reportDir = strings.TrimSpace(reportDir)
	if reportDir == "" || filepath.IsAbs(reportDir) {
		return reportDir
	}
	return filepath.Join(detectProjectRoot(), reportDir)
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
	if parsed, err := time.ParseInLocation("2006-01-02", value, moscowLocation()); err == nil {
		return parsed, nil
	}
	return time.Time{}, fmt.Errorf("--since must be YYYY-MM-DD or RFC3339")
}

func parseDailyDate(value string, now time.Time) (string, time.Time, time.Time, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		value = "today"
	}
	moscow := moscowLocation()
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

func moscowLocation() *time.Location {
	return time.FixedZone("Europe/Moscow", 3*60*60)
}

func moscowDay(now time.Time) time.Time {
	moscow := moscowLocation()
	now = now.In(moscow)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, moscow)
}

func parseDailyDay(value string) (time.Time, bool) {
	day, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(value), moscowLocation())
	return day, err == nil
}

func buildDailyCatchupPlan(cfg config.Config, reportDir string, fromRaw string, now time.Time) (dailyCatchupPlan, error) {
	today := moscowDay(now)
	plan := dailyCatchupPlan{Today: today.Format("2006-01-02")}

	var start time.Time
	if strings.TrimSpace(fromRaw) != "" {
		day, ok := parseDailyDay(fromRaw)
		if !ok {
			return dailyCatchupPlan{}, fmt.Errorf("--from must be YYYY-MM-DD")
		}
		start = day
		plan.LastReport = "manual:" + day.AddDate(0, 0, -1).Format("2006-01-02")
	} else {
		latest, ok, err := latestDailyReportDate(reportDir, today)
		if err != nil {
			return dailyCatchupPlan{}, err
		}
		if !ok {
			return dailyCatchupPlan{}, fmt.Errorf("no previous daily Markdown reports found in %s; pass --from YYYY-MM-DD for the first catch-up", reportDir)
		}
		start = latest.AddDate(0, 0, 1)
		plan.LastReport = latest.Format("2006-01-02")
	}

	for day := start; day.Before(today); day = day.AddDate(0, 0, 1) {
		date := day.Format("2006-01-02")
		markdownPath := filepath.Join(reportDir, date+".md")
		if fileExists(markdownPath) {
			plan.Skipped = append(plan.Skipped, date)
			continue
		}
		jsonlPath, _ := harvest.DailyDefaultOutputPaths(cfg.StateDir, date)
		plan.Jobs = append(plan.Jobs, dailyJob{
			Date:         date,
			Start:        day,
			End:          day.AddDate(0, 0, 1),
			OutputPath:   jsonlPath,
			MarkdownPath: markdownPath,
		})
	}
	return plan, nil
}

func latestDailyReportDate(reportDir string, before time.Time) (time.Time, bool, error) {
	entries, err := os.ReadDir(reportDir)
	if os.IsNotExist(err) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	var latest time.Time
	found := false
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if filepath.Ext(name) != ".md" {
			continue
		}
		day, ok := parseDailyDay(strings.TrimSuffix(name, filepath.Ext(name)))
		if !ok || !day.Before(before) {
			continue
		}
		if !found || day.After(latest) {
			latest = day
			found = true
		}
	}
	return latest, found, nil
}

func dailyDialogLimitDefault() int {
	return mtproto.DefaultDailyDialogLimit()
}

func dailyTranscribeOptions(opts harvest.HistoryOptions) transcribe.Options {
	return transcribe.Options{
		CommandTemplate: opts.TranscribeCommand,
		VoskCommand:     opts.VoskCommand,
		VoskModelPath:   opts.VoskModelPath,
		VoskGrammarPath: opts.VoskGrammarPath,
		FFmpegCommand:   opts.FFmpegCommand,
	}
}

type dailyRuntimeConfig struct {
	TranscribeMedia   bool
	TranscribeCommand string
	VoskCommand       string
	VoskModelPath     string
	VoskGrammarPath   string
	FFmpegCommand     string
}

func dailyRuntimeDefaults() dailyRuntimeConfig {
	transcribeCommand := firstEnvValue("TG_HARVEST_DAILY_TRANSCRIBE_CMD")
	voskCommand := firstEnvValue("TG_HARVEST_DAILY_VOSK_COMMAND")
	if voskCommand == "" {
		if candidate := defaultLocalVoskCommandPath(); candidate != "" {
			voskCommand = candidate
		} else if resolved, err := exec.LookPath("vosk-transcribe"); err == nil {
			voskCommand = resolved
		}
	}
	voskModelPath := firstEnvValue("TG_HARVEST_DAILY_VOSK_MODEL_PATH")
	if voskModelPath == "" {
		if candidate := defaultLocalVoskModelPath(); candidate != "" {
			voskModelPath = candidate
		}
	}
	ffmpegCommand := firstEnvValue("TG_HARVEST_DAILY_FFMPEG_COMMAND")
	if ffmpegCommand == "" {
		ffmpegCommand = transcribe.DefaultFFmpegCommand
	}
	return dailyRuntimeConfig{
		TranscribeMedia:   strings.TrimSpace(transcribeCommand) != "" || (strings.TrimSpace(voskCommand) != "" && strings.TrimSpace(voskModelPath) != ""),
		TranscribeCommand: transcribeCommand,
		VoskCommand:       voskCommand,
		VoskModelPath:     voskModelPath,
		VoskGrammarPath:   firstEnvValue("TG_HARVEST_DAILY_VOSK_GRAMMAR_PATH"),
		FFmpegCommand:     ffmpegCommand,
	}
}

func defaultLocalVoskCommandPath() string {
	projectRoot := detectProjectRoot()
	if projectRoot == "" {
		return ""
	}
	candidate := filepath.Join(projectRoot, "bin", "vosk-transcribe")
	if fileExists(candidate) {
		return candidate
	}
	return ""
}

func defaultLocalVoskModelPath() string {
	projectRoot := detectProjectRoot()
	if projectRoot == "" {
		return ""
	}
	candidate := filepath.Join(projectRoot, "models", "vosk-model-small-ru-0.22")
	if fileExists(candidate) {
		return candidate
	}
	return ""
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
