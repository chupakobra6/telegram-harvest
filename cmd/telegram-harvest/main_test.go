package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/config"
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
		"daily-catchup",
		"daily-download-media --chat",
		"--profile main|study",
		"required account profile",
		"Telegram operations are read-only",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("help missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "import-tdesktop") {
		t.Fatalf("help must not expose Telegram Desktop import:\n%s", stdout)
	}
}

func TestRunRejectsTelegramDesktopImportCommand(t *testing.T) {
	code, _, stderr := runCommand(t, []string{"import-tdesktop"}, nil)
	if code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "unknown command: import-tdesktop") {
		t.Fatalf("missing unknown command error: %s", stderr)
	}
}

func TestRunRequiresExplicitProfile(t *testing.T) {
	code, _, stderr := runCommand(t, []string{"doctor"}, nil)
	if code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "--profile main|study is required") {
		t.Fatalf("missing profile error: %s", stderr)
	}
}

func TestRunPrintConfigUsesEnvAndRootedPaths(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"TG_HARVEST_STUDY_STATE_DIR":     filepath.Join(dir, "state"),
		"TG_HARVEST_STUDY_SESSION_PATH":  filepath.Join(dir, "sessions", "study.json"),
		"TG_HARVEST_STUDY_ALLOWED_CHATS": "12345,@study",
	}
	code, stdout, stderr := runCommand(t, []string{"print-config", "--profile", "study"}, env)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	for _, want := range []string{
		"profile=study",
		"state_dir=" + filepath.Join(dir, "state"),
		"session=" + filepath.Join(dir, "sessions", "study.json"),
		"allowed_chats=2",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("print-config missing %q:\n%s", want, stdout)
		}
	}
}

func TestRunPrintConfigCanSelectMainProfile(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{
		"TG_HARVEST_DAILY_APP_ID":          "77",
		"TG_HARVEST_DAILY_APP_HASH":        "main-hash",
		"TG_HARVEST_DAILY_STATE_DIR":       filepath.Join(dir, "main-state"),
		"TG_HARVEST_DAILY_SESSION_PATH":    filepath.Join(dir, "main-session.json"),
		"TG_HARVEST_DAILY_VOSK_COMMAND":    "/tmp/vosk-transcribe",
		"TG_HARVEST_DAILY_VOSK_MODEL_PATH": filepath.Join(dir, "vosk-model-small-ru-0.22"),
	}
	code, stdout, stderr := runCommand(t, []string{"print-config", "--profile", "main"}, env)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	for _, want := range []string{
		"profile=main",
		"app_id_set=true",
		"state_dir=" + filepath.Join(dir, "main-state"),
		"session=" + filepath.Join(dir, "main-session.json"),
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("main print-config missing %q:\n%s", want, stdout)
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
		"TG_HARVEST_STUDY_SESSION_PATH": filepath.Join(dir, "sessions", "study.json"),
	}

	code, stdout, stderr := runCommand(t, []string{"--profile", "study", "compact", "--in", "messages.jsonl", "--out", "messages.toon"}, env)
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

	code, stdout, stderr = runCommand(t, []string{"--profile", "study", "agent-view", "--in", "messages.jsonl", "--out-dir", "agent-view", "--recent", "5"}, env)
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

	code, stdout, stderr = runCommand(t, []string{"--profile", "study", "agent-view", "--in", "messages.jsonl", "--out-dir", "agent-view", "--recent", "5"}, env)
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
		"TG_HARVEST_STUDY_SESSION_PATH":  filepath.Join(dir, "sessions", "study.json"),
		"TG_HARVEST_STUDY_ALLOWED_CHATS": "12345",
	}
	for _, args := range [][]string{
		{"--profile", "study", "topics", "--chat", "999"},
		{"--profile", "study", "dump", "--chat", "999", "--out", "x.jsonl"},
		{"--profile", "study", "sync", "--chat", "999", "--name", "x"},
		{"--profile", "study", "download-media", "--chat", "999", "--message-id", "1"},
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

func TestBuildDailyCatchupPlanStartsAfterLatestReportAndSkipsToday(t *testing.T) {
	root := t.TempDir()
	reportDir := filepath.Join(root, "reports", "daily")
	mustWriteCLIFile(t, filepath.Join(reportDir, "2026-06-02.md"), "done")
	mustWriteCLIFile(t, filepath.Join(reportDir, "2026-06-07.md"), "today partial")
	cfg := configForCatchup(root)

	plan, err := buildDailyCatchupPlan(cfg, reportDir, "", time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if plan.LastReport != "2026-06-02" || plan.Today != "2026-06-07" {
		t.Fatalf("unexpected plan labels: %+v", plan)
	}
	got := dailyJobDates(plan.Jobs)
	want := []string{"2026-06-03", "2026-06-04", "2026-06-05", "2026-06-06"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("jobs=%v want=%v", got, want)
	}
	for _, job := range plan.Jobs {
		if job.MarkdownPath != filepath.Join(reportDir, job.Date+".md") {
			t.Fatalf("markdown path for %s = %s", job.Date, job.MarkdownPath)
		}
		if !strings.HasSuffix(job.OutputPath, filepath.Join(".state", "daily", "jsonl", job.Date+".jsonl")) {
			t.Fatalf("jsonl path for %s = %s", job.Date, job.OutputPath)
		}
	}
}

func TestBuildDailyCatchupPlanSkipsExistingReportsFromManualStart(t *testing.T) {
	root := t.TempDir()
	reportDir := filepath.Join(root, "reports", "daily")
	mustWriteCLIFile(t, filepath.Join(reportDir, "2026-06-04.md"), "done")
	cfg := configForCatchup(root)

	plan, err := buildDailyCatchupPlan(cfg, reportDir, "2026-06-03", time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	got := dailyJobDates(plan.Jobs)
	want := []string{"2026-06-03", "2026-06-05", "2026-06-06"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("jobs=%v want=%v", got, want)
	}
	if strings.Join(plan.Skipped, ",") != "2026-06-04" {
		t.Fatalf("skipped=%v", plan.Skipped)
	}
}

func TestBuildDailyCatchupPlanRequiresManualStartWithoutReports(t *testing.T) {
	root := t.TempDir()
	cfg := configForCatchup(root)
	_, err := buildDailyCatchupPlan(cfg, filepath.Join(root, "reports", "daily"), "", time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("expected missing report error")
	}
	if !strings.Contains(err.Error(), "--from YYYY-MM-DD") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateDailyOptionsRejectsInvalidVideoTranscribeMode(t *testing.T) {
	if err := validateDailyOptions(dailyOptions{VideoTranscribeMode: "phone"}); err != nil {
		t.Fatalf("phone mode rejected: %v", err)
	}
	err := validateDailyOptions(dailyOptions{VideoTranscribeMode: "cinema"})
	if err == nil || !strings.Contains(err.Error(), "--transcribe-video") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunDailyCatchupRejectsInvalidVideoTranscribeModeBeforeRuntimeAccess(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "sessions", "main.json")
	env := map[string]string{
		"TG_HARVEST_DAILY_APP_ID":       "77",
		"TG_HARVEST_DAILY_APP_HASH":     "main-hash",
		"TG_HARVEST_DAILY_STATE_DIR":    filepath.Join(dir, "state"),
		"TG_HARVEST_DAILY_SESSION_PATH": sessionPath,
	}

	code, _, stderr := runCommand(t, []string{"--profile", "main", "daily-catchup", "--transcribe-video", "cinema"}, env)
	if code != 1 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "--transcribe-video must be one of: phone, all, off") {
		t.Fatalf("missing video mode validation error: %s", stderr)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(sessionPath), "runtime.lock")); !os.IsNotExist(err) {
		t.Fatalf("daily-catchup should not acquire runtime lock, stat err=%v", err)
	}
}

func TestAtomicOutputPublishReplacesFinalOnlyOnPublish(t *testing.T) {
	finalPath := filepath.Join(t.TempDir(), "daily.jsonl")
	mustWriteCLIFile(t, finalPath, "old\n")

	tempPath, file, err := createAtomicOutput(finalPath)
	if err != nil {
		t.Fatalf("create atomic output: %v", err)
	}
	if _, err := file.WriteString("new\n"); err != nil {
		t.Fatalf("write temp output: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close temp output: %v", err)
	}
	if got := readCLIFile(t, finalPath); got != "old\n" {
		t.Fatalf("final changed before publish: %q", got)
	}
	if err := publishAtomicOutput(tempPath, finalPath); err != nil {
		t.Fatalf("publish atomic output: %v", err)
	}
	if got := readCLIFile(t, finalPath); got != "new\n" {
		t.Fatalf("final after publish = %q", got)
	}
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Fatalf("temp file should be renamed away, stat err=%v", err)
	}
}

func configForCatchup(root string) config.Config {
	return config.Config{
		StateDir: filepath.Join(root, ".state", "daily"),
	}
}

func dailyJobDates(jobs []dailyJob) []string {
	dates := make([]string, 0, len(jobs))
	for _, job := range jobs {
		dates = append(dates, job.Date)
	}
	return dates
}

func runCommand(t *testing.T, args []string, env map[string]string) (int, string, string) {
	t.Helper()
	baseDir := t.TempDir()
	clearCommandEnv(t)
	t.Setenv("TG_HARVEST_STUDY_APP_ID", "0")
	t.Setenv("TG_HARVEST_STUDY_APP_HASH", "test-hash")
	t.Setenv("TG_HARVEST_STUDY_STATE_DIR", filepath.Join(baseDir, "state"))
	t.Setenv("TG_HARVEST_STUDY_SESSION_PATH", filepath.Join(baseDir, "sessions", "study.json"))
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
		"TRANSCRIBE_CMD",
		"VOSK_COMMAND",
		"VOSK_MODEL_PATH",
		"VOSK_GRAMMAR_PATH",
		"VOSK_LIBRARY_PATH",
		"FFMPEG_COMMAND",
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
