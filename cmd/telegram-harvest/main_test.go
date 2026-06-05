package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunHelpPrintsCommands(t *testing.T) {
	code, stdout, stderr := runCommand(t, []string{"help"}, nil)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	for _, want := range []string{
		"agent-view",
		"compact",
		"download-media --chat",
		"daily-download-media --chat",
		"Telegram operations are read-only",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("help missing %q:\n%s", want, stdout)
		}
	}
}

func TestDailyCommandRouting(t *testing.T) {
	if !isDailyCommand("daily-download-media") {
		t.Fatalf("daily-download-media should use daily config")
	}
	if isDailyCommand("download-media") {
		t.Fatalf("download-media should use study config")
	}
}

func TestRunPrintConfigUsesEnvAndRootedPaths(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"TG_HARVEST_STUDY_STATE_DIR":     filepath.Join(dir, "state"),
		"TG_HARVEST_STUDY_SESSION_PATH":  filepath.Join(dir, "sessions", "user.json"),
		"TG_HARVEST_STUDY_ALLOWED_CHATS": "12345,@study",
		"TG_HARVEST_STUDY_HISTORY_LIMIT": "25",
	}
	code, stdout, stderr := runCommand(t, []string{"print-config"}, env)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	for _, want := range []string{
		"state_dir=" + filepath.Join(dir, "state"),
		"session=" + filepath.Join(dir, "sessions", "user.json"),
		"allowed_chats=2",
		"history_limit=25",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("print-config missing %q:\n%s", want, stdout)
		}
	}
}

func TestRunDailyConfigUsesHarvestMode(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"TG_HARVEST_APP_ID":          "77",
		"TG_HARVEST_APP_HASH":        "daily-hash",
		"TG_HARVEST_STATE_DIR":       filepath.Join(dir, "daily-state"),
		"TG_HARVEST_SESSION_PATH":    filepath.Join(dir, "daily-session.json"),
		"TG_HARVEST_VOSK_COMMAND":    "/tmp/vosk-transcribe",
		"TG_HARVEST_VOSK_MODEL_PATH": filepath.Join(dir, "vosk-model-small-ru-0.22"),
		"TG_HARVEST_RETENTION_DAYS":  "10",
	}
	code, stdout, stderr := runCommand(t, []string{"daily-config"}, env)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	for _, want := range []string{
		"mode=daily",
		"app_id_set=true",
		"state_dir=" + filepath.Join(dir, "daily-state"),
		"session=" + filepath.Join(dir, "daily-session.json"),
		"daily_transcribe_default=true",
		"daily_vosk_command=/tmp/vosk-transcribe",
		"daily_vosk_model_path=" + filepath.Join(dir, "vosk-model-small-ru-0.22"),
		"daily_retention_days=10",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("daily-config missing %q:\n%s", want, stdout)
		}
	}
}

func TestMaskCLIPhone(t *testing.T) {
	if got := maskCLIPhone("10000000017"); got != "+1********17" {
		t.Fatalf("masked phone = %s", got)
	}
	if got := maskCLIPhone("+1234"); got != "+1234" {
		t.Fatalf("short masked phone = %s", got)
	}
}

func TestRunCompactAndAgentViewUseStateDirRelativePaths(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	input := filepath.Join(stateDir, "messages.jsonl")
	mustWriteCLIFile(t, input, strings.Join([]string{
		`{"source":"telegram","chat":{"id":123,"display":"Study"},"message_id":1,"date":"2026-05-10T10:00:00Z","sender":{"display":"Student"},"kind":"text","text":"first"}`,
		`{"source":"telegram","chat":{"id":123,"display":"Study"},"message_id":2,"date":"2026-05-10T11:00:00Z","sender":{"display":"Teacher"},"kind":"text","text":"second"}`,
	}, "\n")+"\n")
	env := map[string]string{
		"TG_HARVEST_STUDY_STATE_DIR":    stateDir,
		"TG_HARVEST_STUDY_SESSION_PATH": filepath.Join(dir, "sessions", "user.json"),
	}

	code, stdout, stderr := runCommand(t, []string{"compact", "--in", "messages.jsonl", "--out", "messages.toon"}, env)
	if code != 0 {
		t.Fatalf("compact code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "wrote=2") {
		t.Fatalf("unexpected compact stdout:\n%s", stdout)
	}
	toon := readCLIFile(t, filepath.Join(stateDir, "messages.toon"))
	if !strings.Contains(toon, "messages[2|]") || !strings.Contains(toon, "Teacher|second") {
		t.Fatalf("compact output missing records:\n%s", toon)
	}

	code, stdout, stderr = runCommand(t, []string{"agent-view", "--in", "messages.jsonl", "--out-dir", "agent-view", "--recent", "5"}, env)
	if code != 0 {
		t.Fatalf("agent-view code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "mode=rebuild") || !strings.Contains(stdout, "visible_added=2") {
		t.Fatalf("unexpected agent-view stdout:\n%s", stdout)
	}
	index := readCLIFile(t, filepath.Join(stateDir, "agent-view", "README.md"))
	if !strings.Contains(index, "Study") || !strings.Contains(index, "Total visible messages: `2`") {
		t.Fatalf("agent-view index missing summary:\n%s", index)
	}

	code, stdout, stderr = runCommand(t, []string{"agent-view", "--in", "messages.jsonl", "--out-dir", "agent-view", "--recent", "5"}, env)
	if code != 0 {
		t.Fatalf("agent-view noop code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "mode=noop") || !strings.Contains(stdout, "visible_added=0") {
		t.Fatalf("expected noop stdout:\n%s", stdout)
	}
}

func TestRunReadCommandsRefuseChatsOutsideAllowlistBeforeRuntimeAccess(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"TG_HARVEST_STUDY_STATE_DIR":     filepath.Join(dir, "state"),
		"TG_HARVEST_STUDY_SESSION_PATH":  filepath.Join(dir, "sessions", "user.json"),
		"TG_HARVEST_STUDY_ALLOWED_CHATS": "12345",
	}
	for _, args := range [][]string{
		{"topics", "--chat", "999"},
		{"dump", "--chat", "999", "--out", "x.jsonl"},
		{"sync", "--chat", "999", "--name", "x"},
		{"download-media", "--chat", "999", "--message-id", "1"},
	} {
		code, _, stderr := runCommand(t, args, env)
		if code != 1 {
			t.Fatalf("%v code=%d stderr=%s", args, code, stderr)
		}
		if !strings.Contains(stderr, "outside TG_HARVEST_STUDY_ALLOWED_CHATS") {
			t.Fatalf("%v missing allowlist error: %s", args, stderr)
		}
		if _, err := os.Stat(filepath.Join(dir, "sessions", "runtime.lock")); !os.IsNotExist(err) {
			t.Fatalf("%v should not acquire runtime lock, stat err=%v", args, err)
		}
	}
}

func TestParseCompactSinceUsesMoscowForDateOnly(t *testing.T) {
	got, err := parseCompactSince("2026-05-14")
	if err != nil {
		t.Fatalf("parse since: %v", err)
	}
	if got.Location().String() != "Europe/Moscow" {
		t.Fatalf("expected Europe/Moscow location, got %s", got.Location())
	}
	if got.Format("2006-01-02T15:04:05-07:00") != "2026-05-14T00:00:00+03:00" {
		t.Fatalf("unexpected parsed value: %s", got.Format("2006-01-02T15:04:05-07:00"))
	}
	if _, err := parseCompactSince("14-05-2026"); err == nil {
		t.Fatalf("expected invalid date error")
	}
}

func TestParseDailyDateSupportsRelativeDays(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	date, start, end, err := parseDailyDate("yesterday", now)
	if err != nil {
		t.Fatalf("parse daily date: %v", err)
	}
	if date != "2026-06-04" {
		t.Fatalf("date=%s", date)
	}
	if start.Format("2006-01-02T15:04:05-07:00") != "2026-06-04T00:00:00+03:00" {
		t.Fatalf("start=%s", start.Format("2006-01-02T15:04:05-07:00"))
	}
	if end.Sub(start) != 24*time.Hour {
		t.Fatalf("end-start=%s", end.Sub(start))
	}
}

func runCommand(t *testing.T, args []string, env map[string]string) (int, string, string) {
	t.Helper()
	baseDir := t.TempDir()
	clearCommandEnv(t)
	t.Setenv("TG_HARVEST_STUDY_APP_ID", "0")
	t.Setenv("TG_HARVEST_STUDY_APP_HASH", "test-hash")
	t.Setenv("TG_HARVEST_STUDY_STATE_DIR", filepath.Join(baseDir, "state"))
	t.Setenv("TG_HARVEST_STUDY_SESSION_PATH", filepath.Join(baseDir, "sessions", "user.json"))
	for key, value := range env {
		t.Setenv(key, value)
	}

	stdout := tempFile(t, "stdout")
	defer os.Remove(stdout.Name())
	stderr := tempFile(t, "stderr")
	defer os.Remove(stderr.Name())
	stdin := tempFile(t, "stdin")
	defer os.Remove(stdin.Name())

	code := run(args, stdin, stdout, stderr)
	return code, readTempFile(t, stdout), readTempFile(t, stderr)
}

func clearCommandEnv(t *testing.T) {
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
		"DIALOG_LIMIT",
		"TRANSCRIBE_CMD",
		"VOSK_COMMAND",
		"VOSK_MODEL_PATH",
		"VOSK_GRAMMAR_PATH",
		"FFMPEG_COMMAND",
		"RETENTION_DAYS",
	}
	for _, prefix := range prefixes {
		for _, suffix := range suffixes {
			t.Setenv(prefix+suffix, "")
		}
	}
}

func tempFile(t *testing.T, name string) *os.File {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), name)
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	return file
}

func readTempFile(t *testing.T, file *os.File) string {
	t.Helper()
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatalf("seek %s: %v", file.Name(), err)
	}
	content, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatalf("read %s: %v", file.Name(), err)
	}
	return string(content)
}

func mustWriteCLIFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readCLIFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}
