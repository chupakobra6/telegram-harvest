package harvest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteAgentMarkdownViewSplitsChatsTopicsAndDays(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "messages.jsonl")
	output := filepath.Join(dir, "agent-view")
	writeFile(t, input, strings.Join([]string{
		`{"source":"telegram","chat":{"id":100,"display":"Main Study"},"message_id":1,"date":"2026-05-10T21:00:00Z","sender":{"username":"student"},"kind":"text","text":"basic chat message","reply_to_message_id":99}`,
		`{"source":"telegram","chat":{"id":200,"display":"Forum Study","forum":true},"message_id":2,"date":"2026-05-11T08:00:00Z","sender":{"display":"Teacher"},"kind":"text","topic":{"id":7,"title":"Math"},"thread_top_message_id":7,"text":"homework by Friday","links":["https://example.com/task"],"attachments":[{"kind":"document","file_name":"task.pdf"},{"kind":"webpage","title":"Task page","url":"https://example.com/task"}]}`,
		`{"source":"telegram","chat":{"id":200,"display":"Forum Study","forum":true},"message_id":3,"date":"2026-05-11T09:00:00Z","sender":{"display":"Teacher"},"kind":"text","topic":{"id":8,"title":"Admin"},"thread_top_message_id":8,"text":"admin message"}`,
		`{"source":"telegram","chat":{"id":200,"display":"Forum Study","forum":true},"message_id":4,"date":"2026-05-11T10:00:00Z","sender":{"display":"Teacher"},"kind":"service","topic":{"id":7,"title":"Math"},"thread_top_message_id":7,"text":"[service] pinned"}`,
	}, "\n")+"\n")

	stats, err := WriteAgentMarkdownView(AgentViewOptions{
		InputPath:   input,
		OutputDir:   output,
		RecentLimit: 10,
	})
	if err != nil {
		t.Fatalf("write agent view: %v", err)
	}
	if stats.Records != 4 || stats.Written != 3 || stats.Skipped != 1 || stats.Chats != 2 || stats.Topics != 3 {
		t.Fatalf("unexpected stats: %+v", stats)
	}

	index := readFile(t, filepath.Join(output, "README.md"))
	if !strings.Contains(index, "all-recent.md") || !strings.Contains(index, "chat-200") {
		t.Fatalf("index missing navigation:\n%s", index)
	}
	instructions := readFile(t, filepath.Join(output, "AGENTS.md"))
	if !strings.Contains(instructions, "Do not open `messages.jsonl` for normal extraction") {
		t.Fatalf("agent instructions missing raw JSONL guard:\n%s", instructions)
	}
	recent := readFile(t, filepath.Join(output, "all-recent.md"))
	if !strings.Contains(recent, "Forum Study / Admin") || !strings.Contains(recent, "`#3`") {
		t.Fatalf("recent view missing chat/topic/source ref:\n%s", recent)
	}
	if strings.Contains(recent, "pinned") {
		t.Fatalf("service message leaked into recent view:\n%s", recent)
	}

	mathDay := filepath.Join(output, "chats", "chat-200", "topics", "topic-7", "2026-05-11.md")
	adminDay := filepath.Join(output, "chats", "chat-200", "topics", "topic-8", "2026-05-11.md")
	if _, err := os.Stat(mathDay); err != nil {
		t.Fatalf("expected math day file: %v", err)
	}
	if _, err := os.Stat(adminDay); err != nil {
		t.Fatalf("expected admin day file: %v", err)
	}
	math := readFile(t, mathDay)
	if !strings.Contains(math, "homework by Friday") ||
		!strings.Contains(math, "files: document:task.pdf; webpage:Task page") {
		t.Fatalf("math day missing useful content:\n%s", math)
	}
	if strings.Contains(math, "reply_to_message_id") || strings.Contains(math, "thread_top_message_id") {
		t.Fatalf("raw JSON fields leaked into agent view:\n%s", math)
	}
}

func TestWriteAgentMarkdownViewRefusesInputDirAsOutput(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "messages.jsonl")
	writeFile(t, input, `{"source":"telegram","chat":{"id":100,"display":"Main Study"},"message_id":1,"date":"2026-05-10T21:00:00Z","kind":"text","text":"message"}`+"\n")

	_, err := WriteAgentMarkdownView(AgentViewOptions{
		InputPath: input,
		OutputDir: dir,
	})
	if err == nil {
		t.Fatalf("expected dangerous output dir to be rejected")
	}
}

func TestUpdateAgentMarkdownViewAppendsOnlyNewTail(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "messages.jsonl")
	output := filepath.Join(dir, "agent-view")
	writeFile(t, input, `{"source":"telegram","chat":{"id":200,"display":"Forum Study","forum":true},"message_id":1,"date":"2026-05-11T08:00:00Z","sender":{"display":"Teacher"},"kind":"text","topic":{"id":7,"title":"Math"},"thread_top_message_id":7,"text":"first homework"}`+"\n")

	stats, err := UpdateAgentMarkdownView(AgentViewOptions{
		InputPath:   input,
		OutputDir:   output,
		RecentLimit: 10,
	})
	if err != nil {
		t.Fatalf("initial update: %v", err)
	}
	if stats.Mode != "rebuild" || stats.Written != 1 {
		t.Fatalf("expected initial rebuild, stats=%+v", stats)
	}
	if _, err := os.Stat(filepath.Join(output, agentViewManifestName)); err != nil {
		t.Fatalf("expected manifest: %v", err)
	}

	appendFile(t, input, strings.Join([]string{
		`{"source":"telegram","chat":{"id":200,"display":"Forum Study","forum":true},"message_id":2,"date":"2026-05-11T09:00:00Z","sender":{"display":"Teacher"},"kind":"text","topic":{"id":7,"title":"Math"},"thread_top_message_id":7,"text":"second homework"}`,
		`{"source":"telegram","chat":{"id":200,"display":"Forum Study","forum":true},"message_id":3,"date":"2026-05-11T10:00:00Z","sender":{"display":"Teacher"},"kind":"service","topic":{"id":7,"title":"Math"},"thread_top_message_id":7,"text":"[service] pinned"}`,
	}, "\n")+"\n")

	stats, err = UpdateAgentMarkdownView(AgentViewOptions{
		InputPath:   input,
		OutputDir:   output,
		RecentLimit: 10,
	})
	if err != nil {
		t.Fatalf("incremental update: %v", err)
	}
	if stats.Mode != "incremental" || stats.RawAdded != 2 || stats.VisibleAdded != 1 || stats.Written != 2 || stats.Skipped != 1 {
		t.Fatalf("unexpected incremental stats: %+v", stats)
	}
	dayPath := filepath.Join(output, "chats", "chat-200", "topics", "topic-7", "2026-05-11.md")
	day := readFile(t, dayPath)
	if strings.Count(day, "second homework") != 1 {
		t.Fatalf("expected appended message exactly once:\n%s", day)
	}
	index := readFile(t, filepath.Join(output, "README.md"))
	if !strings.Contains(index, "Total visible messages: `2`") {
		t.Fatalf("index was not refreshed:\n%s", index)
	}

	stats, err = UpdateAgentMarkdownView(AgentViewOptions{
		InputPath:   input,
		OutputDir:   output,
		RecentLimit: 10,
	})
	if err != nil {
		t.Fatalf("noop update: %v", err)
	}
	if stats.Mode != "noop" || stats.RawAdded != 0 || stats.VisibleAdded != 0 {
		t.Fatalf("expected noop stats: %+v", stats)
	}
	day = readFile(t, dayPath)
	if strings.Count(day, "second homework") != 1 {
		t.Fatalf("noop duplicated appended message:\n%s", day)
	}
}

func TestUpdateAgentMarkdownViewFallsBackToRebuildWhenSourceShrinks(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "messages.jsonl")
	output := filepath.Join(dir, "agent-view")
	writeFile(t, input, strings.Join([]string{
		`{"source":"telegram","chat":{"id":200,"display":"Forum Study","forum":true},"message_id":1,"date":"2026-05-11T08:00:00Z","sender":{"display":"Teacher"},"kind":"text","topic":{"id":7,"title":"Math"},"thread_top_message_id":7,"text":"old one"}`,
		`{"source":"telegram","chat":{"id":200,"display":"Forum Study","forum":true},"message_id":2,"date":"2026-05-11T09:00:00Z","sender":{"display":"Teacher"},"kind":"text","topic":{"id":7,"title":"Math"},"thread_top_message_id":7,"text":"old two"}`,
	}, "\n")+"\n")
	if _, err := UpdateAgentMarkdownView(AgentViewOptions{InputPath: input, OutputDir: output, RecentLimit: 10}); err != nil {
		t.Fatalf("initial update: %v", err)
	}

	writeFile(t, input, `{"source":"telegram","chat":{"id":200,"display":"Forum Study","forum":true},"message_id":3,"date":"2026-05-12T08:00:00Z","sender":{"display":"Teacher"},"kind":"text","topic":{"id":7,"title":"Math"},"thread_top_message_id":7,"text":"new only"}`+"\n")
	stats, err := UpdateAgentMarkdownView(AgentViewOptions{InputPath: input, OutputDir: output, RecentLimit: 10})
	if err != nil {
		t.Fatalf("shrink update: %v", err)
	}
	if stats.Mode != "rebuild" || stats.Written != 1 {
		t.Fatalf("expected shrink fallback rebuild, stats=%+v", stats)
	}
	recent := readFile(t, filepath.Join(output, "all-recent.md"))
	if !strings.Contains(recent, "new only") || strings.Contains(recent, "old one") {
		t.Fatalf("fallback rebuild kept stale data:\n%s", recent)
	}
}

func TestUpdateAgentMarkdownViewFallsBackToRebuildWhenManifestVersionChanges(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "messages.jsonl")
	output := filepath.Join(dir, "agent-view")
	writeFile(t, input, `{"source":"telegram","chat":{"id":200,"display":"Forum Study","forum":true},"message_id":1,"date":"2026-05-11T08:00:00Z","sender":{"display":"Teacher"},"kind":"text","topic":{"id":7,"title":"Math"},"thread_top_message_id":7,"text":"message"}`+"\n")
	if _, err := UpdateAgentMarkdownView(AgentViewOptions{InputPath: input, OutputDir: output, RecentLimit: 10}); err != nil {
		t.Fatalf("initial update: %v", err)
	}
	manifestPath := filepath.Join(output, agentViewManifestName)
	manifest := readFile(t, manifestPath)
	manifest = strings.Replace(manifest, `"version": 2`, `"version": 1`, 1)
	writeFile(t, manifestPath, manifest)

	stats, err := UpdateAgentMarkdownView(AgentViewOptions{InputPath: input, OutputDir: output, RecentLimit: 10})
	if err != nil {
		t.Fatalf("version update: %v", err)
	}
	if stats.Mode != "rebuild" {
		t.Fatalf("expected version mismatch rebuild, stats=%+v", stats)
	}
}

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open append %s: %v", path, err)
	}
	defer file.Close()
	if _, err := file.WriteString(content); err != nil {
		t.Fatalf("append %s: %v", path, err)
	}
}
