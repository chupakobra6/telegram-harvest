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
	DefaultRPCSpacingMS    = 1500
	DefaultBatchSize       = 80
	DefaultHistoryLimit    = 500
	DefaultMaxBatches      = 20
)

type Config struct {
	Mode         Mode
	AppID        int
	AppHash      string
	Phone        string
	Password     string
	SessionPath  string
	StateDir     string
	AllowedChats []string
	RPCSpacing   time.Duration
	BatchSize    int
	HistoryLimit int
	MaxBatches   int
}

type Mode string

const (
	ModeStudy Mode = "study"
	ModeMain  Mode = "main"
)

func Load() (Config, error) {
	return LoadStudy()
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
	case "", "study":
		return ModeStudy, nil
	case "main":
		return ModeMain, nil
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
	rpcSpacingMS, err := intFromEnvAny(envKeys(mode, "RPC_SPACING_MS"), DefaultRPCSpacingMS)
	if err != nil {
		return Config{}, err
	}
	batchSize, err := intFromEnvAny(envKeys(mode, "HISTORY_BATCH_SIZE"), DefaultBatchSize)
	if err != nil {
		return Config{}, err
	}
	historyLimit, err := intFromEnvAny(envKeys(mode, "HISTORY_LIMIT"), DefaultHistoryLimit)
	if err != nil {
		return Config{}, err
	}
	maxBatches, err := intFromEnvAny(envKeys(mode, "MAX_BATCHES"), DefaultMaxBatches)
	if err != nil {
		return Config{}, err
	}

	return Config{
		Mode:         mode,
		AppID:        appID,
		AppHash:      firstEnv(envKeys(mode, "APP_HASH")...),
		Phone:        firstEnv(envKeys(mode, "PHONE")...),
		Password:     firstEnv(envKeys(mode, "PASSWORD")...),
		SessionPath:  defaultString(firstEnv(envKeys(mode, "SESSION_PATH")...), defaultSessionPath),
		StateDir:     defaultString(firstEnv(envKeys(mode, "STATE_DIR")...), defaultStateDir),
		AllowedChats: splitList(firstEnv(envKeys(mode, "ALLOWED_CHATS")...)),
		RPCSpacing:   time.Duration(rpcSpacingMS) * time.Millisecond,
		BatchSize:    batchSize,
		HistoryLimit: historyLimit,
		MaxBatches:   maxBatches,
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
	if c.BatchSize <= 0 || c.BatchSize > 100 {
		return fmt.Errorf("history batch size must be between 1 and 100")
	}
	if c.HistoryLimit <= 0 {
		return fmt.Errorf("history limit must be > 0")
	}
	if c.MaxBatches <= 0 {
		return fmt.Errorf("max batches must be > 0")
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
	if c.Mode == ModeMain {
		return "telegram-harvest login"
	}
	return "telegram-harvest --profile " + ProfileName(c.Mode) + " login"
}

func (c Config) RuntimeLockPath() string {
	sessionPath := c.SessionPath
	if sessionPath == "" {
		sessionPath = DefaultSessionPath
	}
	dir := filepath.Dir(sessionPath)
	if dir == "." || dir == "" {
		return "runtime.lock"
	}
	return filepath.Join(dir, "runtime.lock")
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
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
