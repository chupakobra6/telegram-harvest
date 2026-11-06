package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/config"
	"github.com/chupakobra6/telegram-harvest/internal/harvest"
)

func recapTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func TestRecapContinuationUsesOnlyMissingTailAndConfirmedDelivery(t *testing.T) {
	root := filepath.Join(t.TempDir(), "recap")
	start := recapTime("2026-11-04T23:59:30+03:00")
	end := recapTime("2026-11-05T12:00:00+03:00")
	state := recapState{Version: 1, AccountID: 42, Scope: "self"}
	longVoice := strings.TrimSpace(strings.Repeat("полная расшифровка ", 400))
	record := func(id int, when time.Time) harvest.MessageRecord {
		return harvest.MessageRecord{MessageID: id, Date: when, Chat: harvest.Chat{ID: 42, Type: "user", Display: "Избранное"}, Sender: harvest.Sender{Self: true}, Text: "текст"}
	}
	voice := record(1, start)
	voice.Attachments = []harvest.Attachment{{Kind: "voice", Transcript: longVoice}}
	round := record(2, moscowDay(end))
	round.Attachments = []harvest.Attachment{{Kind: "round_video", Transcript: "мысли из кружочка"}}
	source := &fakeDailyRangeDumper{stats: harvest.OutgoingStats{Complete: true}, records: []harvest.MessageRecord{record(0, start.Add(-time.Second)), voice, round, record(3, end)}}
	prepare := func(end time.Time) error {
		return prepareRecap(context.Background(), root, &state, source, harvest.HistoryOptions{}, dailyOptions{}, nil, start, end, "asr-v1", nil, io.Discard)
	}
	if err := prepare(end); err != nil {
		t.Fatal(err)
	}
	if source.calls != 1 || !source.opts.Start.Equal(start) || !source.opts.End.Equal(end) || !state.Through.IsZero() {
		t.Fatalf("initial range/delivery: calls=%d range=%s..%s state=%+v", source.calls, source.opts.Start, source.opts.End, state)
	}
	assertRecapIDs(t, root, state.Prepared, []int{1, 2})
	oldID := state.Prepared.ID
	text, err := os.ReadFile(filepath.Join(root, oldID, harvest.DailyLatestCatchupFilename))
	if err != nil || !strings.Contains(string(text), longVoice) || strings.Contains(string(text), "[обрезано]") || !strings.Contains(string(text), end.Format(time.RFC3339)) {
		t.Fatalf("recap handoff lost range/full voice: %v", err)
	}
	// Source preparation is not delivery. A subsequent request still starts at
	// the original boundary and only collects its missing tail.
	nextEnd := end.Add(time.Hour)
	source.records = append(source.records, record(4, end.Add(time.Minute)), record(5, nextEnd))
	resolvedStart, _, err := recapBounds(state, "", "", nextEnd)
	if err != nil || !resolvedStart.Equal(start) {
		t.Fatalf("unissued result advanced start: %s, %v", resolvedStart, err)
	}
	if err := prepare(nextEnd); err != nil {
		t.Fatal(err)
	}
	if source.calls != 2 || !source.opts.Start.Equal(end) || !state.Through.IsZero() {
		t.Fatalf("retry rescanned covered prefix or confirmed delivery: %+v", source.opts)
	}
	assertRecapIDs(t, root, state.Prepared, []int{1, 2, 3, 4})
	if _, err := os.Stat(filepath.Join(root, oldID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("superseded source cache accumulated")
	}
	// Exact retry needs no Telegram collection at all.
	if err := prepare(nextEnd); err != nil || source.calls != 2 {
		t.Fatalf("exact retry recollected: calls=%d err=%v", source.calls, err)
	}
	if err := confirmRecapDelivery(root, &state, state.Prepared.ID, ""); err == nil {
		t.Fatal("unobserved delivery accepted")
	}
	if err := confirmRecapDelivery(root, &state, oldID, "codex://chat/result"); err == nil {
		t.Fatal("stale source ID accepted")
	}
	if err := confirmRecapDelivery(root, &state, state.Prepared.ID, "codex://chat/result"); err != nil {
		t.Fatal(err)
	}
	if err := saveRecapState(filepath.Join(root, "state.json"), state); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadRecapState(filepath.Join(root, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	resolvedStart, _, err = recapBounds(loaded, "", "", nextEnd.Add(time.Hour))
	if err != nil || !resolvedStart.Equal(nextEnd) {
		t.Fatalf("next recap did not resume exact delivered boundary: %s, %v", resolvedStart, err)
	}
}

func TestRecapFailureAndVoiceGapNeverAdvanceDelivery(t *testing.T) {
	root := filepath.Join(t.TempDir(), "recap")
	start := recapTime("2026-11-05T16:10:12+03:00")
	end := start.Add(time.Hour)
	state := recapState{Version: 1, AccountID: 42, Scope: "self", Through: start, DeliveryRef: "prior-result"}
	source := &fakeDailyRangeDumper{stats: harvest.OutgoingStats{Complete: true}, records: []harvest.MessageRecord{{MessageID: 1, Date: start, Text: "доказанный префикс"}}}
	prepare := func() error {
		return prepareRecap(context.Background(), root, &state, source, harvest.HistoryOptions{}, dailyOptions{}, nil, start, end, "asr-v1", nil, io.Discard)
	}
	if err := prepare(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(root, "state.json"))
	source.err = context.Canceled
	end = end.Add(time.Hour)
	if err := prepare(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(root, "state.json"))
	if string(after) != string(before) {
		t.Fatal("cancel altered prepared or confirmed state")
	}
	source.err = nil
	source.records = []harvest.MessageRecord{{MessageID: 8, Date: end.Add(-time.Minute), Chat: harvest.Chat{ID: 42}, Attachments: []harvest.Attachment{{Kind: "voice", DownloadError: "unavailable"}}}}
	if err := prepare(); err == nil || state.Prepared.Ready || !state.Through.Equal(start) {
		t.Fatalf("voice gap treated as ready/delivered: %+v, %v", state, err)
	}
	if err := confirmRecapDelivery(root, &state, state.Prepared.ID, "some-chat-result"); err == nil {
		t.Fatal("voice gap accepted for confirmation")
	}
	source.records[0].Attachments[0] = harvest.Attachment{Kind: "voice", Transcript: "восстановлено"}
	if err := prepare(); err != nil || !source.opts.Start.Equal(end.Add(-time.Minute)) || !state.Prepared.Ready {
		t.Fatalf("gap repair skipped unproven range: start=%s err=%v", source.opts.Start, err)
	}
	assertRecapIDs(t, root, state.Prepared, []int{1, 8})
}

func TestRecapPreservesAcceptedNoSpeechAfterASRCacheEviction(t *testing.T) {
	root := t.TempDir()
	start := recapTime("2026-11-05T10:00:00+03:00")
	end := start.Add(time.Hour)
	empty := filepath.Join(root, "no-speech.txt")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	state := recapState{Version: 1, AccountID: 42, Scope: "self"}
	source := &fakeDailyRangeDumper{stats: harvest.OutgoingStats{Complete: true}, records: []harvest.MessageRecord{{MessageID: 1, Date: start, Attachments: []harvest.Attachment{{Kind: "voice", TranscriptPath: empty}}}}}
	prepare := func() error {
		return prepareRecap(context.Background(), root, &state, source, harvest.HistoryOptions{}, dailyOptions{}, nil, start, end, "v1", nil, io.Discard)
	}
	if err := prepare(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(empty); err != nil {
		t.Fatal(err)
	}
	end = end.Add(time.Hour)
	if err := prepare(); err != nil || !state.Prepared.Ready || !source.opts.Start.Equal(start.Add(time.Hour)) {
		t.Fatalf("accepted no-speech proof lost: %v", err)
	}
}

func TestRecapCacheInvalidationAndRangeGuards(t *testing.T) {
	for _, change := range []string{"asr-options", "source-bytes", "source-missing"} {
		t.Run(change, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "recap")
			start := recapTime("2026-11-05T10:00:00+03:00")
			end := start.Add(time.Hour)
			state := recapState{Version: 1, AccountID: 42, Scope: "self"}
			source := &fakeDailyRangeDumper{stats: harvest.OutgoingStats{Complete: true}}
			prepare := func(fp string) error {
				return prepareRecap(context.Background(), root, &state, source, harvest.HistoryOptions{}, dailyOptions{}, nil, start, end, fp, nil, io.Discard)
			}
			if err := prepare("v1"); err != nil {
				t.Fatal(err)
			}
			fp := "v1"
			path := filepath.Join(root, state.Prepared.ID, "2026-11-05.jsonl")
			switch change {
			case "asr-options":
				fp = "v2"
			case "source-bytes":
				if err := os.WriteFile(path, []byte("altered"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "source-missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if err := prepare(fp); err != nil || source.calls != 2 || !source.opts.Start.Equal(start) {
				t.Fatalf("stale cache reused: calls=%d start=%s err=%v", source.calls, source.opts.Start, err)
			}
		})
	}
	now := recapTime("2026-11-05T16:00:00+03:00")
	for _, pair := range [][2]string{{"", ""}, {now.Format(time.RFC3339), ""}, {"2026-11-05", now.Add(time.Second).Format(time.RFC3339)}} {
		if _, _, err := recapBounds(recapState{}, pair[0], pair[1], now); err == nil {
			t.Fatalf("invalid/unproven range accepted: %v", pair)
		}
	}
	// Ordinary catch-up still excludes today, reuses existing completed reports,
	// and ignores the recap cache rather than deriving its cursor from it.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "2026-11-03.md"), []byte("immutable"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := buildDailyCatchupPlan(config.Config{StateDir: t.TempDir()}, dir, "", now)
	if err != nil || len(plan.Jobs) != 1 || plan.Jobs[0].Date != "2026-11-04" {
		t.Fatalf("ordinary catch-up changed: %+v %v", plan, err)
	}
}

func TestRecapMediaPolicyAndEmptySpeech(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "no-speech.txt")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	records := []harvest.MessageRecord{{Attachments: []harvest.Attachment{
		{Kind: "voice", TranscriptPath: empty},
		{Kind: "video", NoAudio: true},
		{Kind: "video", TranscriptError: "skipped: generic video is disabled"},
		{Kind: "photo", DownloadError: "not requested"},
	}}}
	if gaps := recapMediaGaps(records); len(gaps) != 0 {
		t.Fatalf("valid no-speech/policy skip marked as gap: %v", gaps)
	}
	records[0].Attachments = []harvest.Attachment{{Kind: "round_video", TranscriptError: "skipped: size limit"}, {Kind: "audio"}}
	if gaps := recapMediaGaps(records); len(gaps) != 2 {
		t.Fatalf("unresolved speech escaped acceptance: %v", gaps)
	}
}

func assertRecapIDs(t *testing.T, root string, bundle *recapBundle, want []int) {
	t.Helper()
	records, err := loadRecapRecords(root, bundle)
	if err != nil {
		t.Fatal(err)
	}
	var got []int
	for _, record := range records {
		got = append(got, record.MessageID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("source messages = %v, want %v", got, want)
	}
}
