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
	DefaultSessionPath  = ".sessions/user.json"
	DefaultStateDir     = ".state"
	DefaultRPCSpacingMS = 1500
	DefaultBatchSize    = 80
	DefaultHistoryLimit = 500
	DefaultMaxBatches   = 20
)

type Config struct {
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
	appID, err := intFromEnvAny(DefaultAppIDEnv(), 0)
	if err != nil {
		return Config{}, err
	}
	rpcSpacingMS, err := intFromEnvAny([]string{"TG_STUDY_RPC_SPACING_MS"}, DefaultRPCSpacingMS)
	if err != nil {
		return Config{}, err
	}
	batchSize, err := intFromEnvAny([]string{"TG_STUDY_HISTORY_BATCH_SIZE"}, DefaultBatchSize)
	if err != nil {
		return Config{}, err
	}
	historyLimit, err := intFromEnvAny([]string{"TG_STUDY_HISTORY_LIMIT"}, DefaultHistoryLimit)
	if err != nil {
		return Config{}, err
	}
	maxBatches, err := intFromEnvAny([]string{"TG_STUDY_MAX_BATCHES"}, DefaultMaxBatches)
	if err != nil {
		return Config{}, err
	}

	return Config{
		AppID:        appID,
		AppHash:      firstEnv(DefaultAppHashEnv()...),
		Phone:        firstEnv("TG_STUDY_PHONE", "TG_E2E_PHONE"),
		Password:     firstEnv("TG_STUDY_PASSWORD", "TG_E2E_PASSWORD"),
		SessionPath:  defaultString(firstEnv("TG_STUDY_SESSION_PATH"), DefaultSessionPath),
		StateDir:     defaultString(firstEnv("TG_STUDY_STATE_DIR"), DefaultStateDir),
		AllowedChats: splitList(firstEnv("TG_STUDY_ALLOWED_CHATS")),
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
		return fmt.Errorf("TG_STUDY_PHONE is required for login")
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
		return fmt.Errorf("TG_STUDY_APP_ID is required")
	}
	if strings.TrimSpace(c.AppHash) == "" {
		return fmt.Errorf("TG_STUDY_APP_HASH is required")
	}
	if strings.TrimSpace(c.SessionPath) == "" {
		return fmt.Errorf("TG_STUDY_SESSION_PATH is required")
	}
	if strings.TrimSpace(c.StateDir) == "" {
		return fmt.Errorf("TG_STUDY_STATE_DIR is required")
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
