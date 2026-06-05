package harvest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteDailyMarkdownRendersOutgoingTimelineAndTranscripts(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "day.md")
	start := time.Date(2026, 6, 5, 0, 0, 0, 0, moscowLocation)
	records := []MessageRecord{
		{
			Chat:      Chat{ID: 2, Display: "Work"},
			MessageID: 20,
			SourceURL: "https://t.me/c/2/20",
			Date:      start.Add(10 * time.Hour),
			Kind:      "voice",
			Attachments: []Attachment{
				{
					Kind:             "voice",
					MediaID:          "document:123",
					FileName:         "voice.ogg",
					TranscriptPath:   filepath.Join(dir, "voice.txt"),
					TranscriptCached: true,
					Transcript:       "Поговорил про итоги дня",
				},
			},
		},
		{
			Chat:      Chat{ID: 1, Display: "Notes"},
			MessageID: 10,
			Date:      start.Add(9 * time.Hour),
			Kind:      "text",
			Text:      "Запланировал задачу\n\nПроверил формат",
		},
	}
	err := WriteDailyMarkdown(DailyMarkdownOptions{
		OutputPath: output,
		Date:       "2026-06-05",
		Start:      start,
		End:        start.AddDate(0, 0, 1),
		Stats: OutgoingDayStats{
			DialogsScanned: 10,
			Attachments:    1,
			Transcripts:    1,
		},
		Records: records,
	})
	if err != nil {
		t.Fatal(err)
	}
	content := readTestFile(t, output)
	for _, want := range []string{
		"# Telegram-отчет за 2026-06-05",
		"## Сводка",
		"- Период: 2026-06-05 00:00 .. 2026-06-06 00:00",
		"- Исходящих сообщений: 2",
		"- Вложений: 1",
		"- Транскриптов: 1",
		"## Хронология",
		"09:00 в Notes: Запланировал задачу<br><br>Проверил формат `#10`",
		"10:00 в Work: [voice] [`#20`](https://t.me/c/2/20)",
		"файл: voice; media_id=document:123; voice.ogg",
		"transcript_cached=true",
		"транскрипт: Поговорил про итоги дня",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("markdown missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "JSONL:") || strings.Contains(content, "Ошибок: 0") || strings.Contains(content, "Flood waits: 0") {
		t.Fatalf("markdown includes zero summary fields:\n%s", content)
	}
}

func TestDailyDefaultOutputPathsSplitsMarkdownAndJSONL(t *testing.T) {
	stateDir := filepath.Join("/repo", ".state", "daily")
	jsonl, markdown := DailyDefaultOutputPaths(stateDir, "2026-06-05")
	if jsonl != filepath.Join("/repo", ".state", "daily", "jsonl", "2026-06-05.jsonl") {
		t.Fatalf("jsonl path = %s", jsonl)
	}
	if markdown != filepath.Join("/repo", "reports", "daily", "2026-06-05.md") {
		t.Fatalf("markdown path = %s", markdown)
	}
}

func TestDailyDefaultReportRootFallsBackInsideCustomStateDir(t *testing.T) {
	got := DailyDefaultReportRoot(filepath.Join("/custom", "daily-state"))
	if got != filepath.Join("/custom", "daily-state", "reports", "daily") {
		t.Fatalf("report root = %s", got)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}
