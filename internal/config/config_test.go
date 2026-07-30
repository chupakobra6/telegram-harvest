package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadUsesStudyEnv(t *testing.T) {
	clearTelegramConfigEnv(t)
	t.Setenv("TG_HARVEST_STUDY_APP_ID", "42")
	t.Setenv("TG_HARVEST_STUDY_APP_HASH", "hash")
	t.Setenv("TG_HARVEST_STUDY_PHONE", "+100")

	cfg, err := LoadStudy()
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
	if cfg.RPCSpacing != time.Duration(DefaultRPCSpacingMS)*time.Millisecond {
		t.Fatalf("RPCSpacing = %s", cfg.RPCSpacing)
	}
}

func TestLoadIgnoresPacingAndHistoryEnv(t *testing.T) {
	clearTelegramConfigEnv(t)
	t.Setenv("TG_HARVEST_STUDY_APP_ID", "42")
	t.Setenv("TG_HARVEST_STUDY_APP_HASH", "hash")
	t.Setenv("TG_HARVEST_STUDY_RPC_SPACING_MS", "2500")
	t.Setenv("TG_HARVEST_STUDY_HISTORY_BATCH_SIZE", "many")
	t.Setenv("TG_HARVEST_STUDY_HISTORY_LIMIT", "many")
	t.Setenv("TG_HARVEST_STUDY_MAX_BATCHES", "many")

	cfg, err := LoadStudy()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RPCSpacing != time.Duration(DefaultRPCSpacingMS)*time.Millisecond {
		t.Fatalf("RPCSpacing = %s", cfg.RPCSpacing)
	}
}

func TestLoadMainUsesDailyEnvAndDefaults(t *testing.T) {
	clearTelegramConfigEnv(t)
	t.Setenv("TG_HARVEST_STUDY_APP_ID", "42")
	t.Setenv("TG_HARVEST_STUDY_APP_HASH", "study-hash")
	t.Setenv("TG_HARVEST_APP_ID", "999")
	t.Setenv("TG_HARVEST_APP_HASH", "legacy-hash")
	t.Setenv("TG_HARVEST_DAILY_APP_ID", "77")
	t.Setenv("TG_HARVEST_DAILY_APP_HASH", "main-hash")
	t.Setenv("TG_HARVEST_DAILY_PHONE", "+200")

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

func TestLoadMainParsesDailyAdditionalSenders(t *testing.T) {
	clearTelegramConfigEnv(t)
	t.Setenv("TG_HARVEST_DAILY_ADDITIONAL_SENDERS", "3740223926:8718303786, 100:200, 3740223926:8718303786")

	cfg, err := LoadMain()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DailyAdditionalSenderCount() != 2 {
		t.Fatalf("additional senders = %#v", cfg.DailyAdditionalSenders)
	}
	if got := cfg.DailyAdditionalSenders[0]; got.ChatID != 3740223926 || got.SenderID != 8718303786 {
		t.Fatalf("first additional sender = %#v", got)
	}
}

func TestLoadMainRejectsInvalidDailyAdditionalSender(t *testing.T) {
	clearTelegramConfigEnv(t)
	t.Setenv("TG_HARVEST_DAILY_ADDITIONAL_SENDERS", "3740223926")

	_, err := LoadMain()
	if err == nil || !strings.Contains(err.Error(), "TG_HARVEST_DAILY_ADDITIONAL_SENDERS") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadMainIgnoresUnscopedHarvestEnv(t *testing.T) {
	clearTelegramConfigEnv(t)
	t.Setenv("TG_HARVEST_APP_ID", "77")
	t.Setenv("TG_HARVEST_APP_HASH", "legacy-hash")

	cfg, err := LoadMain()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AppID != 0 || cfg.AppHash != "" {
		t.Fatalf("legacy env leaked into daily profile: %+v", cfg)
	}
	if err := cfg.ValidateRuntime(); err == nil {
		t.Fatalf("expected missing TG_HARVEST_DAILY_* validation error")
	}
}

func TestLoadProfileSelectsMainOrStudyEnv(t *testing.T) {
	clearTelegramConfigEnv(t)
	t.Setenv("TG_HARVEST_STUDY_APP_ID", "42")
	t.Setenv("TG_HARVEST_STUDY_APP_HASH", "study-hash")
	t.Setenv("TG_HARVEST_DAILY_APP_ID", "77")
	t.Setenv("TG_HARVEST_DAILY_APP_HASH", "main-hash")

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
	if _, err := LoadProfile(""); err == nil {
		t.Fatalf("expected empty profile to be rejected")
	}
	if _, err := LoadProfile("unknown"); err == nil {
		t.Fatalf("expected unknown profile error")
	}
}

func TestLoadRequiresExplicitProfile(t *testing.T) {
	if _, err := Load(); err == nil {
		t.Fatalf("expected Load to reject implicit profile")
	}
}

func TestLoginCommandAlwaysIncludesProfile(t *testing.T) {
	main := Config{Mode: ModeMain}
	if got := main.LoginCommand(); got != "telegram-harvest --profile main login" {
		t.Fatalf("main login command = %q", got)
	}
	study := Config{Mode: ModeStudy}
	if got := study.LoginCommand(); got != "telegram-harvest --profile study login" {
		t.Fatalf("study login command = %q", got)
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

	cfg, err := LoadStudy()
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
	t.Setenv("TG_HARVEST_STUDY_APP_ID", "many")
	_, err := LoadStudy()
	if err == nil {
		t.Fatalf("expected invalid integer error")
	}
}

func TestValidateRuntimeChecksRequiredAndBounds(t *testing.T) {
	valid := Config{
		AppID:       1,
		AppHash:     "hash",
		SessionPath: ".sessions/study.json",
		StateDir:    ".state",
		RPCSpacing:  time.Second,
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
		AppID:       1,
		AppHash:     "hash",
		SessionPath: ".sessions/study.json",
		StateDir:    ".state",
		RPCSpacing:  time.Second,
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
	if got := cfg.RuntimeLockPath(); got != filepath.Join(root, ".sessions", "study.json.runtime.lock") {
		t.Fatalf("runtime lock = %s", got)
	}
}

func TestRuntimeLockPathIsPerSessionFile(t *testing.T) {
	dir := t.TempDir()
	study := Config{SessionPath: filepath.Join(dir, "sessions", "study.json")}
	main := Config{SessionPath: filepath.Join(dir, "sessions", "main.json")}

	if study.RuntimeLockPath() == main.RuntimeLockPath() {
		t.Fatalf("runtime locks should differ: %s", study.RuntimeLockPath())
	}
	if got := study.RuntimeLockPath(); got != filepath.Join(dir, "sessions", "study.json.runtime.lock") {
		t.Fatalf("study runtime lock = %s", got)
	}
	if got := main.RuntimeLockPath(); got != filepath.Join(dir, "sessions", "main.json.runtime.lock") {
		t.Fatalf("main runtime lock = %s", got)
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
		"TG_HARVEST_DAILY_",
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
		"ADDITIONAL_SENDERS",
	}
	for _, prefix := range prefixes {
		for _, suffix := range suffixes {
			t.Setenv(prefix+suffix, "")
		}
	}
}
