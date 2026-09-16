package harvest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Unlike the older sync fake, this models newest-first pages and the old cap.
type pagedIncrementalSource struct {
	newest, stopAfter int
	options           []HistoryOptions
}

func TestRoutineSyncResumesInterruptedRefresh(t *testing.T) {
	opts := incrementalFixture(t)
	opts.History.Start = time.Unix(1, 0)
	source := &pagedIncrementalSource{newest: 260, stopAfter: 1}
	if _, err := RunSync(context.Background(), source, opts); !errors.Is(err, context.Canceled) {
		t.Fatalf("first error=%v", err)
	}
	source.stopAfter = 0
	opts.History.Start = time.Time{}
	result, err := RunSync(context.Background(), source, opts)
	if err != nil || result.Stats.Records != 260 || source.options[1].StartOffsetID != 161 || source.options[1].Start.IsZero() {
		t.Fatalf("resumed=%+v options=%+v err=%v", result, source.options, err)
	}
}

func TestIncrementalRecoversPartialPublicationAndRefusesForeignSuffix(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(fmt.Sprint(foreign), func(t *testing.T) {
			opts := incrementalFixture(t)
			base, err := LoadSyncState(opts.StatePath)
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(record(11, nil))
			if err != nil {
				t.Fatal(err)
			}
			body = append(body, '\n')
			next := base
			next.LastID = 11
			next.Records = 1
			journal := incrementalStage{Chat: opts.Chat, MinID: 10, Base: base, Bytes: int64(len(body)), Added: 1, Ready: true, Stats: HistoryStats{Records: 1, LastID: 11, Complete: true}, Outputs: map[string]int64{opts.StreamPath: 0, opts.MergedPath: 0}, Next: &next}
			mustWriteFile(t, opts.StatePath+".incremental.jsonl", body)
			if err := saveIncrementalStage(opts.StatePath+".incremental.json", journal); err != nil {
				t.Fatal(err)
			}
			tail := body[:len(body)/2]
			if foreign {
				tail = []byte("foreign writer\n")
			}
			mustWriteFile(t, opts.StreamPath, tail)
			mustWriteFile(t, opts.MergedPath, nil)
			source := &fakeHistorySource{}
			result, err := RunSync(context.Background(), source, opts)
			if foreign {
				if err == nil {
					t.Fatal("foreign suffix overwritten")
				}
				saved, _ := LoadSyncState(opts.StatePath)
				if saved.LastID != 10 {
					t.Fatal("unverified checkpoint advanced")
				}
				return
			}
			if err != nil || result.State.LastID != 11 || len(source.seen) != 0 {
				t.Fatalf("publication=%+v err=%v", result, err)
			}
			for _, path := range []string{opts.StreamPath, opts.MergedPath} {
				if rows := readRecords(t, path); len(rows) != 1 || rows[0].MessageID != 11 {
					t.Fatalf("output=%+v", rows)
				}
			}
		})
	}
}

func TestRefreshCapturesEditsAndMissingOlderRecordsOnce(t *testing.T) {
	opts := incrementalFixture(t)
	source := &fakeHistorySource{chat: Chat{ID: 1}, records: []MessageRecord{record(11, nil), record(12, nil)}}
	if _, err := RunSync(context.Background(), source, opts); err != nil {
		t.Fatal(err)
	}
	source.records[0].Text = "updated formula and deadline"
	source.records = append(source.records, record(5, nil))
	opts.History.Start = time.Unix(1, 0)
	result, err := RunSync(context.Background(), source, opts)
	if err != nil || result.Stats.Records != 2 || result.State.LastID != 12 {
		t.Fatalf("refresh=%+v err=%v", result, err)
	}
	records := readRecords(t, opts.MergedPath)
	if len(records) != 4 || !records[2].Revision || records[2].Text != "updated formula and deadline" {
		t.Fatalf("revisions=%+v", records)
	}
	result, err = RunSync(context.Background(), source, opts)
	if err != nil || result.Stats.Records != 0 {
		t.Fatalf("repeat refresh=%+v err=%v", result, err)
	}
	if len(readRecords(t, opts.MergedPath)) != 4 {
		t.Fatal("unchanged messages replayed")
	}
}

func TestMessageContentIgnoresVolatileUIButIncludesTaskChanges(t *testing.T) {
	a := record(1, nil)
	b := a
	b.Chat.UnreadCount, b.Views, b.Chat.TopMessageID = 500, 50, 100
	if messageContent(a) != messageContent(b) {
		t.Fatal("volatile UI treated as edit")
	}
	b.Text = "new deadline"
	if messageContent(a) == messageContent(b) {
		t.Fatal("lost meaningful edit")
	}
	a.Attachments = []Attachment{{Kind: "video", MediaID: "1", TranscriptPath: "old/cache", TranscriptError: "disabled"}, {Kind: "webpage", URL: "https://example.org/lecture.pdf", Title: "Cached preview"}}
	b = a
	b.Attachments = append([]Attachment(nil), a.Attachments...)
	b.Attachments[0].TranscriptPath = "new/cache"
	b.Attachments[0].TranscriptError = ""
	b.Attachments[1].Title = ""
	if messageContent(a) != messageContent(b) {
		t.Fatal("cache paths or link-preview title treated as content revision")
	}
}

func (s *pagedIncrementalSource) DumpHistory(_ context.Context, _ string, opts HistoryOptions, emit func(MessageRecord) error) (Chat, HistoryStats, error) {
	s.options = append(s.options, opts)
	stats := HistoryStats{}
	top := s.newest
	if opts.StartOffsetID > 0 {
		top = opts.StartOffsetID - 1
	}
	for top > opts.MinID {
		oldest := max(opts.MinID+1, top-99)
		if !opts.All && opts.Limit > 0 {
			oldest = max(oldest, top-(opts.Limit-stats.Records)+1)
		}
		for id := oldest; id <= top; id++ {
			if err := emit(record(id, nil)); err != nil {
				return Chat{}, stats, err
			}
			stats.Records++
			if stats.FirstID == 0 || id < stats.FirstID {
				stats.FirstID = id
			}
			stats.LastID = max(stats.LastID, id)
		}
		stats.Batches++
		if opts.Progress != nil {
			if err := opts.Progress(HistoryProgress{Records: stats.Records, Batches: stats.Batches, FirstID: stats.FirstID, LastID: stats.LastID, NextOffsetID: oldest}); err != nil {
				return Chat{}, stats, err
			}
		}
		if s.stopAfter > 0 && stats.Batches == s.stopAfter {
			// The next page was interrupted after a partial write, before fsync.
			if err := emit(record(oldest-1, nil)); err != nil {
				return Chat{}, stats, err
			}
			return Chat{}, stats, context.Canceled
		}
		top = oldest - 1
		if !opts.All && opts.Limit > 0 && stats.Records >= opts.Limit {
			break
		}
	}
	stats.Complete = top <= opts.MinID
	return Chat{ID: 1, TopMessageID: s.newest}, stats, nil
}

func incrementalFixture(t *testing.T) SyncOptions {
	t.Helper()
	dir := t.TempDir()
	opts := SyncOptions{Chat: "study", StreamPath: filepath.Join(dir, "chat.jsonl"), StatePath: filepath.Join(dir, "chat.state.json"), MergedPath: filepath.Join(dir, "merged.jsonl"), History: HistoryOptions{Limit: 100, BatchSize: 100}}
	if err := SaveSyncState(opts.StatePath, SyncState{LastID: 10}); err != nil {
		t.Fatal(err)
	}
	return opts
}

func TestIncrementalDrainsBeyondOldRecordCaps(t *testing.T) {
	for _, count := range []int{250, 6000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			opts := incrementalFixture(t)
			source := &pagedIncrementalSource{newest: count + 10}
			result, err := RunSync(context.Background(), source, opts)
			if err != nil || result.State.LastID != count+10 || result.Stats.Records != count {
				t.Fatalf("range result=%+v err=%v", result, err)
			}
			for _, path := range []string{opts.StreamPath, opts.MergedPath} {
				records := readRecords(t, path)
				if len(records) != count {
					t.Fatalf("%s: got %d, want %d", path, len(records), count)
				}
			}
			second, err := RunSync(context.Background(), source, opts)
			if err != nil || second.Stats.Records != 0 {
				t.Fatalf("second=%+v err=%v", second, err)
			}
		})
	}
}

func TestIncrementalResumesAfterCancellationWithoutSkippingOrDuplicating(t *testing.T) {
	opts := incrementalFixture(t)
	source := &pagedIncrementalSource{newest: 260, stopAfter: 1}
	_, err := RunSync(context.Background(), source, opts)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	state, err := LoadSyncState(opts.StatePath)
	if err != nil || state.LastID != 10 {
		t.Fatalf("premature cursor=%+v err=%v", state, err)
	}
	if _, err := os.Stat(opts.MergedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial range published")
	}
	source.stopAfter = 0
	// A newer message arriving during resume belongs to the following sync.
	source.newest = 261
	result, err := RunSync(context.Background(), source, opts)
	if err != nil || result.State.LastID != 260 || result.Stats.Records != 250 {
		t.Fatalf("resume=%+v err=%v", result, err)
	}
	if source.options[1].StartOffsetID != 161 {
		t.Fatalf("did not resume: %+v", source.options)
	}
	result, err = RunSync(context.Background(), source, opts)
	if err != nil || result.Stats.Records != 1 || result.State.LastID != 261 {
		t.Fatalf("new head=%+v err=%v", result, err)
	}
	for _, path := range []string{opts.StreamPath, opts.MergedPath} {
		records := readRecords(t, path)
		seen := map[int]bool{}
		for _, r := range records {
			if seen[r.MessageID] {
				t.Fatalf("duplicate %d", r.MessageID)
			}
			seen[r.MessageID] = true
		}
		if len(seen) != 251 {
			t.Fatalf("lost records: %d", len(seen))
		}
	}
}
