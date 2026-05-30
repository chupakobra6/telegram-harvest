package harvest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteCompactTOONSkipsServiceAndSortsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "messages.jsonl")
	output := filepath.Join(dir, "messages.toon")
	writeFile(t, input, strings.Join([]string{
		`{"source":"telegram","chat":{"id":2,"display":"Study"},"message_id":10,"date":"2026-05-10T10:00:00Z","sender":{"username":"student"},"kind":"text","text":"old"}`,
		`{"source":"telegram","chat":{"id":2,"display":"Study"},"message_id":11,"date":"2026-05-11T10:00:00Z","sender":{"display":"Teacher"},"kind":"service","text":"[service] pinned"}`,
		`{"source":"telegram","chat":{"id":2,"display":"Study"},"message_id":12,"date":"2026-05-12T10:00:00Z","sender":{"username":"teacher"},"kind":"text","text":"new"}`,
	}, "\n")+"\n")

	stats, err := WriteCompactTOON(CompactOptions{
		InputPath:  input,
		OutputPath: output,
	})
	if err != nil {
		t.Fatalf("write compact: %v", err)
	}
	if stats.Records != 3 || stats.Written != 2 || stats.Skipped != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	content := readFile(t, output)
	if !strings.Contains(content, "messages[2|]{date|chat_id|chat|topic_id|topic|msg_id|reply_to|kind|from|text|links|files}:") {
		t.Fatalf("missing compact table header:\n%s", content)
	}
	if strings.Contains(content, "pinned") {
		t.Fatalf("service message leaked into compact output:\n%s", content)
	}
	newIndex := strings.Index(content, "|12||text|@teacher|new|")
	oldIndex := strings.Index(content, "|10||text|@student|old|")
	if newIndex < 0 || oldIndex < 0 || newIndex > oldIndex {
		t.Fatalf("expected newest message before old message:\n%s", content)
	}
}

func TestWriteCompactTOONFiltersSinceEscapesTextAndLimits(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "messages.jsonl")
	output := filepath.Join(dir, "messages.toon")
	writeFile(t, input, strings.Join([]string{
		`{"source":"telegram","chat":{"id":2,"display":"Study"},"message_id":10,"date":"2026-05-10T10:00:00Z","sender":{"display":"Student"},"kind":"text","text":"too old"}`,
		`{"source":"telegram","chat":{"id":2,"display":"Study"},"message_id":11,"date":"2026-05-11T10:00:00Z","sender":{"display":"Student"},"kind":"text","text":"line one\nline two | needs quote","links":["https://example.com/a"],"attachments":[{"kind":"document","file_name":"task.pdf"}]}`,
		`{"source":"telegram","chat":{"id":2,"display":"Study"},"message_id":12,"date":"2026-05-12T10:00:00Z","sender":{"display":"Student"},"kind":"text","text":"newer"}`,
	}, "\n")+"\n")

	since := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	stats, err := WriteCompactTOON(CompactOptions{
		InputPath:  input,
		OutputPath: output,
		Since:      since,
		Limit:      1,
	})
	if err != nil {
		t.Fatalf("write compact: %v", err)
	}
	if stats.Records != 3 || stats.Written != 1 || stats.Skipped != 2 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	content := readFile(t, output)
	if strings.Contains(content, "too old") {
		t.Fatalf("since filter did not drop old record:\n%s", content)
	}
	if strings.Contains(content, "line one") {
		t.Fatalf("limit did not keep only newest record:\n%s", content)
	}
	if !strings.Contains(content, "|12||text|Student|newer|") {
		t.Fatalf("newest record missing:\n%s", content)
	}
}

func TestWriteCompactTOONIncludesServiceWhenRequested(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "messages.jsonl")
	output := filepath.Join(dir, "messages.toon")
	writeFile(t, input, `{"source":"telegram","chat":{"id":2,"display":"Study"},"message_id":10,"date":"2026-05-10T10:00:00Z","kind":"service","text":"[service] message pinned"}`+"\n")

	stats, err := WriteCompactTOON(CompactOptions{
		InputPath:      input,
		OutputPath:     output,
		IncludeService: true,
	})
	if err != nil {
		t.Fatalf("write compact: %v", err)
	}
	if stats.Written != 1 {
		t.Fatalf("expected service record to be written, stats=%+v", stats)
	}
	content := readFile(t, output)
	if !strings.Contains(content, "message pinned") {
		t.Fatalf("service record missing:\n%s", content)
	}
}

func TestWriteCompactTOONRejectsBadInputs(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "messages.toon")
	if _, err := WriteCompactTOON(CompactOptions{OutputPath: output}); err == nil {
		t.Fatalf("expected missing input error")
	}
	input := filepath.Join(dir, "messages.jsonl")
	writeFile(t, input, "{broken\n")
	if _, err := WriteCompactTOON(CompactOptions{InputPath: input, OutputPath: output}); err == nil {
		t.Fatalf("expected parse error")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}
