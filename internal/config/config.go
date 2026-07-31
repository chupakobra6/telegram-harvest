package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultSessionPath     = ".sessions/study.json"
	DefaultStateDir        = ".state"
	DefaultMainSessionPath = ".sessions/main.json"
	DefaultMainStateDir    = ".state/daily"
	// Static floor selected by live sequential calibration. Lower
	// 400-450 ms candidates produced FloodWait under a sustained 103-RPC burst.
	DefaultRPCSpacingMS = 500
	// Project cap for Telegram history/search requests.
	DefaultBatchSize = 100
	// One full history batch for non-backfill reads; use --all for exhaustive scans.
	DefaultHistoryLimit = 100
)

type Config struct {
	Mode                   Mode
	AppID                  int
	AppHash                string
	Phone                  string
	Password               string
	SessionPath            string
	StateDir               string
	AllowedChats           []string
	DailyAdditionalSenders []DailyAdditionalSender
	RPCSpacing             time.Duration
}

type DailyAdditionalSender struct {
	ChatID   int64
	SenderID int64
}

type Mode string

const (
	ModeStudy Mode = "study"
	ModeMain  Mode = "main"
)

func Load() (Config, error) {
	return Config{}, fmt.Errorf("profile is required; use main or study")
}

func LoadStudy() (Config, error) {
	return loadMode(ModeStudy, DefaultSessionPath, DefaultStateDir)
}

func LoadMain() (Config, error) {
	return loadMode(ModeMain, DefaultMainSessionPath, DefaultMainStateDir)
}

func LoadProfile(profile string) (Config, error) {
	mode, err := ProfileMode(profile)
	if err != nil {
		return Config{}, err
	}
	switch mode {
	case ModeMain:
		return LoadMain()
	default:
		return LoadStudy()
	}
}

func ProfileMode(profile string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(profile)) {
	case "study":
		return ModeStudy, nil
	case "main":
		return ModeMain, nil
	case "":
		return "", fmt.Errorf("profile is required; use main or study")
	default:
		return "", fmt.Errorf("unknown profile %q; use main or study", profile)
	}
}

func ProfileName(mode Mode) string {
	switch mode {
	case ModeMain:
		return "main"
	default:
		return "study"
	}
}

func loadMode(mode Mode, defaultSessionPath string, defaultStateDir string) (Config, error) {
	appID, err := intFromEnvAny(envKeys(mode, "APP_ID"), 0)
	if err != nil {
		return Config{}, err
	}
	additionalSenders := []DailyAdditionalSender(nil)
	if mode == ModeMain {
		additionalSenders, err = parseDailyAdditionalSenders(firstEnv(envKeys(mode, "ADDITIONAL_SENDERS")...))
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", envKeys(mode, "ADDITIONAL_SENDERS")[0], err)
		}
	}
	return Config{
		Mode:                   mode,
		AppID:                  appID,
		AppHash:                firstEnv(envKeys(mode, "APP_HASH")...),
		Phone:                  firstEnv(envKeys(mode, "PHONE")...),
		Password:               firstEnv(envKeys(mode, "PASSWORD")...),
		SessionPath:            defaultString(firstEnv(envKeys(mode, "SESSION_PATH")...), defaultSessionPath),
		StateDir:               defaultString(firstEnv(envKeys(mode, "STATE_DIR")...), defaultStateDir),
		AllowedChats:           splitList(firstEnv(envKeys(mode, "ALLOWED_CHATS")...)),
		DailyAdditionalSenders: additionalSenders,
		RPCSpacing:             time.Duration(DefaultRPCSpacingMS) * time.Millisecond,
	}, nil
}

func envKeys(mode Mode, suffix string) []string {
	switch mode {
	case ModeMain:
		return []string{
			"TG_HARVEST_DAILY_" + suffix,
		}
	default:
		return []string{
			"TG_HARVEST_STUDY_" + suffix,
		}
	}
}

func displayEnvKeys(mode Mode, suffix string) []string {
	switch mode {
	case ModeMain:
		return []string{
			"TG_HARVEST_DAILY_" + suffix,
		}
	default:
		return envKeys(mode, suffix)
	}
}

func (c Config) ValidateLogin() error {
	return c.ValidateRuntime()
}

func (c Config) ChatAllowed(chat string) bool {
	if len(c.AllowedChats) == 0 {
		return true
	}
	key := chatKey(chat)
	if key == "" {
		return false
	}
	for _, allowed := range c.AllowedChats {
		if chatKey(allowed) == key {
			return true
		}
	}
	return false
}

func (c Config) AllowedChatCount() int {
	return len(c.AllowedChats)
}

func (c Config) DailyAdditionalSenderCount() int {
	return len(c.DailyAdditionalSenders)
}

func (c Config) ValidateRuntime() error {
	if c.AppID == 0 {
		return fmt.Errorf("%s is required", c.EnvName("APP_ID"))
	}
	if strings.TrimSpace(c.AppHash) == "" {
		return fmt.Errorf("%s is required", c.EnvName("APP_HASH"))
	}
	if strings.TrimSpace(c.SessionPath) == "" {
		return fmt.Errorf("%s is required", c.EnvName("SESSION_PATH"))
	}
	if strings.TrimSpace(c.StateDir) == "" {
		return fmt.Errorf("%s is required", c.EnvName("STATE_DIR"))
	}
	if c.RPCSpacing <= 0 {
		return fmt.Errorf("rpc spacing must be > 0")
	}
	return nil
}

func (c Config) WithRoot(root string) Config {
	if root == "" {
		return c
	}
	c.SessionPath = resolvePath(root, c.SessionPath)
	c.StateDir = resolvePath(root, c.StateDir)
	return c
}

func (c Config) EnvName(suffix string) string {
	keys := displayEnvKeys(c.Mode, suffix)
	return keys[0]
}

func (c Config) EnvNames(suffix string) string {
	return strings.Join(displayEnvKeys(c.Mode, suffix), " or ")
}

func (c Config) LoginCommand() string {
	return "telegram-harvest --profile " + ProfileName(c.Mode) + " login"
}

func (c Config) RuntimeLockPath() string {
	sessionPath := strings.TrimSpace(c.SessionPath)
	if sessionPath == "" {
		if c.Mode == ModeMain {
			sessionPath = DefaultMainSessionPath
		} else {
			sessionPath = DefaultSessionPath
		}
	}
	return sessionPath + ".runtime.lock"
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func parseDailyAdditionalSenders(value string) ([]DailyAdditionalSender, error) {
	items := splitList(value)
	result := make([]DailyAdditionalSender, 0, len(items))
	seen := make(map[DailyAdditionalSender]struct{}, len(items))
	for _, item := range items {
		parts := strings.Split(item, ":")
		if len(parts) != 2 {
			return nil, fmt.Errorf("expected comma-separated chat_id:sender_id pairs")
		}
		chatID, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
		if err != nil || chatID == 0 {
			return nil, fmt.Errorf("invalid chat id in %q", item)
		}
		senderID, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		if err != nil || senderID == 0 {
			return nil, fmt.Errorf("invalid sender id in %q", item)
		}
		source := DailyAdditionalSender{ChatID: chatID, SenderID: senderID}
		if _, ok := seen[source]; ok {
			continue
		}
		seen[source] = struct{}{}
		result = append(result, source)
	}
	return result, nil
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func splitList(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\t' || r == ' '
	})
	result := make([]string, 0, len(fields))
	seen := map[string]struct{}{}
	for _, field := range fields {
		trimmed := strings.TrimSpace(field)
		key := chatKey(trimmed)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}

func chatKey(value string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "@"))
}

func intFromEnvAny(keys []string, fallback int) (int, error) {
	for _, key := range keys {
		value := strings.TrimSpace(os.Getenv(key))
		if value == "" {
			continue
		}
		n, err := strconv.Atoi(value)
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer: %w", key, err)
		}
		return n, nil
	}
	return fallback, nil
}

func resolvePath(root string, value string) string {
	if value == "" || filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(root, value)
}
