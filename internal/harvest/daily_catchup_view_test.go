package harvest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteDailyCatchupMarkdownMergesDaysInChronologicalOrder(t *testing.T) {
	dir := t.TempDir()
	reportDir := filepath.Join(dir, "daily")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for date, content := range map[string]string{
		"2026-07-22": "# Telegram-отчет за 2026-07-22\n\n## Сводка\n\n- Сообщений: 2\n\n## Хронология\n\n### 09:00\n\nПервый день.\n",
		"2026-07-23": "# Telegram-отчет за 2026-07-23\n\n## Сводка\n\n- Сообщений: 3\n\n## Хронология\n\n### 10:00\n\nВторой день.\n",
	} {
		if err := os.WriteFile(filepath.Join(reportDir, date+".md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	output := filepath.Join(reportDir, DailyLatestCatchupFilename)
	if err := WriteDailyCatchupMarkdown(DailyCatchupMarkdownOptions{
		OutputPath: output,
		ReportDir:  reportDir,
		Dates:      []string{"2026-07-23", "2026-07-22", "2026-07-23"},
	}); err != nil {
		t.Fatal(err)
	}
	content := readTestFile(t, output)
	for _, want := range []string{
		"# Telegram catch-up: 2026-07-22 — 2026-07-23",
		"Дней: 2.",
		"## 2026-07-22",
		"### Сводка",
		"#### 09:00",
		"## 2026-07-23",
		"#### 10:00",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("merged Markdown missing %q:\n%s", want, content)
		}
	}
	if strings.Index(content, "Первый день.") > strings.Index(content, "Второй день.") {
		t.Fatalf("days are not chronological:\n%s", content)
	}
	if strings.Contains(content, "# Telegram-отчет за") {
		t.Fatalf("daily top-level headings were not removed:\n%s", content)
	}
}

func TestWriteDailyCatchupMarkdownFailsWhenDailyReportIsMissing(t *testing.T) {
	dir := t.TempDir()
	err := WriteDailyCatchupMarkdown(DailyCatchupMarkdownOptions{
		OutputPath: filepath.Join(dir, DailyLatestCatchupFilename),
		ReportDir:  dir,
		Dates:      []string{"2026-07-22"},
	})
	if err == nil || !strings.Contains(err.Error(), "2026-07-22") {
		t.Fatalf("unexpected error: %v", err)
	}
}
