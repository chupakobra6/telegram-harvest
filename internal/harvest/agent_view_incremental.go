package harvest

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	agentViewManifestVersion = 2
	agentViewManifestName    = ".agent-view-state.json"
)

type agentViewManifest struct {
	Version        int                              `json:"version"`
	SourcePath     string                           `json:"source_path"`
	SourceSize     int64                            `json:"source_size"`
	SourceLines    int                              `json:"source_lines"`
	VisibleRecords int                              `json:"visible_records"`
	SkippedRecords int                              `json:"skipped_records"`
	RecentLimit    int                              `json:"recent_limit"`
	IncludeService bool                             `json:"include_service"`
	Since          string                           `json:"since,omitempty"`
	GeneratedAt    time.Time                        `json:"generated_at"`
	Chats          map[string]*agentViewChatSummary `json:"chats"`
	Recent         []MessageRecord                  `json:"recent"`
}

type agentViewChatSummary struct {
	Key       string                            `json:"key"`
	Chat      Chat                              `json:"chat"`
	Count     int                               `json:"count"`
	FirstDate time.Time                         `json:"first_date"`
	LastDate  time.Time                         `json:"last_date"`
	Topics    map[string]*agentViewTopicSummary `json:"topics"`
}

type agentViewTopicSummary struct {
	Key       string                          `json:"key"`
	Title     string                          `json:"title"`
	Topic     *Topic                          `json:"topic,omitempty"`
	Count     int                             `json:"count"`
	FirstDate time.Time                       `json:"first_date"`
	LastDate  time.Time                       `json:"last_date"`
	Days      map[string]*agentViewDaySummary `json:"days"`
}

type agentViewDaySummary struct {
	Day       string    `json:"day"`
	Count     int       `json:"count"`
	FirstDate time.Time `json:"first_date"`
	LastDate  time.Time `json:"last_date"`
}

func UpdateAgentMarkdownView(opts AgentViewOptions) (AgentViewStats, error) {
	if strings.TrimSpace(opts.InputPath) == "" {
		return AgentViewStats{}, fmt.Errorf("input path is required")
	}
	if strings.TrimSpace(opts.OutputDir) == "" {
		return AgentViewStats{}, fmt.Errorf("output dir is required")
	}
	if opts.RecentLimit <= 0 {
		opts.RecentLimit = defaultRecentLimit
	}
	sourceInfo, err := os.Stat(opts.InputPath)
	if err != nil {
		return AgentViewStats{}, fmt.Errorf("stat input: %w", err)
	}
	manifest, err := readAgentViewManifest(opts.OutputDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return WriteAgentMarkdownView(opts)
		}
		return AgentViewStats{}, err
	}
	if !agentViewManifestCompatible(opts, manifest) || sourceInfo.Size() < manifest.SourceSize {
		return WriteAgentMarkdownView(opts)
	}
	if sourceInfo.Size() == manifest.SourceSize {
		stats := agentViewStatsFromManifest(manifest)
		stats.Mode = "noop"
		return stats, nil
	}
	if ok, err := sourceOffsetAtLineBoundary(opts.InputPath, manifest.SourceSize); err != nil {
		return AgentViewStats{}, err
	} else if !ok {
		return WriteAgentMarkdownView(opts)
	}

	newRecords, delta, err := readAgentViewRecordsFromOffset(opts, manifest.SourceSize)
	if err != nil {
		return AgentViewStats{}, err
	}
	if len(newRecords) > 0 {
		sortRecordsNewestFirst(newRecords)
		if err := appendAgentViewDayFiles(opts.OutputDir, newRecords); err != nil {
			return AgentViewStats{}, err
		}
	}

	applyAgentViewManifestDelta(&manifest, sourceInfo.Size(), delta, newRecords)
	files, err := writeAgentViewIndexFilesFromManifest(opts, manifest)
	if err != nil {
		return AgentViewStats{}, err
	}
	if err := writeAgentViewManifest(opts.OutputDir, manifest); err != nil {
		return AgentViewStats{}, err
	}

	stats := agentViewStatsFromManifest(manifest)
	stats.Mode = "incremental"
	stats.RawAdded = delta.Records
	stats.VisibleAdded = len(newRecords)
	stats.Files = files + countAffectedDayFiles(newRecords) + 1
	return stats, nil
}

func buildAgentViewManifest(opts AgentViewOptions, sourceSize int64, stats AgentViewStats, records []MessageRecord) agentViewManifest {
	manifest := agentViewManifest{
		Version:        agentViewManifestVersion,
		SourcePath:     filepath.Clean(opts.InputPath),
		SourceSize:     sourceSize,
		SourceLines:    stats.Records,
		VisibleRecords: len(records),
		SkippedRecords: stats.Skipped,
		RecentLimit:    opts.RecentLimit,
		IncludeService: opts.IncludeService,
		Since:          formatAgentViewSince(opts.Since),
		GeneratedAt:    time.Now().UTC(),
		Chats:          make(map[string]*agentViewChatSummary),
	}
	applyVisibleRecordsToManifest(&manifest, records)
	manifest.Recent = limitedRecentRecords(records, opts.RecentLimit)
	return manifest
}

func readAgentViewManifest(outputDir string) (agentViewManifest, error) {
	path := filepath.Join(outputDir, agentViewManifestName)
	file, err := os.Open(path)
	if err != nil {
		return agentViewManifest{}, err
	}
	defer file.Close()
	var manifest agentViewManifest
	if err := json.NewDecoder(file).Decode(&manifest); err != nil {
		return agentViewManifest{}, fmt.Errorf("parse agent-view manifest: %w", err)
	}
	if manifest.Chats == nil {
		manifest.Chats = make(map[string]*agentViewChatSummary)
	}
	return manifest, nil
}

func writeAgentViewManifest(outputDir string, manifest agentViewManifest) error {
	path := filepath.Join(outputDir, agentViewManifestName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("prepare manifest dir: %w", err)
	}
	content, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode agent-view manifest: %w", err)
	}
	content = append(content, '\n')
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return fmt.Errorf("write agent-view manifest: %w", err)
	}
	return nil
}

func agentViewManifestCompatible(opts AgentViewOptions, manifest agentViewManifest) bool {
	if manifest.Version != agentViewManifestVersion {
		return false
	}
	if filepath.Clean(manifest.SourcePath) != filepath.Clean(opts.InputPath) {
		return false
	}
	if manifest.RecentLimit != opts.RecentLimit {
		return false
	}
	if manifest.IncludeService != opts.IncludeService {
		return false
	}
	if manifest.Since != formatAgentViewSince(opts.Since) {
		return false
	}
	required := []string{
		filepath.Join(opts.OutputDir, "AGENTS.md"),
		filepath.Join(opts.OutputDir, "README.md"),
		filepath.Join(opts.OutputDir, "all-recent.md"),
	}
	for _, path := range required {
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	return true
}

func sourceOffsetAtLineBoundary(inputPath string, offset int64) (bool, error) {
	if offset <= 0 {
		return true, nil
	}
	file, err := os.Open(inputPath)
	if err != nil {
		return false, fmt.Errorf("open input: %w", err)
	}
	defer file.Close()
	if _, err := file.Seek(offset-1, 0); err != nil {
		return false, fmt.Errorf("seek input: %w", err)
	}
	buf := []byte{0}
	if _, err := file.Read(buf); err != nil {
		return false, fmt.Errorf("read input boundary: %w", err)
	}
	return buf[0] == '\n', nil
}

func readAgentViewRecordsFromOffset(opts AgentViewOptions, offset int64) ([]MessageRecord, AgentViewStats, error) {
	file, err := os.Open(opts.InputPath)
	if err != nil {
		return nil, AgentViewStats{}, fmt.Errorf("open input: %w", err)
	}
	defer file.Close()
	if _, err := file.Seek(offset, 0); err != nil {
		return nil, AgentViewStats{}, fmt.Errorf("seek input: %w", err)
	}

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
			return nil, stats, fmt.Errorf("parse input tail line %d: %w", stats.Records, err)
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
		return nil, stats, fmt.Errorf("read input tail: %w", err)
	}
	return records, stats, nil
}

func appendAgentViewDayFiles(outputDir string, records []MessageRecord) error {
	chats := buildChatViews(records)
	for _, chat := range chats {
		for _, topic := range sortedTopics(chat) {
			for _, day := range sortedDaysOldestFirst(topic) {
				dayRecords := topic.Days[day]
				sortRecordsOldestFirst(dayRecords)
				path := filepath.Join(outputDir, "chats", chatSlug(chat.Chat), "topics", topic.Key, day+".md")
				if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
					if err := writeTextFile(path, renderDayMessages(chat, topic, day)); err != nil {
						return err
					}
					continue
				} else if err != nil {
					return fmt.Errorf("stat day file: %w", err)
				}
				if err := appendAgentViewDayLines(path, dayRecords); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func appendAgentViewDayLines(path string, records []MessageRecord) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open day file for append: %w", err)
	}
	defer file.Close()
	writer := bufio.NewWriter(file)
	for _, record := range records {
		fmt.Fprintln(writer, messageLine(record, false, 0))
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush day file: %w", err)
	}
	return nil
}

func applyAgentViewManifestDelta(manifest *agentViewManifest, sourceSize int64, delta AgentViewStats, records []MessageRecord) {
	manifest.SourceSize = sourceSize
	manifest.SourceLines += delta.Records
	manifest.SkippedRecords += delta.Skipped
	manifest.VisibleRecords += len(records)
	manifest.GeneratedAt = time.Now().UTC()
	applyVisibleRecordsToManifest(manifest, records)
	manifest.Recent = limitedRecentRecords(append(manifest.Recent, records...), manifest.RecentLimit)
}

func applyVisibleRecordsToManifest(manifest *agentViewManifest, records []MessageRecord) {
	if manifest.Chats == nil {
		manifest.Chats = make(map[string]*agentViewChatSummary)
	}
	for _, record := range records {
		chatKey := chatSlug(record.Chat)
		chat := manifest.Chats[chatKey]
		if chat == nil {
			chat = &agentViewChatSummary{
				Key:    chatKey,
				Chat:   record.Chat,
				Topics: make(map[string]*agentViewTopicSummary),
			}
			manifest.Chats[chatKey] = chat
		}
		chat.Count++
		updateSummaryDates(&chat.FirstDate, &chat.LastDate, record.Date)

		key := topicKey(record)
		topic := chat.Topics[key]
		if topic == nil {
			topic = &agentViewTopicSummary{
				Key:   key,
				Title: topicTitle(record, key),
				Topic: record.Topic,
				Days:  make(map[string]*agentViewDaySummary),
			}
			chat.Topics[key] = topic
		}
		topic.Count++
		updateSummaryDates(&topic.FirstDate, &topic.LastDate, record.Date)
		dayKey := record.Date.In(moscowLocation).Format("2006-01-02")
		day := topic.Days[dayKey]
		if day == nil {
			day = &agentViewDaySummary{Day: dayKey}
			topic.Days[dayKey] = day
		}
		day.Count++
		updateSummaryDates(&day.FirstDate, &day.LastDate, record.Date)
	}
}

func updateSummaryDates(first, last *time.Time, value time.Time) {
	if value.IsZero() {
		return
	}
	if first.IsZero() || value.Before(*first) {
		*first = value
	}
	if last.IsZero() || value.After(*last) {
		*last = value
	}
}

func limitedRecentRecords(records []MessageRecord, limit int) []MessageRecord {
	if limit <= 0 {
		limit = defaultRecentLimit
	}
	sortRecordsNewestFirst(records)
	if len(records) > limit {
		records = records[:limit]
	}
	out := make([]MessageRecord, len(records))
	copy(out, records)
	return out
}

func writeAgentViewIndexFilesFromManifest(opts AgentViewOptions, manifest agentViewManifest) (int, error) {
	files := 0
	if err := writeTextFile(filepath.Join(opts.OutputDir, "AGENTS.md"), renderAgentViewInstructions(opts)); err != nil {
		return files, err
	}
	files++
	if err := writeTextFile(filepath.Join(opts.OutputDir, "README.md"), renderAgentIndexFromManifest(opts, manifest)); err != nil {
		return files, err
	}
	files++
	if err := writeTextFile(filepath.Join(opts.OutputDir, "all-recent.md"), renderAllRecent(opts, manifest.Recent)); err != nil {
		return files, err
	}
	files++
	for _, chat := range manifestChats(manifest) {
		chatDir := filepath.Join(opts.OutputDir, "chats", chat.Key)
		if err := writeTextFile(filepath.Join(chatDir, "README.md"), renderChatIndexFromManifest(chat)); err != nil {
			return files, err
		}
		files++
		for _, topic := range manifestTopics(chat) {
			topicDir := filepath.Join(chatDir, "topics", topic.Key)
			if err := writeTextFile(filepath.Join(topicDir, "README.md"), renderTopicIndexFromManifest(chat, topic)); err != nil {
				return files, err
			}
			files++
		}
	}
	return files, nil
}

func renderAgentIndexFromManifest(opts AgentViewOptions, manifest agentViewManifest) string {
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
	b.WriteString("- Service messages are skipped by default.\n")
	b.WriteString("- Each visible message keeps `#message_id`; cite facts as `path #message_id`.\n")
	b.WriteString("- Message time is Europe/Moscow local time.\n\n")
	b.WriteString("## Files\n\n")
	b.WriteString(fmt.Sprintf("- Raw merged JSONL: `%s`\n", opts.InputPath))
	b.WriteString("- Latest cross-chat slice: [all-recent.md](all-recent.md)\n")
	b.WriteString("- Agent rules in this generated directory: [AGENTS.md](AGENTS.md)\n")
	b.WriteString(fmt.Sprintf("- Generated at: `%s`\n", manifest.GeneratedAt.In(moscowLocation).Format(time.RFC3339)))
	if manifest.Since != "" {
		b.WriteString(fmt.Sprintf("- Since filter: `%s`\n", manifest.Since))
	}
	b.WriteString("\n")
	b.WriteString("## Chats\n\n")
	b.WriteString("| Chat | Messages | Date Range | Open |\n")
	b.WriteString("| --- | ---: | --- | --- |\n")
	for _, chat := range manifestChats(manifest) {
		b.WriteString(fmt.Sprintf(
			"| %s | %d | %s | [%s](%s) |\n",
			mdCell(displayChat(chat.Chat)),
			chat.Count,
			mdCell(dateRangeFromSummary(chat.FirstDate, chat.LastDate)),
			mdCell(chat.Key),
			mdPath(filepath.ToSlash(filepath.Join("chats", chat.Key, "README.md"))),
		))
	}
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("Total visible messages: `%d`.\n", manifest.VisibleRecords))
	return b.String()
}

func renderChatIndexFromManifest(chat *agentViewChatSummary) string {
	var b strings.Builder
	chatName := displayChat(chat.Chat)
	b.WriteString(fmt.Sprintf("# Chat: %s\n\n", chatName))
	b.WriteString(fmt.Sprintf("- Chat id: `%d` (source lookup only; agents normally do not need it)\n", chat.Chat.ID))
	b.WriteString(fmt.Sprintf("- Messages in view: `%d`\n", chat.Count))
	b.WriteString(fmt.Sprintf("- Date range: `%s`\n", dateRangeFromSummary(chat.FirstDate, chat.LastDate)))
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
	for _, topic := range manifestTopics(chat) {
		b.WriteString(fmt.Sprintf(
			"| %s | %d | %s | [%s](%s) |\n",
			mdCell(topic.Title),
			topic.Count,
			mdCell(dateRangeFromSummary(topic.FirstDate, topic.LastDate)),
			mdCell(topic.Key),
			mdPath(filepath.ToSlash(filepath.Join("topics", topic.Key, "README.md"))),
		))
	}
	return b.String()
}

func renderTopicIndexFromManifest(chat *agentViewChatSummary, topic *agentViewTopicSummary) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# %s / %s\n\n", displayChat(chat.Chat), topic.Title))
	b.WriteString(fmt.Sprintf("- Messages in topic view: `%d`\n", topic.Count))
	b.WriteString(fmt.Sprintf("- Date range: `%s`\n", dateRangeFromSummary(topic.FirstDate, topic.LastDate)))
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
	for _, day := range manifestDays(topic) {
		b.WriteString(fmt.Sprintf(
			"| %s | %d | [%s](%s) |\n",
			day.Day,
			day.Count,
			day.Day,
			mdPath(day.Day+".md"),
		))
	}
	return b.String()
}

func manifestChats(manifest agentViewManifest) []*agentViewChatSummary {
	chats := make([]*agentViewChatSummary, 0, len(manifest.Chats))
	for _, chat := range manifest.Chats {
		chats = append(chats, chat)
	}
	sort.SliceStable(chats, func(i, j int) bool {
		if chats[i].LastDate.Equal(chats[j].LastDate) {
			return displayChat(chats[i].Chat) < displayChat(chats[j].Chat)
		}
		return chats[i].LastDate.After(chats[j].LastDate)
	})
	return chats
}

func manifestTopics(chat *agentViewChatSummary) []*agentViewTopicSummary {
	topics := make([]*agentViewTopicSummary, 0, len(chat.Topics))
	for _, topic := range chat.Topics {
		topics = append(topics, topic)
	}
	sort.SliceStable(topics, func(i, j int) bool {
		if topics[i].LastDate.Equal(topics[j].LastDate) {
			return topics[i].Title < topics[j].Title
		}
		return topics[i].LastDate.After(topics[j].LastDate)
	})
	return topics
}

func manifestDays(topic *agentViewTopicSummary) []*agentViewDaySummary {
	days := make([]*agentViewDaySummary, 0, len(topic.Days))
	for _, day := range topic.Days {
		days = append(days, day)
	}
	sort.SliceStable(days, func(i, j int) bool {
		return days[i].Day > days[j].Day
	})
	return days
}

func sortedDaysOldestFirst(topic *topicView) []string {
	days := make([]string, 0, len(topic.Days))
	for day := range topic.Days {
		days = append(days, day)
	}
	sort.Strings(days)
	return days
}

func dateRangeFromSummary(first, last time.Time) string {
	if first.IsZero() || last.IsZero() {
		return ""
	}
	return first.In(moscowLocation).Format("2006-01-02") + " .. " + last.In(moscowLocation).Format("2006-01-02")
}

func formatAgentViewSince(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func agentViewStatsFromManifest(manifest agentViewManifest) AgentViewStats {
	return AgentViewStats{
		Records: manifest.SourceLines,
		Written: manifest.VisibleRecords,
		Skipped: manifest.SkippedRecords,
		Chats:   len(manifest.Chats),
		Topics:  manifestTopicCount(manifest),
	}
}

func manifestTopicCount(manifest agentViewManifest) int {
	total := 0
	for _, chat := range manifest.Chats {
		total += len(chat.Topics)
	}
	return total
}

func countAffectedDayFiles(records []MessageRecord) int {
	seen := make(map[string]struct{})
	for _, record := range records {
		key := filepath.Join(chatSlug(record.Chat), topicKey(record), record.Date.In(moscowLocation).Format("2006-01-02"))
		seen[key] = struct{}{}
	}
	return len(seen)
}
