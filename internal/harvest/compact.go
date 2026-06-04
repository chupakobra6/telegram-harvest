package harvest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type CompactOptions struct {
	InputPath      string
	OutputPath     string
	Since          time.Time
	Limit          int
	IncludeService bool
}

type CompactStats struct {
	Records int
	Written int
	Skipped int
}

func WriteCompactTOON(opts CompactOptions) (CompactStats, error) {
	if strings.TrimSpace(opts.InputPath) == "" {
		return CompactStats{}, fmt.Errorf("input path is required")
	}
	if strings.TrimSpace(opts.OutputPath) == "" {
		return CompactStats{}, fmt.Errorf("output path is required")
	}
	records, stats, err := readCompactRecords(opts)
	if err != nil {
		return CompactStats{}, err
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Date.Equal(records[j].Date) {
			if records[i].Chat.ID == records[j].Chat.ID {
				return records[i].MessageID > records[j].MessageID
			}
			return records[i].Chat.ID < records[j].Chat.ID
		}
		return records[i].Date.After(records[j].Date)
	})
	if opts.Limit > 0 && len(records) > opts.Limit {
		stats.Skipped += len(records) - opts.Limit
		records = records[:opts.Limit]
	}
	stats.Written = len(records)
	if err := writeCompactRecords(opts.OutputPath, opts.InputPath, records); err != nil {
		return CompactStats{}, err
	}
	return stats, nil
}

func readCompactRecords(opts CompactOptions) ([]MessageRecord, CompactStats, error) {
	file, err := os.Open(opts.InputPath)
	if err != nil {
		return nil, CompactStats{}, fmt.Errorf("open input: %w", err)
	}
	defer file.Close()

	records := make([]MessageRecord, 0)
	stats := CompactStats{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		stats.Records++
		var record MessageRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, stats, fmt.Errorf("parse input line %d: %w", stats.Records, err)
		}
		if !opts.IncludeService && record.Kind == "service" {
			stats.Skipped++
			continue
		}
		if !opts.Since.IsZero() && record.Date.Before(opts.Since) {
			stats.Skipped++
			continue
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, stats, fmt.Errorf("read input: %w", err)
	}
	return records, stats, nil
}

func writeCompactRecords(outputPath, inputPath string, records []MessageRecord) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("prepare output dir: %w", err)
	}
	file, err := os.OpenFile(outputPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open output: %w", err)
	}
	defer file.Close()

	writer := bufio.NewWriter(file)

	generatedAt := time.Now().UTC().Format(time.RFC3339)
	fmt.Fprintf(writer, "source: telegram\n")
	fmt.Fprintf(writer, "source_jsonl: %s\n", toonValue(inputPath, ','))
	fmt.Fprintf(writer, "generated_at: %s\n", generatedAt)
	fmt.Fprintf(writer, "canonical_format: jsonl\n")
	fmt.Fprintf(writer, "messages[%d|]{date|chat_id|chat|topic_id|topic|msg_id|reply_to|kind|from|text|links|files}:\n", len(records))
	for _, record := range records {
		row := []string{
			formatDate(record.Date),
			strconv.FormatInt(record.Chat.ID, 10),
			toonValue(displayChat(record.Chat), '|'),
			formatTopicID(record),
			toonValue(displayTopic(record), '|'),
			strconv.Itoa(record.MessageID),
			formatPositiveInt(record.ReplyToMessageID),
			toonValue(record.Kind, '|'),
			toonValue(displaySender(record.Sender), '|'),
			toonValue(compactText(record.Text), '|'),
			toonValue(strings.Join(record.Links, "; "), '|'),
			toonValue(displayAttachments(record.Attachments), '|'),
		}
		fmt.Fprintf(writer, "  %s\n", strings.Join(row, "|"))
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush output: %w", err)
	}
	return nil
}

func formatDate(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func displayChat(chat Chat) string {
	switch {
	case strings.TrimSpace(chat.Display) != "":
		return chat.Display
	case strings.TrimSpace(chat.Title) != "":
		return chat.Title
	case strings.TrimSpace(chat.Username) != "":
		return "@" + strings.TrimPrefix(chat.Username, "@")
	default:
		return strconv.FormatInt(chat.ID, 10)
	}
}

func displayTopic(record MessageRecord) string {
	if record.Topic != nil {
		return record.Topic.Title
	}
	return ""
}

func formatTopicID(record MessageRecord) string {
	if record.Topic != nil && record.Topic.ID > 0 {
		return strconv.Itoa(record.Topic.ID)
	}
	if record.ThreadTopMessageID > 0 {
		return strconv.Itoa(record.ThreadTopMessageID)
	}
	return ""
}

func displaySender(sender Sender) string {
	if sender.Username != "" {
		return "@" + strings.TrimPrefix(sender.Username, "@")
	}
	if sender.Display != "" {
		return sender.Display
	}
	if sender.ID != 0 {
		return strconv.FormatInt(sender.ID, 10)
	}
	return ""
}

func displayAttachments(attachments []Attachment) string {
	if len(attachments) == 0 {
		return ""
	}
	parts := make([]string, 0, len(attachments))
	for _, attachment := range attachments {
		label := attachment.Kind
		if attachment.FileName != "" {
			label += ":" + attachment.FileName
		} else if attachment.Title != "" {
			label += ":" + attachment.Title
		} else if attachment.URL != "" {
			label += ":" + attachment.URL
		} else if attachment.MIMEType != "" {
			label += ":" + attachment.MIMEType
		}
		if attachment.LocalPath != "" {
			label += " -> " + attachment.LocalPath
		}
		if attachment.DownloadError != "" {
			label += " [download_error: " + attachment.DownloadError + "]"
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, "; ")
}

func formatPositiveInt(value int) string {
	if value <= 0 {
		return ""
	}
	return strconv.Itoa(value)
}

func compactText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.ReplaceAll(value, "\t", " ")
	parts := strings.Split(value, "\n")
	for i := range parts {
		parts[i] = strings.Join(strings.Fields(parts[i]), " ")
	}
	return strings.TrimSpace(strings.Join(parts, "\\n"))
}

func toonValue(value string, delimiter rune) string {
	if value == "" {
		return ""
	}
	if !needsTOONQuote(value, delimiter) {
		return value
	}
	escaped := strings.ReplaceAll(value, "\\", "\\\\")
	escaped = strings.ReplaceAll(escaped, "\"", "\\\"")
	escaped = strings.ReplaceAll(escaped, "\n", "\\n")
	escaped = strings.ReplaceAll(escaped, "\r", "\\r")
	escaped = strings.ReplaceAll(escaped, "\t", "\\t")
	return `"` + escaped + `"`
}

func needsTOONQuote(value string, delimiter rune) bool {
	if strings.TrimSpace(value) != value {
		return true
	}
	if strings.ContainsRune(value, delimiter) {
		return true
	}
	if strings.ContainsAny(value, "\n\r\t\\\"#[]{}:") {
		return true
	}
	lower := strings.ToLower(value)
	if lower == "true" || lower == "false" || lower == "null" {
		return true
	}
	if _, err := strconv.ParseFloat(value, 64); err == nil {
		return true
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
