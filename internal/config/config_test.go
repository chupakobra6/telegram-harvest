package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadUsesStudyEnv(t *testing.T) {
	clearTelegramConfigEnv(t)
	t.Setenv("TG_HARVEST_STUDY_APP_ID", "42")
	t.Setenv("TG_HARVEST_STUDY_APP_HASH", "hash")
	t.Setenv("TG_HARVEST_STUDY_PHONE", "+100")
	t.Setenv("TG_HARVEST_STUDY_RPC_SPACING_MS", "2500")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AppID != 42 {
		t.Fatalf("AppID = %d", cfg.AppID)
	}
	if cfg.AppHash != "hash" {
		t.Fatalf("AppHash = %q", cfg.AppHash)
	}
	if cfg.Phone != "+100" {
		t.Fatalf("Phone = %q", cfg.Phone)
	}
	if cfg.RPCSpacing != 2500*time.Millisecond {
		t.Fatalf("RPCSpacing = %s", cfg.RPCSpacing)
	}
}

func TestLoadMainUsesHarvestEnvAndDefaults(t *testing.T) {
	clearTelegramConfigEnv(t)
	t.Setenv("TG_HARVEST_STUDY_APP_ID", "42")
	t.Setenv("TG_HARVEST_STUDY_APP_HASH", "study-hash")
	t.Setenv("TG_HARVEST_APP_ID", "77")
	t.Setenv("TG_HARVEST_APP_HASH", "main-hash")
	t.Setenv("TG_HARVEST_PHONE", "+200")

	cfg, err := LoadMain()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeMain {
		t.Fatalf("mode = %q", cfg.Mode)
	}
	if cfg.AppID != 77 || cfg.AppHash != "main-hash" || cfg.Phone != "+200" {
		t.Fatalf("main env not loaded: %+v", cfg)
	}
	if cfg.SessionPath != DefaultMainSessionPath {
		t.Fatalf("main session path = %s", cfg.SessionPath)
	}
	if cfg.StateDir != DefaultMainStateDir {
		t.Fatalf("main state dir = %s", cfg.StateDir)
	}
}

func TestLoadProfileSelectsMainOrStudyEnv(t *testing.T) {
	clearTelegramConfigEnv(t)
	t.Setenv("TG_HARVEST_STUDY_APP_ID", "42")
	t.Setenv("TG_HARVEST_STUDY_APP_HASH", "study-hash")
	t.Setenv("TG_HARVEST_APP_ID", "77")
	t.Setenv("TG_HARVEST_APP_HASH", "main-hash")

	study, err := LoadProfile("study")
	if err != nil {
		t.Fatal(err)
	}
	if study.Mode != ModeStudy || study.AppID != 42 || study.AppHash != "study-hash" {
		t.Fatalf("study profile = %+v", study)
	}
	main, err := LoadProfile("main")
	if err != nil {
		t.Fatal(err)
	}
	if main.Mode != ModeMain || main.AppID != 77 || main.AppHash != "main-hash" {
		t.Fatalf("main profile = %+v", main)
	}
	if _, err := LoadProfile("daily"); err == nil {
		t.Fatalf("expected daily profile to be rejected")
	}
	if _, err := LoadProfile("unknown"); err == nil {
		t.Fatalf("expected unknown profile error")
	}
}

func TestLoadDotEnvDoesNotOverrideExistingEnv(t *testing.T) {
	t.Setenv("KEY", "existing")
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("KEY=from_file\nOTHER=\"two words\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("KEY"); got != "existing" {
		t.Fatalf("KEY = %q", got)
	}
	if got := os.Getenv("OTHER"); got != "two words" {
		t.Fatalf("OTHER = %q", got)
	}
}

func TestLoadAllowedChats(t *testing.T) {
	clearTelegramConfigEnv(t)
	t.Setenv("TG_HARVEST_STUDY_ALLOWED_CHATS", "1234567890, @study_chat 1234567890")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.AllowedChatCount(); got != 2 {
		t.Fatalf("AllowedChatCount = %d", got)
	}
	if !cfg.ChatAllowed("1234567890") {
		t.Fatalf("expected numeric chat to be allowed")
	}
	if !cfg.ChatAllowed("study_chat") {
		t.Fatalf("expected username without @ to be allowed")
	}
	if cfg.ChatAllowed("other_chat") {
		t.Fatalf("unexpected chat allowed")
	}
}

func TestLoadRejectsInvalidIntegerEnv(t *testing.T) {
	clearTelegramConfigEnv(t)
	t.Setenv("TG_HARVEST_STUDY_HISTORY_BATCH_SIZE", "many")
	_, err := Load()
	if err == nil {
		t.Fatalf("expected invalid integer error")
	}
}

func TestValidateRuntimeChecksRequiredAndBounds(t *testing.T) {
	valid := Config{
		AppID:        1,
		AppHash:      "hash",
		SessionPath:  ".sessions/study.json",
		StateDir:     ".state",
		RPCSpacing:   time.Second,
		BatchSize:    80,
		HistoryLimit: 500,
		MaxBatches:   20,
	}
	if err := valid.ValidateRuntime(); err != nil {
		t.Fatalf("valid runtime rejected: %v", err)
	}
	cases := []struct {
		name string
		edit func(*Config)
	}{
		{"missing app id", func(c *Config) { c.AppID = 0 }},
		{"missing app hash", func(c *Config) { c.AppHash = "" }},
		{"missing session", func(c *Config) { c.SessionPath = "" }},
		{"missing state dir", func(c *Config) { c.StateDir = "" }},
		{"bad spacing", func(c *Config) { c.RPCSpacing = 0 }},
		{"bad batch low", func(c *Config) { c.BatchSize = 0 }},
		{"bad batch high", func(c *Config) { c.BatchSize = 101 }},
		{"bad history limit", func(c *Config) { c.HistoryLimit = 0 }},
		{"bad max batches", func(c *Config) { c.MaxBatches = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid
			tc.edit(&cfg)
			if err := cfg.ValidateRuntime(); err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
}

func TestValidateLoginAllowsInteractivePhone(t *testing.T) {
	cfg := Config{
		AppID:        1,
		AppHash:      "hash",
		SessionPath:  ".sessions/study.json",
		StateDir:     ".state",
		RPCSpacing:   time.Second,
		BatchSize:    80,
		HistoryLimit: 500,
		MaxBatches:   20,
	}
	if err := cfg.ValidateLogin(); err != nil {
		t.Fatalf("login config without phone rejected: %v", err)
	}
}

func TestWithRootAndRuntimeLockPath(t *testing.T) {
	root := t.TempDir()
	cfg := Config{
		SessionPath: ".sessions/study.json",
		StateDir:    ".state",
	}.WithRoot(root)
	if cfg.SessionPath != filepath.Join(root, ".sessions", "study.json") {
		t.Fatalf("unexpected session path: %s", cfg.SessionPath)
	}
	if cfg.StateDir != filepath.Join(root, ".state") {
		t.Fatalf("unexpected state dir: %s", cfg.StateDir)
	}
	if got := cfg.RuntimeLockPath(); got != filepath.Join(root, ".sessions", "runtime.lock") {
		t.Fatalf("runtime lock = %s", got)
	}
}

func TestLoadDotEnvReportsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("BROKEN\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadDotEnv(path); err == nil {
		t.Fatalf("expected malformed dotenv error")
	}
}

func clearTelegramConfigEnv(t *testing.T) {
	t.Helper()
	prefixes := []string{
		"TG_HARVEST_",
		"TG_HARVEST_STUDY_",
	}
	suffixes := []string{
		"APP_ID",
		"APP_HASH",
		"PHONE",
		"PASSWORD",
		"SESSION_PATH",
		"STATE_DIR",
		"ALLOWED_CHATS",
		"RPC_SPACING_MS",
		"HISTORY_BATCH_SIZE",
		"HISTORY_LIMIT",
		"MAX_BATCHES",
		"PROFILE",
	}
	for _, prefix := range prefixes {
		for _, suffix := range suffixes {
			t.Setenv(prefix+suffix, "")
		}
	}
}
