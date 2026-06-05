package harvest

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const dailyTranscriptPreviewRunes = 4000

type DailyMarkdownOptions struct {
	OutputPath string
	SourcePath string
	Date       string
	Start      time.Time
	End        time.Time
	Stats      OutgoingDayStats
	Records    []MessageRecord
}

func WriteDailyMarkdown(opts DailyMarkdownOptions) error {
	if strings.TrimSpace(opts.OutputPath) == "" {
		return fmt.Errorf("output path is required")
	}
	records := append([]MessageRecord(nil), opts.Records...)
	sortRecordsOldestFirst(records)
	return writeTextFile(opts.OutputPath, renderDailyMarkdown(opts, records))
}

func renderDailyMarkdown(opts DailyMarkdownOptions, records []MessageRecord) string {
	dateLabel := opts.Date
	if dateLabel == "" && !opts.Start.IsZero() {
		dateLabel = opts.Start.In(moscowLocation).Format("2006-01-02")
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# Telegram Daily Harvest: %s\n\n", dateLabel))
	b.WriteString("## Summary\n\n")
	if !opts.Start.IsZero() && !opts.End.IsZero() {
		b.WriteString(fmt.Sprintf("- Period: `%s` .. `%s`\n", opts.Start.In(moscowLocation).Format(time.RFC3339), opts.End.In(moscowLocation).Format(time.RFC3339)))
	}
	if strings.TrimSpace(opts.SourcePath) != "" {
		b.WriteString(fmt.Sprintf("- Raw JSONL: `%s`\n", opts.SourcePath))
	}
	b.WriteString(fmt.Sprintf("- Outgoing messages: `%d`\n", len(records)))
	b.WriteString(fmt.Sprintf("- Chats with messages: `%d`\n", dailyChatCount(records)))
	b.WriteString(fmt.Sprintf("- Attachments: `%d`\n", opts.Stats.Attachments))
	b.WriteString(fmt.Sprintf("- Transcripts: `%d`\n", opts.Stats.Transcripts))
	b.WriteString(fmt.Sprintf("- Dialogs scanned: `%d`; skipped by date: `%d`; errors: `%d`\n", opts.Stats.DialogsScanned, opts.Stats.DialogsSkipped, len(opts.Stats.DialogErrors)))
	if opts.Stats.FloodWaits > 0 {
		b.WriteString(fmt.Sprintf("- Flood waits: `%d`\n", opts.Stats.FloodWaits))
	}
	if len(opts.Stats.DialogErrors) > 0 {
		b.WriteString("\n## Dialog Errors\n\n")
		for _, err := range opts.Stats.DialogErrors {
			b.WriteString("- " + err + "\n")
		}
	}
	b.WriteString("\n## Timeline\n\n")
	if len(records) == 0 {
		b.WriteString("No outgoing messages found for this day.\n")
		return b.String()
	}
	lastHour := ""
	for _, record := range records {
		hour := record.Date.In(moscowLocation).Format("15:00")
		if hour != lastHour {
			b.WriteString(fmt.Sprintf("### %s\n\n", hour))
			lastHour = hour
		}
		b.WriteString(dailyMessageLine(record))
		b.WriteString("\n")
		for _, attachment := range record.Attachments {
			b.WriteString("  - " + dailyAttachmentLine(attachment) + "\n")
			if transcript := compactTranscript(attachment.Transcript); transcript != "" {
				b.WriteString("  - transcript: " + transcript + "\n")
			}
		}
	}
	return b.String()
}

func dailyMessageLine(record MessageRecord) string {
	timeLabel := record.Date.In(moscowLocation).Format("15:04")
	destination := displayChat(record.Chat)
	if topic := displayTopic(record); topic != "" {
		destination += " / " + topic
	}
	text := compactText(record.Text)
	if text == "" {
		text = "[" + record.Kind + "]"
	}
	if strings.TrimSpace(record.SourceURL) != "" {
		return fmt.Sprintf("- %s to %s: %s [`#%d`](%s)", timeLabel, destination, text, record.MessageID, record.SourceURL)
	}
	return fmt.Sprintf("- %s to %s: %s `#%d`", timeLabel, destination, text, record.MessageID)
}

func dailyAttachmentLine(attachment Attachment) string {
	parts := []string{"file: " + attachment.Kind}
	if attachment.MediaID != "" {
		parts = append(parts, "media_id="+attachment.MediaID)
	}
	if attachment.FileName != "" {
		parts = append(parts, attachment.FileName)
	}
	if attachment.MIMEType != "" {
		parts = append(parts, attachment.MIMEType)
	}
	if attachment.Size > 0 {
		parts = append(parts, strconv.FormatInt(attachment.Size, 10)+" bytes")
	}
	if attachment.LocalPath != "" {
		parts = append(parts, "local_path="+attachment.LocalPath)
	}
	if attachment.TranscriptPath != "" {
		parts = append(parts, "transcript_path="+attachment.TranscriptPath)
	}
	if attachment.TranscriptCached {
		parts = append(parts, "transcript_cached=true")
	}
	if attachment.DownloadError != "" {
		parts = append(parts, "download_error="+attachment.DownloadError)
	}
	if attachment.TranscriptError != "" {
		parts = append(parts, "transcript_error="+attachment.TranscriptError)
	}
	return strings.Join(parts, "; ")
}

func dailyChatCount(records []MessageRecord) int {
	seen := map[int64]struct{}{}
	for _, record := range records {
		seen[record.Chat.ID] = struct{}{}
	}
	return len(seen)
}

func compactTranscript(value string) string {
	value = compactText(value)
	if value == "" {
		return ""
	}
	if utf8.RuneCountInString(value) <= dailyTranscriptPreviewRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:dailyTranscriptPreviewRunes]) + " ... [truncated, see transcript_path]"
}

func DailyDefaultOutputPaths(stateDir string, date string) (string, string) {
	base := filepath.Join(stateDir, "days")
	return filepath.Join(base, date+".jsonl"), filepath.Join(base, date+".md")
}
