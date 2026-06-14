package harvest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeHistorySource struct {
	chat    Chat
	records []MessageRecord
	seen    []HistoryOptions
}

func (f *fakeHistorySource) DumpHistory(ctx context.Context, chat string, opts HistoryOptions, emit func(MessageRecord) error) (Chat, HistoryStats, error) {
	f.seen = append(f.seen, opts)
	stats := HistoryStats{Batches: 1, Complete: true}
	for _, record := range f.records {
		if opts.TopicID > 0 {
			if record.Topic == nil || record.Topic.ID != opts.TopicID {
				continue
			}
		}
		if opts.MinID > 0 && record.MessageID <= opts.MinID {
			continue
		}
		if err := emit(record); err != nil {
			return Chat{}, HistoryStats{}, err
		}
		stats.Records++
		if stats.FirstID == 0 || record.MessageID < stats.FirstID {
			stats.FirstID = record.MessageID
		}
		if record.MessageID > stats.LastID {
			stats.LastID = record.MessageID
		}
	}
	if opts.Progress != nil {
		if err := opts.Progress(HistoryProgress{
			BatchRecords: stats.Records,
			Records:      stats.Records,
			FirstID:      stats.FirstID,
			LastID:       stats.LastID,
			Batches:      stats.Batches,
			NextOffsetID: stats.FirstID,
			Done:         stats.Complete,
		}); err != nil {
			return Chat{}, HistoryStats{}, err
		}
	}
	return f.chat, stats, nil
}

func TestRunSyncResetAllTruncatesStreamAndState(t *testing.T) {
	dir := t.TempDir()
	streamPath := filepath.Join(dir, "chat.jsonl")
	statePath := filepath.Join(dir, "chat.state.json")
	mergedPath := filepath.Join(dir, "messages.jsonl")
	mustWriteFile(t, streamPath, []byte("old-stream\n"))
	mustWriteFile(t, mergedPath, []byte("old-merged\n"))
	if err := SaveSyncState(statePath, SyncState{LastID: 99, Records: 9}); err != nil {
		t.Fatalf("save old state: %v", err)
	}

	source := &fakeHistorySource{
		chat: Chat{ID: 1, Type: "supergroup", Display: "Study"},
		records: []MessageRecord{
			record(1, nil),
			record(2, nil),
		},
	}
	now := time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC)

	result, err := RunSync(context.Background(), source, SyncOptions{
		Chat:        "study",
		StreamPath:  streamPath,
		StatePath:   statePath,
		MergedPath:  mergedPath,
		History:     HistoryOptions{All: true, BatchSize: 100},
		Reset:       true,
		ResetMerged: true,
		Now:         func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("run sync: %v", err)
	}
	if len(source.seen) != 1 || source.seen[0].MinID != 0 {
		t.Fatalf("expected reset sync to ignore old last id, seen=%+v", source.seen)
	}
	if result.State.LastID != 2 || result.State.Records != 2 || !result.State.LastSyncAt.Equal(now) {
		t.Fatalf("unexpected state: %+v", result.State)
	}
	if lines := readLines(t, streamPath); len(lines) != 2 || strings.Contains(strings.Join(lines, "\n"), "old-stream") {
		t.Fatalf("stream was not truncated correctly: %q", lines)
	}
	if lines := readLines(t, mergedPath); len(lines) != 2 || strings.Contains(strings.Join(lines, "\n"), "old-merged") {
		t.Fatalf("merged stream was not truncated correctly: %q", lines)
	}
}

func TestRunSyncIncrementalUsesSavedLastID(t *testing.T) {
	dir := t.TempDir()
	streamPath := filepath.Join(dir, "chat.jsonl")
	statePath := filepath.Join(dir, "chat.state.json")
	mergedPath := filepath.Join(dir, "messages.jsonl")
	mustWriteFile(t, streamPath, []byte(`{"message_id":10}`+"\n"))
	if err := SaveSyncState(statePath, SyncState{LastID: 10, Records: 1}); err != nil {
		t.Fatalf("save old state: %v", err)
	}

	source := &fakeHistorySource{
		chat: Chat{ID: 1, Type: "supergroup", Display: "Study"},
		records: []MessageRecord{
			record(9, nil),
			record(10, nil),
			record(11, nil),
			record(12, nil),
		},
	}
	result, err := RunSync(context.Background(), source, SyncOptions{
		Chat:       "study",
		StreamPath: streamPath,
		StatePath:  statePath,
		MergedPath: mergedPath,
		History:    HistoryOptions{Limit: 100, BatchSize: 50},
	})
	if err != nil {
		t.Fatalf("run sync: %v", err)
	}
	if got := source.seen[0].MinID; got != 10 {
		t.Fatalf("expected MinID from saved state, got %d", got)
	}
	if result.State.LastID != 12 || result.State.Records != 3 {
		t.Fatalf("unexpected state: %+v", result.State)
	}
	if lines := readLines(t, streamPath); len(lines) != 3 {
		t.Fatalf("expected old line plus two new records, got %d lines: %q", len(lines), lines)
	}
	if records := readRecords(t, mergedPath); len(records) != 2 || records[0].MessageID != 11 || records[1].MessageID != 12 {
		t.Fatalf("unexpected merged records: %+v", records)
	}
}

func TestRunSyncIncrementalRecoversStaleCheckpointRange(t *testing.T) {
	dir := t.TempDir()
	streamPath := filepath.Join(dir, "chat.jsonl")
	statePath := filepath.Join(dir, "chat.state.json")
	mergedPath := filepath.Join(dir, "messages.jsonl")
	mustWriteFile(t, streamPath, []byte(`{"message_id":407584}`+"\n"))
	if err := SaveSyncState(statePath, SyncState{
		Chat:    Chat{ID: 1, Type: "basic_group", Display: "Study", TopMessageID: 11001},
		LastID:  407584,
		Records: 3428,
		Backfill: &Backfill{
			Complete: true,
			LatestID: 10763,
			OldestID: 6960,
			Records:  3428,
		},
	}); err != nil {
		t.Fatalf("save stale state: %v", err)
	}

	source := &fakeHistorySource{
		chat: Chat{ID: 1, Type: "basic_group", Display: "Study", TopMessageID: 11001},
		records: []MessageRecord{
			record(10763, nil),
			record(10764, nil),
			record(10800, nil),
			record(11001, nil),
		},
	}
	result, err := RunSync(context.Background(), source, SyncOptions{
		Chat:       "study",
		StreamPath: streamPath,
		StatePath:  statePath,
		MergedPath: mergedPath,
		History:    HistoryOptions{Limit: 100, BatchSize: 50},
	})
	if err != nil {
		t.Fatalf("run sync: %v", err)
	}
	if got := source.seen[0].MinID; got != 10763 {
		t.Fatalf("expected recovery MinID from completed backfill, got %d", got)
	}
	if result.State.LastID != 11001 || result.State.Records != 3431 {
		t.Fatalf("unexpected recovered state: %+v", result.State)
	}
	if records := readRecords(t, mergedPath); len(records) != 3 || records[0].MessageID != 10764 || records[2].MessageID != 11001 {
		t.Fatalf("unexpected recovered merged records: %+v", records)
	}
}

func TestRunSyncAllResumesIncompleteBackfill(t *testing.T) {
	dir := t.TempDir()
	streamPath := filepath.Join(dir, "chat.jsonl")
	statePath := filepath.Join(dir, "chat.state.json")
	mustWriteFile(t, streamPath, []byte(`{"message_id":90}`+"\n"))
	if err := SaveSyncState(statePath, SyncState{
		LastID:  0,
		Records: 1,
		Backfill: &Backfill{
			Active:       true,
			NextOffsetID: 90,
			LatestID:     120,
			OldestID:     90,
			Records:      1,
			Batches:      1,
		},
	}); err != nil {
		t.Fatalf("save active state: %v", err)
	}

	source := &fakeHistorySource{
		chat: Chat{ID: 1, Type: "supergroup", Display: "Study"},
		records: []MessageRecord{
			record(70, nil),
			record(80, nil),
		},
	}
	result, err := RunSync(context.Background(), source, SyncOptions{
		Chat:       "study",
		StreamPath: streamPath,
		StatePath:  statePath,
		History:    HistoryOptions{All: true, BatchSize: 50},
	})
	if err != nil {
		t.Fatalf("run sync: %v", err)
	}
	if got := source.seen[0].StartOffsetID; got != 90 {
		t.Fatalf("expected resume offset 90, got %d", got)
	}
	if result.State.Backfill == nil || !result.State.Backfill.Complete {
		t.Fatalf("expected completed backfill, got %+v", result.State.Backfill)
	}
	if result.State.LastID != 120 || result.State.Records != 3 {
		t.Fatalf("unexpected resumed state: %+v", result.State)
	}
}

func TestRunSyncTopicKeepsTopicSeparated(t *testing.T) {
	dir := t.TempDir()
	streamPath := filepath.Join(dir, "topic-77.jsonl")
	statePath := filepath.Join(dir, "topic-77.state.json")
	topic := Topic{ID: 77, Title: "Seminars"}
	otherTopic := Topic{ID: 88, Title: "General"}
	source := &fakeHistorySource{
		chat: Chat{ID: 1, Type: "supergroup", Display: "Study", Forum: true},
		records: []MessageRecord{
			record(80, &topic),
			record(81, &otherTopic),
			record(82, &topic),
		},
	}

	result, err := RunSync(context.Background(), source, SyncOptions{
		Chat:       "study",
		StreamPath: streamPath,
		StatePath:  statePath,
		History: HistoryOptions{
			All:        true,
			TopicID:    77,
			TopicTitle: "Seminars",
		},
		Reset: true,
	})
	if err != nil {
		t.Fatalf("run sync: %v", err)
	}
	if got := source.seen[0].TopicID; got != 77 {
		t.Fatalf("expected topic id passed to source, got %d", got)
	}
	if result.State.Topic == nil || result.State.Topic.ID != 77 || result.State.Topic.Title != "Seminars" {
		t.Fatalf("unexpected topic state: %+v", result.State.Topic)
	}
	records := readRecords(t, streamPath)
	if len(records) != 2 {
		t.Fatalf("expected only selected topic records, got %d: %+v", len(records), records)
	}
	for _, record := range records {
		if record.Topic == nil || record.Topic.ID != 77 || record.ThreadTopMessageID != 77 {
			t.Fatalf("record lost topic separation: %+v", record)
		}
	}
}

func TestRunSyncRejectsUnsafeStateTransitions(t *testing.T) {
	dir := t.TempDir()
	source := &fakeHistorySource{chat: Chat{ID: 1, Type: "supergroup", Display: "Study"}}
	streamPath := filepath.Join(dir, "chat.jsonl")
	statePath := filepath.Join(dir, "chat.state.json")
	mergedPath := filepath.Join(dir, "messages.jsonl")

	_, err := RunSync(context.Background(), source, SyncOptions{
		Chat:        "study",
		StreamPath:  streamPath,
		StatePath:   statePath,
		MergedPath:  mergedPath,
		History:     HistoryOptions{All: true},
		ResetMerged: true,
	})
	if err == nil || !strings.Contains(err.Error(), "requires --reset") {
		t.Fatalf("expected full sync reset error, got %v", err)
	}

	if err := SaveSyncState(statePath, SyncState{
		Backfill: &Backfill{Active: true, NextOffsetID: 100},
	}); err != nil {
		t.Fatalf("save active backfill: %v", err)
	}
	_, err = RunSync(context.Background(), source, SyncOptions{
		Chat:       "study",
		StreamPath: streamPath,
		StatePath:  statePath,
		History:    HistoryOptions{Limit: 10},
	})
	if err == nil || !strings.Contains(err.Error(), "incomplete full backfill") {
		t.Fatalf("expected blocked incremental error, got %v", err)
	}
}

func TestRunSyncValidatesRequiredInputs(t *testing.T) {
	dir := t.TempDir()
	source := &fakeHistorySource{}
	valid := SyncOptions{
		Chat:       "study",
		StreamPath: filepath.Join(dir, "chat.jsonl"),
		StatePath:  filepath.Join(dir, "chat.state.json"),
	}
	cases := []struct {
		name   string
		source HistorySource
		opts   SyncOptions
		want   string
	}{
		{"missing source", nil, valid, "history source is required"},
		{"missing chat", source, SyncOptions{StreamPath: valid.StreamPath, StatePath: valid.StatePath}, "chat is required"},
		{"missing stream", source, SyncOptions{Chat: valid.Chat, StatePath: valid.StatePath}, "stream path is required"},
		{"missing state", source, SyncOptions{Chat: valid.Chat, StreamPath: valid.StreamPath}, "state path is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := RunSync(context.Background(), tc.source, tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
}

func TestLoadSyncStateReportsInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	mustWriteFile(t, path, []byte("{broken"))
	if _, err := LoadSyncState(path); err == nil {
		t.Fatalf("expected invalid state error")
	}
}

func record(id int, topic *Topic) MessageRecord {
	rec := MessageRecord{
		Source:    "telegram",
		Chat:      Chat{ID: 1, Type: "supergroup", Display: "Study"},
		MessageID: id,
		Date:      time.Unix(int64(id), 0).UTC(),
		Kind:      "text",
		Text:      "message",
	}
	if topic != nil {
		rec.Topic = topic
		rec.ThreadTopMessageID = topic.ID
	}
	return rec
}

func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func readRecords(t *testing.T, path string) []MessageRecord {
	t.Helper()
	lines := readLines(t, path)
	records := make([]MessageRecord, 0, len(lines))
	for _, line := range lines {
		var record MessageRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode JSONL record: %v", err)
		}
		records = append(records, record)
	}
	return records
}
