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
			Text:      "Запланировал задачу",
		},
	}
	err := WriteDailyMarkdown(DailyMarkdownOptions{
		OutputPath: output,
		SourcePath: filepath.Join(dir, "day.jsonl"),
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
		"# Telegram Daily Harvest: 2026-06-05",
		"09:00 to Notes: Запланировал задачу `#10`",
		"10:00 to Work: [voice] [`#20`](https://t.me/c/2/20)",
		"file: voice; media_id=document:123; voice.ogg",
		"transcript_cached=true",
		"transcript: Поговорил про итоги дня",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("markdown missing %q:\n%s", want, content)
		}
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
