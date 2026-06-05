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
	"unicode/utf8"
)

const (
	defaultRecentLimit = 300
	recentTextLimit    = 500
)

var moscowLocation = time.FixedZone("Europe/Moscow", 3*60*60)

type AgentViewOptions struct {
	InputPath      string
	OutputDir      string
	Since          time.Time
	RecentLimit    int
	IncludeService bool
}

type AgentViewStats struct {
	Mode         string
	Records      int
	Written      int
	Skipped      int
	RawAdded     int
	VisibleAdded int
	Chats        int
	Topics       int
	Files        int
}

type chatView struct {
	Chat    Chat
	Records []MessageRecord
	Topics  map[string]*topicView
}

type topicView struct {
	Key     string
	Title   string
	Topic   *Topic
	Records []MessageRecord
	Days    map[string][]MessageRecord
}

func WriteAgentMarkdownView(opts AgentViewOptions) (AgentViewStats, error) {
	if strings.TrimSpace(opts.InputPath) == "" {
		return AgentViewStats{}, fmt.Errorf("input path is required")
	}
	if strings.TrimSpace(opts.OutputDir) == "" {
		return AgentViewStats{}, fmt.Errorf("output dir is required")
	}
	if opts.RecentLimit <= 0 {
		opts.RecentLimit = defaultRecentLimit
	}
	records, stats, err := readAgentViewRecords(opts)
	if err != nil {
		return AgentViewStats{}, err
	}
	sourceInfo, err := os.Stat(opts.InputPath)
	if err != nil {
		return AgentViewStats{}, fmt.Errorf("stat input: %w", err)
	}
	sortRecordsNewestFirst(records)
	stats.Mode = "rebuild"
	stats.Written = len(records)
	stats.RawAdded = stats.Records
	stats.VisibleAdded = stats.Written

	chats := buildChatViews(records)
	stats.Chats = len(chats)
	stats.Topics = countTopics(chats)
	if err := prepareAgentViewOutput(opts.OutputDir, opts.InputPath); err != nil {
		return AgentViewStats{}, err
	}
	files, err := writeAgentViewFiles(opts, records, chats)
	if err != nil {
		return AgentViewStats{}, err
	}
	stats.Files = files
	manifest := buildAgentViewManifest(opts, sourceInfo.Size(), stats, records)
	if err := writeAgentViewManifest(opts.OutputDir, manifest); err != nil {
		return AgentViewStats{}, err
	}
	stats.Files++
	return stats, nil
}

func readAgentViewRecords(opts AgentViewOptions) ([]MessageRecord, AgentViewStats, error) {
	file, err := os.Open(opts.InputPath)
	if err != nil {
		return nil, AgentViewStats{}, fmt.Errorf("open input: %w", err)
	}
	defer file.Close()

	records := make([]MessageRecord, 0)
	stats := AgentViewStats{}
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

func sortRecordsNewestFirst(records []MessageRecord) {
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Date.Equal(records[j].Date) {
			if records[i].Chat.ID == records[j].Chat.ID {
				return records[i].MessageID > records[j].MessageID
			}
			return records[i].Chat.ID < records[j].Chat.ID
		}
		return records[i].Date.After(records[j].Date)
	})
}

func buildChatViews(records []MessageRecord) []*chatView {
	byID := make(map[int64]*chatView)
	for _, record := range records {
		chat := byID[record.Chat.ID]
		if chat == nil {
			chat = &chatView{
				Chat:   record.Chat,
				Topics: make(map[string]*topicView),
			}
			byID[record.Chat.ID] = chat
		}
		chat.Records = append(chat.Records, record)
		key := topicKey(record)
		topic := chat.Topics[key]
		if topic == nil {
			topic = &topicView{
				Key:   key,
				Title: topicTitle(record, key),
				Topic: record.Topic,
				Days:  make(map[string][]MessageRecord),
			}
			chat.Topics[key] = topic
		}
		topic.Records = append(topic.Records, record)
		day := record.Date.In(moscowLocation).Format("2006-01-02")
		topic.Days[day] = append(topic.Days[day], record)
	}

	chats := make([]*chatView, 0, len(byID))
	for _, chat := range byID {
		sortRecordsNewestFirst(chat.Records)
		for _, topic := range chat.Topics {
			sortRecordsNewestFirst(topic.Records)
			for day := range topic.Days {
				sortRecordsOldestFirst(topic.Days[day])
			}
		}
		chats = append(chats, chat)
	}
	sort.SliceStable(chats, func(i, j int) bool {
		left := latestDate(chats[i].Records)
		right := latestDate(chats[j].Records)
		if left.Equal(right) {
			return displayChat(chats[i].Chat) < displayChat(chats[j].Chat)
		}
		return left.After(right)
	})
	return chats
}

func sortRecordsOldestFirst(records []MessageRecord) {
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Date.Equal(records[j].Date) {
			if records[i].Chat.ID == records[j].Chat.ID {
				return records[i].MessageID < records[j].MessageID
			}
			return records[i].Chat.ID < records[j].Chat.ID
		}
		return records[i].Date.Before(records[j].Date)
	})
}

func countTopics(chats []*chatView) int {
	total := 0
	for _, chat := range chats {
		total += len(chat.Topics)
	}
	return total
}

func prepareAgentViewOutput(outputDir, inputPath string) error {
	cleanOutput := filepath.Clean(outputDir)
	if cleanOutput == "." || cleanOutput == string(filepath.Separator) {
		return fmt.Errorf("refusing to use dangerous output dir: %s", outputDir)
	}
	if filepath.Clean(filepath.Dir(inputPath)) == cleanOutput {
		return fmt.Errorf("refusing to replace input directory: %s", outputDir)
	}
	if err := os.RemoveAll(cleanOutput); err != nil {
		return fmt.Errorf("clean output dir: %w", err)
	}
	if err := os.MkdirAll(cleanOutput, 0o700); err != nil {
		return fmt.Errorf("prepare output dir: %w", err)
	}
	return nil
}

func writeAgentViewFiles(opts AgentViewOptions, records []MessageRecord, chats []*chatView) (int, error) {
	files := 0
	if err := writeTextFile(filepath.Join(opts.OutputDir, "AGENTS.md"), renderAgentViewInstructions(opts)); err != nil {
		return files, err
	}
	files++
	if err := writeTextFile(filepath.Join(opts.OutputDir, "README.md"), renderAgentIndex(opts, records, chats)); err != nil {
		return files, err
	}
	files++
	if err := writeTextFile(filepath.Join(opts.OutputDir, "all-recent.md"), renderAllRecent(opts, records)); err != nil {
		return files, err
	}
	files++
	for _, chat := range chats {
		chatDir := filepath.Join(opts.OutputDir, "chats", chatSlug(chat.Chat))
		if err := writeTextFile(filepath.Join(chatDir, "README.md"), renderChatIndex(opts, chat)); err != nil {
			return files, err
		}
		files++
		topics := sortedTopics(chat)
		for _, topic := range topics {
			topicDir := filepath.Join(chatDir, "topics", topic.Key)
			if err := writeTextFile(filepath.Join(topicDir, "README.md"), renderTopicIndex(opts, chat, topic)); err != nil {
				return files, err
			}
			files++
			days := sortedDaysNewestFirst(topic)
			for _, day := range days {
				path := filepath.Join(topicDir, day+".md")
				if err := writeTextFile(path, renderDayMessages(chat, topic, day)); err != nil {
					return files, err
				}
				files++
			}
		}
	}
	return files, nil
}

func renderAgentViewInstructions(opts AgentViewOptions) string {
	var b strings.Builder
	b.WriteString("# AGENTS.md\n\n")
	b.WriteString("## Telegram Study View Rules\n")
	b.WriteString("- This directory is generated from the Telegram JSONL dump. Do not edit files here by hand.\n")
	b.WriteString("- Start with `README.md`; use `all-recent.md` only for a fast latest-message scan.\n")
	b.WriteString("- For a concrete task, narrow context in this order: chat README, topic README, one date file.\n")
	b.WriteString("- Do not open `messages.jsonl` for normal extraction. Use raw JSONL only when a field omitted from Markdown is required for audit/debug.\n")
	b.WriteString("- Prefer `rg -n \"keyword\" .` inside this directory or inside one chat/topic directory before opening large files.\n")
	b.WriteString("- Cite Telegram facts as `path/to/YYYY-MM-DD.md #message_id` or `all-recent.md #message_id`.\n")
	b.WriteString("- When an attachment shows a `local_path`, open that local file if the message may contain an assignment, marks, schedule, or instructions.\n")
	b.WriteString("- Service messages, reply ids, thread ids, views, raw Telegram URLs, and raw JSON keys are intentionally omitted from Markdown.\n")
	b.WriteString("- If the needed fact is absent or ambiguous, ask for a fresh sync/handoff instead of guessing.\n")
	b.WriteString("\n")
	b.WriteString("## Source\n")
	b.WriteString(fmt.Sprintf("- Raw JSONL source: `%s`\n", opts.InputPath))
	b.WriteString("- Update with `telegram-study-harvest agent-view --in messages.jsonl --out-dir agent-view` after every sync.\n")
	b.WriteString("- Use `--rebuild` only when forcing a full rewrite; normal runs use the stored source offset.\n")
	return b.String()
}

func renderAgentIndex(opts AgentViewOptions, records []MessageRecord, chats []*chatView) string {
	var b strings.Builder
	b.WriteString("# Telegram Study Agent View\n\n")
	b.WriteString("This is the fast navigation layer over Telegram study chats. It is generated from JSONL and optimized for agents.\n")
	b.WriteString("Use it before raw dumps: it keeps the useful study text and removes noisy Telegram fields.\n\n")
	b.WriteString("## Start Here\n\n")
	b.WriteString("1. Read this file to see configured chats and date ranges.\n")
	b.WriteString("2. Open [all-recent.md](all-recent.md) when the task is about the latest week or unclear source.\n")
	b.WriteString("3. If the subject/chat is known, open the chat README from the table below.\n")
	b.WriteString("4. In the chat README, choose a topic. In the topic README, choose one date file.\n")
	b.WriteString("5. Use raw JSONL only when Markdown lacks a field needed for audit/debug.\n\n")
	b.WriteString("## Fast Search\n\n")
	b.WriteString("- Unknown chat/topic: `rg -n \"дедлайн|deadline|домаш|дз|задал|сдать|экзамен|зачет|SmartLMS\" .`\n")
	b.WriteString("- Known chat: run `rg` inside `chats/chat-...` instead of the full view.\n")
	b.WriteString("- Known topic: run `rg` inside `chats/chat-.../topics/topic-...` and open only matching day files.\n\n")
	b.WriteString("## Token Policy\n\n")
	b.WriteString("- Raw append-only source stays in JSONL; Markdown files are rebuildable slices.\n")
	b.WriteString("- Message lines omit JSON keys, reply ids, thread ids, views, Telegram source URLs, and raw service actions.\n")
	b.WriteString("- Attachment labels include `local_path` for saved files and `transcript_path` for transcribed audio/video.\n")
	b.WriteString("- Service messages are skipped by default.\n")
	b.WriteString("- Each visible message keeps `#message_id`; cite facts as `path #message_id`.\n")
	b.WriteString("- Message time is Europe/Moscow local time.\n\n")
	b.WriteString("## Files\n\n")
	b.WriteString(fmt.Sprintf("- Raw merged JSONL: `%s`\n", opts.InputPath))
	b.WriteString("- Latest cross-chat slice: [all-recent.md](all-recent.md)\n")
	b.WriteString("- Agent rules in this generated directory: [AGENTS.md](AGENTS.md)\n")
	b.WriteString(fmt.Sprintf("- Generated at: `%s`\n", time.Now().In(moscowLocation).Format(time.RFC3339)))
	if !opts.Since.IsZero() {
		b.WriteString(fmt.Sprintf("- Since filter: `%s`\n", opts.Since.In(moscowLocation).Format(time.RFC3339)))
	}
	b.WriteString("\n")
	b.WriteString("## Chats\n\n")
	b.WriteString("| Chat | Messages | Date Range | Open |\n")
	b.WriteString("| --- | ---: | --- | --- |\n")
	for _, chat := range chats {
		b.WriteString(fmt.Sprintf(
			"| %s | %d | %s | [%s](%s) |\n",
			mdCell(displayChat(chat.Chat)),
			len(chat.Records),
			mdCell(dateRange(chat.Records)),
			mdCell(chatSlug(chat.Chat)),
			mdPath(filepath.ToSlash(filepath.Join("chats", chatSlug(chat.Chat), "README.md"))),
		))
	}
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("Total visible messages: `%d`.\n", len(records)))
	return b.String()
}

func renderAllRecent(opts AgentViewOptions, records []MessageRecord) string {
	limit := opts.RecentLimit
	if limit > len(records) {
		limit = len(records)
	}
	var b strings.Builder
	b.WriteString("# Recent Telegram Messages\n\n")
	b.WriteString(fmt.Sprintf("Newest `%d` visible messages across configured study chats.\n", limit))
	b.WriteString("Use this file for a first pass only; open chat/topic/day files before extracting final facts.\n\n")
	lastDay := ""
	for _, record := range records[:limit] {
		day := record.Date.In(moscowLocation).Format("2006-01-02")
		if day != lastDay {
			if lastDay != "" {
				b.WriteString("\n")
			}
			b.WriteString(fmt.Sprintf("## %s\n\n", day))
			lastDay = day
		}
		b.WriteString(messageLine(record, true, recentTextLimit))
		b.WriteString("\n")
	}
	return b.String()
}

func renderChatIndex(opts AgentViewOptions, chat *chatView) string {
	var b strings.Builder
	chatName := displayChat(chat.Chat)
	b.WriteString(fmt.Sprintf("# Chat: %s\n\n", chatName))
	b.WriteString(fmt.Sprintf("- Chat id: `%d` (source lookup only; agents normally do not need it)\n", chat.Chat.ID))
	b.WriteString(fmt.Sprintf("- Messages in view: `%d`\n", len(chat.Records)))
	b.WriteString(fmt.Sprintf("- Date range: `%s`\n", dateRange(chat.Records)))
	b.WriteString("- Latest cross-chat context: [../../all-recent.md](../../all-recent.md)\n\n")
	b.WriteString("## How To Read\n\n")
	b.WriteString("Use this file as a topic map. Open one topic README, then a single date file.\n")
	b.WriteString("If the topic is unclear, search inside this chat directory before opening many files.\n\n")
	b.WriteString("```bash\n")
	b.WriteString("rg -n \"дедлайн|deadline|домаш|дз|задал|сдать|экзамен|зачет|SmartLMS\" .\n")
	b.WriteString("```\n\n")
	b.WriteString("## Topics\n\n")
	b.WriteString("| Topic | Messages | Date Range | Open |\n")
	b.WriteString("| --- | ---: | --- | --- |\n")
	for _, topic := range sortedTopics(chat) {
		b.WriteString(fmt.Sprintf(
			"| %s | %d | %s | [%s](%s) |\n",
			mdCell(topic.Title),
			len(topic.Records),
			mdCell(dateRange(topic.Records)),
			mdCell(topic.Key),
			mdPath(filepath.ToSlash(filepath.Join("topics", topic.Key, "README.md"))),
		))
	}
	return b.String()
}

func renderTopicIndex(opts AgentViewOptions, chat *chatView, topic *topicView) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# %s / %s\n\n", displayChat(chat.Chat), topic.Title))
	b.WriteString(fmt.Sprintf("- Messages in topic view: `%d`\n", len(topic.Records)))
	b.WriteString(fmt.Sprintf("- Date range: `%s`\n", dateRange(topic.Records)))
	if topic.Topic != nil && topic.Topic.ID > 0 {
		b.WriteString(fmt.Sprintf("- Topic id: `%d` (source lookup only)\n", topic.Topic.ID))
	}
	b.WriteString("- Chat index: [../../README.md](../../README.md)\n\n")
	b.WriteString("## How To Read\n\n")
	b.WriteString("Days are newest-first. Prefer opening one date file or searching this topic directory first.\n\n")
	b.WriteString("```bash\n")
	b.WriteString("rg -n \"дедлайн|deadline|домаш|дз|задал|сдать|экзамен|зачет|SmartLMS\" .\n")
	b.WriteString("```\n\n")
	b.WriteString("## Days\n\n")
	b.WriteString("| Day | Messages | Open |\n")
	b.WriteString("| --- | ---: | --- |\n")
	for _, day := range sortedDaysNewestFirst(topic) {
		b.WriteString(fmt.Sprintf(
			"| %s | %d | [%s](%s) |\n",
			day,
			len(topic.Days[day]),
			day,
			mdPath(day+".md"),
		))
	}
	return b.String()
}

func renderDayMessages(chat *chatView, topic *topicView, day string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# %s / %s / %s\n\n", displayChat(chat.Chat), topic.Title, day))
	b.WriteString("Format: `HH:MM sender: text #message_id`. Links and attachment names stay inline when present.\n")
	b.WriteString("This file is the smallest normal reading unit for Telegram evidence.\n\n")
	for _, record := range topic.Days[day] {
		b.WriteString(messageLine(record, false, 0))
		b.WriteString("\n")
	}
	return b.String()
}

func writeTextFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("prepare output dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func messageLine(record MessageRecord, includeChat bool, maxTextRunes int) string {
	timeLabel := record.Date.In(moscowLocation).Format("15:04")
	prefix := timeLabel
	if includeChat {
		prefix += " " + displayChat(record.Chat)
		topic := displayTopic(record)
		if topic != "" {
			prefix += " / " + topic
		}
	}
	sender := displaySender(record.Sender)
	if sender == "" {
		sender = "unknown"
	}
	text := compactText(record.Text)
	if maxTextRunes > 0 {
		text = truncateRunes(text, maxTextRunes)
	}
	if text == "" {
		text = "[no text]"
	}
	line := fmt.Sprintf("- %s %s: %s", prefix, sender, text)
	if links := strings.Join(record.Links, "; "); links != "" {
		line += " links: " + links
	}
	if attachments := displayAttachments(record.Attachments); attachments != "" {
		line += " files: " + attachments
	}
	line += fmt.Sprintf(" `#%d`", record.MessageID)
	return line
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit]) + " ... [truncated]"
}

func sortedTopics(chat *chatView) []*topicView {
	topics := make([]*topicView, 0, len(chat.Topics))
	for _, topic := range chat.Topics {
		topics = append(topics, topic)
	}
	sort.SliceStable(topics, func(i, j int) bool {
		left := latestDate(topics[i].Records)
		right := latestDate(topics[j].Records)
		if left.Equal(right) {
			return topics[i].Title < topics[j].Title
		}
		return left.After(right)
	})
	return topics
}

func sortedDaysNewestFirst(topic *topicView) []string {
	days := make([]string, 0, len(topic.Days))
	for day := range topic.Days {
		days = append(days, day)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	return days
}

func latestDate(records []MessageRecord) time.Time {
	var latest time.Time
	for _, record := range records {
		if record.Date.After(latest) {
			latest = record.Date
		}
	}
	return latest
}

func oldestDate(records []MessageRecord) time.Time {
	if len(records) == 0 {
		return time.Time{}
	}
	oldest := records[0].Date
	for _, record := range records[1:] {
		if record.Date.Before(oldest) {
			oldest = record.Date
		}
	}
	return oldest
}

func dateRange(records []MessageRecord) string {
	if len(records) == 0 {
		return ""
	}
	return oldestDate(records).In(moscowLocation).Format("2006-01-02") + " .. " + latestDate(records).In(moscowLocation).Format("2006-01-02")
}

func chatSlug(chat Chat) string {
	if chat.ID != 0 {
		return "chat-" + strconv.FormatInt(chat.ID, 10)
	}
	return "chat-unknown"
}

func topicKey(record MessageRecord) string {
	if record.Topic != nil && record.Topic.ID > 0 {
		return "topic-" + strconv.Itoa(record.Topic.ID)
	}
	if record.ThreadTopMessageID > 0 {
		return "topic-" + strconv.Itoa(record.ThreadTopMessageID)
	}
	if record.Chat.Forum {
		return "topic-none"
	}
	return "topic-main"
}

func topicTitle(record MessageRecord, key string) string {
	if record.Topic != nil && strings.TrimSpace(record.Topic.Title) != "" {
		return record.Topic.Title
	}
	switch key {
	case "topic-main":
		return "main"
	case "topic-none":
		return "no topic"
	default:
		return key
	}
}

func mdCell(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "|", "\\|")
	return strings.TrimSpace(value)
}

func mdPath(value string) string {
	return strings.ReplaceAll(value, " ", "%20")
}
