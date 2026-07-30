package harvest

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const dailyTranscriptPreviewRunes = 4000

type DailyMarkdownOptions struct {
	OutputPath string
	Date       string
	Start      time.Time
	End        time.Time
	Stats      OutgoingStats
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
	writeDailySummaryCount(&b, "Сообщений", len(records))
	writeDailySummaryCount(&b, "Чатов с сообщениями", dailyChatCount(records))
	writeDailySummaryCount(&b, "Вложений", opts.Stats.Attachments)
	writeDailySummaryCount(&b, "Транскриптов", opts.Stats.Transcripts)
	writeDailySummaryCount(&b, "Пересланных", opts.Stats.Forwarded)
	if len(opts.Stats.DialogErrors) > 0 {
		b.WriteString("\n## Проблемы сбора\n\n")
		for _, err := range opts.Stats.DialogErrors {
			b.WriteString("- " + err + "\n")
		}
	}
	b.WriteString("\n## Хронология\n\n")
	if len(records) == 0 {
		b.WriteString("Сообщений за этот день не найдено.\n")
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
		writeDailyMessage(&b, record)
		b.WriteString("\n")
	}
	return b.String()
}

func writeDailySummaryCount(b *strings.Builder, label string, count int) {
	if count > 0 {
		b.WriteString(fmt.Sprintf("- %s: %d\n", label, count))
	}
}

func writeDailyMessage(b *strings.Builder, record MessageRecord) {
	b.WriteString(dailyMessageHeader(record))
	b.WriteString("\n")
	if label, origin := dailyForwardOrigin(record.Forward); origin != "" {
		b.WriteString("\n")
		b.WriteString("  **" + label + ":** " + origin + "\n")
	}
	if text := dailyMarkdownText(record.Text); text != "" {
		b.WriteString("\n")
		writeDailyQuote(b, text, "  ")
	} else if len(record.Attachments) == 0 && strings.TrimSpace(record.Kind) != "" && record.Kind != "text" {
		b.WriteString("\n")
		b.WriteString("  _" + dailyAttachmentKindLabel(record.Kind) + "_\n")
	}
	for _, attachment := range record.Attachments {
		b.WriteString("\n")
		b.WriteString("  **Вложение:** " + dailyAttachmentSummary(attachment) + "\n")
		if transcript := compactTranscript(attachment.Transcript); transcript != "" {
			b.WriteString("\n")
			b.WriteString("  **Транскрипт:**\n")
			writeDailyQuote(b, transcript, "  ")
		}
	}
}

func dailyForwardOrigin(forward *ForwardInfo) (string, string) {
	if forward == nil {
		return "", ""
	}
	label := "Переслано от"
	origin := strings.TrimSpace(forward.OriginName)
	if forward.Origin != nil {
		if display := strings.TrimSpace(forward.Origin.Display); display != "" {
			origin = display
		} else if username := strings.TrimSpace(forward.Origin.Username); username != "" {
			origin = "@" + strings.TrimPrefix(username, "@")
		}
		if forward.Origin.Type == "channel" || forward.Origin.Type == "supergroup" {
			label = "Переслано из"
		}
	}
	if origin == "" {
		origin = "источник скрыт Telegram"
	}
	if strings.TrimSpace(forward.SourceURL) != "" {
		origin = markdownLink(origin, forward.SourceURL)
	}
	if author := strings.TrimSpace(forward.PostAuthor); author != "" {
		origin += " · автор: " + author
	}
	return label, origin
}

func dailyMessageHeader(record MessageRecord) string {
	timeLabel := record.Date.In(moscowLocation).Format("15:04")
	destination := displayChat(record.Chat)
	if topic := displayTopic(record); topic != "" {
		destination += " / " + topic
	}
	ref := fmt.Sprintf("#%d", record.MessageID)
	if strings.TrimSpace(record.SourceURL) != "" {
		ref = fmt.Sprintf("[#%d](%s)", record.MessageID, record.SourceURL)
	}
	if !record.Outgoing && !record.Sender.Self {
		sender := strings.TrimSpace(record.Sender.Display)
		if sender == "" {
			sender = displaySender(record.Sender)
		}
		if sender == "" {
			sender = "неизвестный отправитель"
		}
		return fmt.Sprintf("- **%s**, **%s** в **%s** %s", timeLabel, sender, destination, ref)
	}
	return fmt.Sprintf("- **%s** в **%s** %s", timeLabel, destination, ref)
}

func dailyAttachmentSummary(attachment Attachment) string {
	label := dailyAttachmentKindLabel(attachment.Kind)
	url := strings.TrimSpace(attachment.URL)
	if url == "" {
		return label
	}
	title := strings.TrimSpace(attachment.Title)
	if title == "" {
		title = url
	}
	return label + ": " + markdownLink(title, url)
}

func dailyAttachmentKindLabel(kind string) string {
	switch strings.TrimSpace(kind) {
	case "photo", "image":
		return "изображение"
	case "voice":
		return "голосовое"
	case "audio":
		return "аудио"
	case "round_video":
		return "кружок"
	case "video":
		return "видео"
	case "document":
		return "документ"
	case "webpage":
		return "ссылка"
	case "":
		return "вложение"
	default:
		return kind
	}
}

func dailyChatCount(records []MessageRecord) int {
	seen := map[int64]struct{}{}
	for _, record := range records {
		seen[record.Chat.ID] = struct{}{}
	}
	return len(seen)
}

func compactTranscript(value string) string {
	value = dailyMarkdownText(value)
	if value == "" {
		return ""
	}
	if utf8.RuneCountInString(value) <= dailyTranscriptPreviewRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:dailyTranscriptPreviewRunes]) + " ... [обрезано]"
}

func dailyMarkdownText(value string) string {
	value = strings.ReplaceAll(value, "<br />", "\n")
	value = strings.ReplaceAll(value, "<br/>", "\n")
	value = strings.ReplaceAll(value, "<br>", "\n")
	return strings.ReplaceAll(compactText(value), "\\n", "\n")
}

func writeDailyQuote(b *strings.Builder, value string, prefix string) {
	lines := strings.Split(value, "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			b.WriteString(prefix + ">\n")
			continue
		}
		b.WriteString(prefix + "> " + line + "\n")
	}
}

func markdownLink(label string, url string) string {
	label = strings.ReplaceAll(label, "\\", "\\\\")
	label = strings.ReplaceAll(label, "]", "\\]")
	label = strings.ReplaceAll(label, "\n", " ")
	return "[" + label + "](" + url + ")"
}

func DailyDefaultOutputPaths(stateDir string, date string) (string, string) {
	reportRoot := DailyDefaultReportRoot(stateDir)
	return filepath.Join(stateDir, "jsonl", date+".jsonl"),
		filepath.Join(reportRoot, date+".md")
}

func DailyDefaultASRLogPath(stateDir string, date string) string {
	return filepath.Join(stateDir, "asr", date+".jsonl")
}

func DailyDefaultReportRoot(stateDir string) string {
	stateDir = strings.TrimSpace(stateDir)
	if stateDir == "" {
		return filepath.Join("reports", "daily")
	}
	clean := filepath.Clean(stateDir)
	for current := clean; ; current = filepath.Dir(current) {
		if filepath.Base(current) == ".state" {
			parent := filepath.Dir(current)
			return filepath.Join(parent, "reports", "daily")
		}
		next := filepath.Dir(current)
		if next == current {
			break
		}
	}
	return filepath.Join(clean, "reports", "daily")
}
