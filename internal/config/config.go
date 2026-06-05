package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
)

const (
	DefaultSessionPath      = ".sessions/user.json"
	DefaultStateDir         = ".state"
	DefaultDailySessionPath = ".sessions/daily.json"
	DefaultDailyStateDir    = ".state/daily"
	DefaultRPCSpacingMS     = 1500
	DefaultBatchSize        = 80
	DefaultHistoryLimit     = 500
	DefaultMaxBatches       = 20
)

type Config struct {
	Profile      string
	EnvPrefix    string
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

func Load() (Config, error) {
	return LoadStudy()
}

func LoadStudy() (Config, error) {
	return loadProfile(profileSpec{
		Profile:            "study",
		EnvPrefix:          "TG_STUDY",
		AppIDEnv:           []string{"TG_STUDY_APP_ID", "TG_E2E_APP_ID"},
		AppHashEnv:         []string{"TG_STUDY_APP_HASH", "TG_E2E_APP_HASH"},
		PhoneEnv:           []string{"TG_STUDY_PHONE", "TG_E2E_PHONE"},
		PasswordEnv:        []string{"TG_STUDY_PASSWORD", "TG_E2E_PASSWORD"},
		SessionPathEnv:     []string{"TG_STUDY_SESSION_PATH"},
		StateDirEnv:        []string{"TG_STUDY_STATE_DIR"},
		AllowedChatsEnv:    []string{"TG_STUDY_ALLOWED_CHATS"},
		RPCSpacingMSEnv:    []string{"TG_STUDY_RPC_SPACING_MS"},
		HistoryBatchEnv:    []string{"TG_STUDY_HISTORY_BATCH_SIZE"},
		HistoryLimitEnv:    []string{"TG_STUDY_HISTORY_LIMIT"},
		MaxBatchesEnv:      []string{"TG_STUDY_MAX_BATCHES"},
		DefaultSessionPath: DefaultSessionPath,
		DefaultStateDir:    DefaultStateDir,
	})
}

func LoadDaily() (Config, error) {
	return loadProfile(profileSpec{
		Profile:            "daily",
		EnvPrefix:          "TG_DAILY",
		AppIDEnv:           []string{"TG_DAILY_APP_ID", "TG_HARVEST_APP_ID"},
		AppHashEnv:         []string{"TG_DAILY_APP_HASH", "TG_HARVEST_APP_HASH"},
		PhoneEnv:           []string{"TG_DAILY_PHONE", "TG_HARVEST_PHONE"},
		PasswordEnv:        []string{"TG_DAILY_PASSWORD", "TG_HARVEST_PASSWORD"},
		SessionPathEnv:     []string{"TG_DAILY_SESSION_PATH", "TG_HARVEST_SESSION_PATH"},
		StateDirEnv:        []string{"TG_DAILY_STATE_DIR", "TG_HARVEST_STATE_DIR"},
		AllowedChatsEnv:    []string{"TG_DAILY_ALLOWED_CHATS", "TG_HARVEST_ALLOWED_CHATS"},
		RPCSpacingMSEnv:    []string{"TG_DAILY_RPC_SPACING_MS", "TG_HARVEST_RPC_SPACING_MS"},
		HistoryBatchEnv:    []string{"TG_DAILY_HISTORY_BATCH_SIZE", "TG_HARVEST_HISTORY_BATCH_SIZE"},
		HistoryLimitEnv:    []string{"TG_DAILY_HISTORY_LIMIT", "TG_HARVEST_HISTORY_LIMIT"},
		MaxBatchesEnv:      []string{"TG_DAILY_MAX_BATCHES", "TG_HARVEST_MAX_BATCHES"},
		DefaultSessionPath: DefaultDailySessionPath,
		DefaultStateDir:    DefaultDailyStateDir,
	})
}

type profileSpec struct {
	Profile            string
	EnvPrefix          string
	AppIDEnv           []string
	AppHashEnv         []string
	PhoneEnv           []string
	PasswordEnv        []string
	SessionPathEnv     []string
	StateDirEnv        []string
	AllowedChatsEnv    []string
	RPCSpacingMSEnv    []string
	HistoryBatchEnv    []string
	HistoryLimitEnv    []string
	MaxBatchesEnv      []string
	DefaultSessionPath string
	DefaultStateDir    string
}

func loadProfile(spec profileSpec) (Config, error) {
	appID, err := intFromEnvAny(spec.AppIDEnv, 0)
	if err != nil {
		return Config{}, err
	}
	rpcSpacingMS, err := intFromEnvAny(spec.RPCSpacingMSEnv, DefaultRPCSpacingMS)
	if err != nil {
		return Config{}, err
	}
	batchSize, err := intFromEnvAny(spec.HistoryBatchEnv, DefaultBatchSize)
	if err != nil {
		return Config{}, err
	}
	historyLimit, err := intFromEnvAny(spec.HistoryLimitEnv, DefaultHistoryLimit)
	if err != nil {
		return Config{}, err
	}
	maxBatches, err := intFromEnvAny(spec.MaxBatchesEnv, DefaultMaxBatches)
	if err != nil {
		return Config{}, err
	}

	return Config{
		Profile:      spec.Profile,
		EnvPrefix:    spec.EnvPrefix,
		AppID:        appID,
		AppHash:      firstEnv(spec.AppHashEnv...),
		Phone:        firstEnv(spec.PhoneEnv...),
		Password:     firstEnv(spec.PasswordEnv...),
		SessionPath:  defaultString(firstEnv(spec.SessionPathEnv...), spec.DefaultSessionPath),
		StateDir:     defaultString(firstEnv(spec.StateDirEnv...), spec.DefaultStateDir),
		AllowedChats: splitList(firstEnv(spec.AllowedChatsEnv...)),
		RPCSpacing:   time.Duration(rpcSpacingMS) * time.Millisecond,
		BatchSize:    batchSize,
		HistoryLimit: historyLimit,
		MaxBatches:   maxBatches,
	}, nil
}

func (c Config) WithTelegramDesktopDefaults() Config {
	if c.AppID == 0 {
		c.AppID = telegram.TestAppID
	}
	if strings.TrimSpace(c.AppHash) == "" {
		c.AppHash = telegram.TestAppHash
	}
	return c
}

func DefaultAppIDEnv() []string {
	return []string{"TG_STUDY_APP_ID", "TG_E2E_APP_ID"}
}

func DefaultAppHashEnv() []string {
	return []string{"TG_STUDY_APP_HASH", "TG_E2E_APP_HASH"}
}

func (c Config) ValidateLogin() error {
	if err := c.ValidateRuntime(); err != nil {
		return err
	}
	if strings.TrimSpace(c.Phone) == "" {
		return fmt.Errorf("%s is required for login", c.EnvName("PHONE"))
	}
	return nil
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
	prefix := strings.TrimSpace(c.EnvPrefix)
	if prefix == "" {
		prefix = "TG_STUDY"
	}
	return prefix + "_" + suffix
}

func (c Config) LoginCommand() string {
	if c.Profile == "daily" {
		return "telegram-study-harvest daily-login"
	}
	return "telegram-study-harvest login"
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
