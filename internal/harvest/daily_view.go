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
	b.WriteString(fmt.Sprintf("# Telegram-отчет за %s\n\n", dateLabel))
	b.WriteString("## Сводка\n\n")
	if !opts.Start.IsZero() && !opts.End.IsZero() {
		b.WriteString(fmt.Sprintf("- Период: %s .. %s\n", formatDailySummaryTime(opts.Start), formatDailySummaryTime(opts.End)))
	}
	if strings.TrimSpace(opts.SourcePath) != "" {
		b.WriteString(fmt.Sprintf("- JSONL: %s\n", opts.SourcePath))
	}
	writeDailySummaryCount(&b, "Исходящих сообщений", len(records))
	writeDailySummaryCount(&b, "Чатов с сообщениями", dailyChatCount(records))
	writeDailySummaryCount(&b, "Вложений", opts.Stats.Attachments)
	writeDailySummaryCount(&b, "Транскриптов", opts.Stats.Transcripts)
	if opts.Stats.DialogsScanned > 0 || opts.Stats.DialogsSkipped > 0 || len(opts.Stats.DialogErrors) > 0 {
		parts := make([]string, 0, 3)
		if opts.Stats.DialogsScanned > 0 {
			parts = append(parts, fmt.Sprintf("просканировано диалогов: %d", opts.Stats.DialogsScanned))
		}
		if opts.Stats.DialogsSkipped > 0 {
			parts = append(parts, fmt.Sprintf("пропущено по дате: %d", opts.Stats.DialogsSkipped))
		}
		if len(opts.Stats.DialogErrors) > 0 {
			parts = append(parts, fmt.Sprintf("ошибок: %d", len(opts.Stats.DialogErrors)))
		}
		b.WriteString("- Диалоги: " + strings.Join(parts, "; ") + "\n")
	}
	if opts.Stats.FloodWaits > 0 {
		b.WriteString(fmt.Sprintf("- Flood waits: %d\n", opts.Stats.FloodWaits))
	}
	if len(opts.Stats.DialogErrors) > 0 {
		b.WriteString("\n## Ошибки диалогов\n\n")
		for _, err := range opts.Stats.DialogErrors {
			b.WriteString("- " + err + "\n")
		}
	}
	b.WriteString("\n## Хронология\n\n")
	if len(records) == 0 {
		b.WriteString("Исходящих сообщений за этот день не найдено.\n")
		return b.String()
	}
	lastHour := ""
	for _, record := range records {
		hour := record.Date.In(moscowLocation).Format("15:00")
		if hour != lastHour {
			if lastHour != "" {
				b.WriteString("\n")
			}
			b.WriteString(fmt.Sprintf("### %s\n\n", hour))
			lastHour = hour
		}
		b.WriteString(dailyMessageLine(record))
		b.WriteString("\n")
		for _, attachment := range record.Attachments {
			b.WriteString("  - " + dailyAttachmentLine(attachment) + "\n")
			if transcript := compactTranscript(attachment.Transcript); transcript != "" {
				b.WriteString("  - транскрипт: " + transcript + "\n")
			}
		}
	}
	return b.String()
}

func formatDailySummaryTime(value time.Time) string {
	return value.In(moscowLocation).Format("2006-01-02 15:04")
}

func writeDailySummaryCount(b *strings.Builder, label string, count int) {
	if count > 0 {
		b.WriteString(fmt.Sprintf("- %s: %d\n", label, count))
	}
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
		return fmt.Sprintf("- %s в %s: %s [`#%d`](%s)", timeLabel, destination, text, record.MessageID, record.SourceURL)
	}
	return fmt.Sprintf("- %s в %s: %s `#%d`", timeLabel, destination, text, record.MessageID)
}

func dailyAttachmentLine(attachment Attachment) string {
	parts := []string{"файл: " + attachment.Kind}
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
	if attachment.DownloadHint != "" {
		parts = append(parts, "download_hint="+attachment.DownloadHint)
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
	return filepath.Join(stateDir, "reports", "jsonl", date+".jsonl"),
		filepath.Join(stateDir, "reports", "md", date+".md")
}
