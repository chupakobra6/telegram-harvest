package mtproto

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/chupakobra6/telegram-harvest/internal/transcribe"
	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestNumericPeerCandidatesSupportsTelegramChannelIDs(t *testing.T) {
	got := numericPeerCandidates(-1001234567890)
	want := []int64{-1001234567890, 1001234567890, 1234567890}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidate[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestNormalizeHistoryOptionsAndBatchLimits(t *testing.T) {
	opts := normalizeHistoryOptions(harvest.HistoryOptions{BatchSize: 500})
	if opts.Limit != 100 || opts.BatchSize != 100 || opts.MaxBatches != 0 {
		t.Fatalf("unexpected normalized opts: %+v", opts)
	}
	if got := initialHistoryCapacity(opts); got != 100 {
		t.Fatalf("initial capacity = %d", got)
	}
	if !shouldContinueHistory(opts, 99, 200) {
		t.Fatalf("expected history to continue before limits")
	}
	if shouldContinueHistory(opts, 100, 0) {
		t.Fatalf("expected history to stop at record limit")
	}
	capped := normalizeHistoryOptions(harvest.HistoryOptions{Limit: 1000, BatchSize: 100, MaxBatches: 2})
	if shouldContinueHistory(capped, 10, 2) {
		t.Fatalf("expected history to stop at batch limit")
	}
	if got := nextBatchLimit(opts, 60); got != 40 {
		t.Fatalf("next batch limit = %d", got)
	}

	all := normalizeHistoryOptions(harvest.HistoryOptions{All: true})
	if all.Limit != 0 || all.MaxBatches != 0 || all.BatchSize != 100 {
		t.Fatalf("unexpected all-history opts: %+v", all)
	}
}

func TestHistoryTimeBounds(t *testing.T) {
	start := time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC)
	opts := harvest.HistoryOptions{Start: start, End: end}

	before := tg.Message{Date: int(start.Add(-time.Second).Unix())}
	if !historyMessageBeforeStart(&before, opts) {
		t.Fatalf("expected message before start to stop history scan")
	}
	atStart := harvest.MessageRecord{Date: start}
	if !historyRecordInTimeRange(atStart, opts) {
		t.Fatalf("expected start boundary to be inclusive")
	}
	atEnd := harvest.MessageRecord{Date: end}
	if historyRecordInTimeRange(atEnd, opts) {
		t.Fatalf("expected end boundary to be exclusive")
	}
	inside := harvest.MessageRecord{Date: end.Add(-time.Second)}
	if !historyRecordInTimeRange(inside, opts) {
		t.Fatalf("expected record before end to be included")
	}
}

func TestDailyAdditionalSenderIsScopedToConfiguredChat(t *testing.T) {
	opts := harvest.OutgoingRangeOptions{
		AdditionalSenderIDsByChat: map[int64][]int64{
			3740223926: {8718303786},
		},
	}
	if !dailyHasAdditionalSenders(opts, 3740223926) {
		t.Fatal("configured chat should use daily history scan")
	}
	if dailyHasAdditionalSenders(opts, 123) {
		t.Fatal("unconfigured chat should keep outgoing search")
	}
	if !dailyAdditionalSenderAllowed(opts, 3740223926, 8718303786) {
		t.Fatal("configured sender should be included")
	}
	if dailyAdditionalSenderAllowed(opts, 3740223926, 42) {
		t.Fatal("other sender in configured chat should be excluded")
	}
	if dailyAdditionalSenderAllowed(opts, 123, 8718303786) {
		t.Fatal("configured sender should be excluded from other chats")
	}

	start := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	opts.Start = start
	opts.End = start.AddDate(0, 0, 1)
	target := resolvedTarget{Chat: harvest.Chat{ID: 3740223926, Display: "Haru 🌸"}}
	entities := peer.NewEntities(map[int64]*tg.User{
		8718303786: {ID: 8718303786, FirstName: "Трекмейт", Bot: true},
		42:         {ID: 42, FirstName: "Другой участник"},
	}, nil, nil)
	session := &Session{}

	trackmate, ok := session.normalizeOutgoingDayRecord(context.Background(), &tg.Message{
		ID:      1,
		Date:    int(start.Add(time.Hour).Unix()),
		FromID:  &tg.PeerUser{UserID: 8718303786},
		Message: "daily summary",
	}, target, entities, nil, opts)
	if !ok || trackmate.Sender.ID != 8718303786 || !trackmate.Sender.Bot {
		t.Fatalf("Trackmate record was not included: ok=%t record=%+v", ok, trackmate)
	}

	if _, ok := session.normalizeOutgoingDayRecord(context.Background(), &tg.Message{
		ID:      2,
		Date:    int(start.Add(2 * time.Hour).Unix()),
		FromID:  &tg.PeerUser{UserID: 42},
		Message: "not part of daily",
	}, target, entities, nil, opts); ok {
		t.Fatal("other Haru participant should be excluded from daily")
	}
}

func TestPlanDailyDialogScanSkipsOnlyProvenUnchangedDialogs(t *testing.T) {
	checkpoint := harvest.DailyDialogCheckpointDecision{
		Enabled: true,
		Dialogs: map[string]harvest.DailyDialogHead{
			harvest.DailyDialogHeadKey("user", 1): {
				ChatID:            1,
				ChatType:          "user",
				TopMessageID:      10,
				VerifiedMessageID: 10,
				HeadFullyVerified: true,
			},
			harvest.DailyDialogHeadKey("supergroup", 2): {
				ChatID:            2,
				ChatType:          "supergroup",
				TopMessageID:      20,
				VerifiedMessageID: 20,
				HeadFullyVerified: true,
			},
		},
	}
	unchanged := planDailyDialogScan(harvest.Chat{ID: 1, Type: "user", TopMessageID: 10}, checkpoint)
	if unchanged.Kind != dailyDialogUnchanged || unchanged.MinID != 0 {
		t.Fatalf("unchanged plan = %+v", unchanged)
	}
	changed := planDailyDialogScan(harvest.Chat{ID: 2, Type: "supergroup", TopMessageID: 25}, checkpoint)
	if changed.Kind != dailyDialogChanged || changed.MinID != 20 {
		t.Fatalf("changed plan = %+v", changed)
	}
	decreased := planDailyDialogScan(harvest.Chat{ID: 2, Type: "supergroup", TopMessageID: 19}, checkpoint)
	if decreased.Kind != dailyDialogChanged || decreased.MinID != 0 {
		t.Fatalf("decreased-head plan = %+v", decreased)
	}
	uncovered := checkpoint
	uncovered.Dialogs = map[string]harvest.DailyDialogHead{
		harvest.DailyDialogHeadKey("user", 1): {
			ChatID:            1,
			ChatType:          "user",
			TopMessageID:      15,
			VerifiedMessageID: 10,
		},
	}
	sameUncoveredHead := planDailyDialogScan(harvest.Chat{ID: 1, Type: "user", TopMessageID: 15}, uncovered)
	if sameUncoveredHead.Kind != dailyDialogChanged || sameUncoveredHead.MinID != 10 {
		t.Fatalf("uncovered same-head plan = %+v", sameUncoveredHead)
	}
	newDialog := planDailyDialogScan(harvest.Chat{ID: 3, Type: "user", TopMessageID: 1}, checkpoint)
	if newDialog.Kind != dailyDialogNew || newDialog.MinID != 0 {
		t.Fatalf("new-dialog plan = %+v", newDialog)
	}
	full := planDailyDialogScan(harvest.Chat{ID: 1, Type: "user", TopMessageID: 10}, harvest.DailyDialogCheckpointDecision{})
	if full.Kind != dailyDialogFull || full.MinID != 0 {
		t.Fatalf("fallback plan = %+v", full)
	}
}

func TestDailyDialogHeadDoesNotVerifyHeadBeyondRangeEnd(t *testing.T) {
	end := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	covered := dailyDialogHead(harvest.Chat{
		ID:            1,
		Type:          "user",
		TopMessageID:  10,
		LastMessageAt: end.Add(-time.Second),
	}, end)
	if !covered.HeadFullyVerified || covered.VerifiedMessageID != 10 {
		t.Fatalf("covered head = %+v", covered)
	}
	future := dailyDialogHead(harvest.Chat{
		ID:            2,
		Type:          "supergroup",
		TopMessageID:  20,
		LastMessageAt: end.Add(time.Second),
	}, end)
	if future.HeadFullyVerified || future.VerifiedMessageID != 0 {
		t.Fatalf("future head was marked verified: %+v", future)
	}
	atBoundary := dailyDialogHead(harvest.Chat{
		ID:            3,
		Type:          "user",
		TopMessageID:  30,
		LastMessageAt: end,
	}, end)
	if atBoundary.HeadFullyVerified || atBoundary.VerifiedMessageID != 0 {
		t.Fatalf("exclusive-end head was marked verified: %+v", atBoundary)
	}
}

func TestChangedDialogMinIDIsSentToSearchAndHistoryRPCs(t *testing.T) {
	start := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	target := resolvedTarget{InputPeer: &tg.InputPeerUser{UserID: 1}}
	opts := harvest.OutgoingRangeOptions{
		Start:   start,
		End:     start.AddDate(0, 0, 1),
		History: harvest.HistoryOptions{MinID: 123},
	}
	search := outgoingSearchRequest(target, opts, 200, 100)
	if search.MinID != 123 || search.OffsetID != 200 || search.Limit != 100 {
		t.Fatalf("search request = %+v", search)
	}
	history := outgoingHistoryRequest(target, opts, 200, 100)
	if history.MinID != 123 || history.OffsetID != 200 || history.Limit != 100 {
		t.Fatalf("history request = %+v", history)
	}
}

func TestOutgoingSearchContinuesAfterSparsePage(t *testing.T) {
	start := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	target := resolvedTarget{Chat: harvest.Chat{ID: 1, Type: "user", Display: "One"}}
	opts := harvest.OutgoingRangeOptions{
		Start:   start,
		End:     start.AddDate(0, 0, 1),
		History: harvest.HistoryOptions{BatchSize: 100},
	}
	pages := []tg.MessagesMessagesClass{
		&tg.MessagesMessages{Messages: []tg.MessageClass{
			&tg.Message{ID: 20, Date: int(start.Add(2 * time.Hour).Unix()), Out: true, PeerID: &tg.PeerUser{UserID: 1}, Message: "newer"},
		}},
		&tg.MessagesMessages{Messages: []tg.MessageClass{
			&tg.Message{ID: 10, Date: int(start.Add(time.Hour).Unix()), Out: true, PeerID: &tg.PeerUser{UserID: 1}, Message: "older"},
		}},
		&tg.MessagesMessages{},
	}
	calls := 0
	load := func(_ context.Context, offsetID int, _ int) (tg.MessagesMessagesClass, error) {
		if calls == 0 && offsetID != 0 {
			t.Fatalf("first offset = %d, want 0", offsetID)
		}
		if calls == 1 && offsetID != 20 {
			t.Fatalf("second offset = %d, want 20", offsetID)
		}
		if calls == 2 && offsetID != 10 {
			t.Fatalf("third offset = %d, want 10", offsetID)
		}
		page := pages[calls]
		calls++
		return page, nil
	}
	records, stats, err := (&Session{}).collectOutgoingDay(context.Background(), target, opts, false, load)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || !stats.Complete || stats.Batches != 3 {
		t.Fatalf("calls=%d stats=%+v", calls, stats)
	}
	if len(records) != 2 || records[0].MessageID != 10 || records[1].MessageID != 20 {
		t.Fatalf("records = %+v", records)
	}
}

func TestCheckpointMinIDKeepsNewTrackmateAndOutgoingMessages(t *testing.T) {
	start := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	target := resolvedTarget{
		Chat: harvest.Chat{ID: 3740223926, Type: "supergroup", Display: "Haru 🌸"},
	}
	opts := harvest.OutgoingRangeOptions{
		Start: start,
		End:   start.AddDate(0, 0, 1),
		AdditionalSenderIDsByChat: map[int64][]int64{
			3740223926: {8718303786},
		},
		History: harvest.HistoryOptions{
			BatchSize: 100,
			MinID:     10,
		},
	}
	messages := []tg.MessageClass{
		&tg.Message{ID: 13, Date: int(start.Add(3 * time.Hour).Unix()), Out: true, PeerID: &tg.PeerChannel{ChannelID: 3740223926}, Message: "self"},
		&tg.Message{ID: 12, Date: int(start.Add(2 * time.Hour).Unix()), FromID: &tg.PeerUser{UserID: 8718303786}, PeerID: &tg.PeerChannel{ChannelID: 3740223926}, Message: "trackmate"},
		&tg.Message{ID: 11, Date: int(start.Add(time.Hour).Unix()), FromID: &tg.PeerUser{UserID: 99}, PeerID: &tg.PeerChannel{ChannelID: 3740223926}, Message: "other"},
		&tg.Message{ID: 10, Date: int(start.Add(-time.Hour).Unix()), Out: true, PeerID: &tg.PeerChannel{ChannelID: 3740223926}, Message: "already verified"},
	}
	load := func(context.Context, int, int) (tg.MessagesMessagesClass, error) {
		return &tg.MessagesMessages{
			Messages: messages,
			Users: []tg.UserClass{
				&tg.User{ID: 8718303786, FirstName: "Trackmate", Bot: true},
				&tg.User{ID: 99, FirstName: "Other"},
			},
		}, nil
	}
	records, stats, err := (&Session{}).collectOutgoingDay(context.Background(), target, opts, true, load)
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Complete {
		t.Fatalf("stats incomplete: %+v", stats)
	}
	if len(records) != 2 || records[0].MessageID != 12 || records[1].MessageID != 13 {
		t.Fatalf("checkpoint records = %+v", records)
	}
	if records[0].Sender.ID != 8718303786 || !records[0].Sender.Bot {
		t.Fatalf("Trackmate record lost sender identity: %+v", records[0])
	}
	fullOpts := opts
	fullOpts.History.MinID = 0
	fullRecords, fullStats, err := (&Session{}).collectOutgoingDay(context.Background(), target, fullOpts, true, load)
	if err != nil {
		t.Fatal(err)
	}
	checkpointJSONL := messageRecordsJSONL(t, records)
	fullJSONL := messageRecordsJSONL(t, fullRecords)
	if !fullStats.Complete || checkpointJSONL != fullJSONL {
		t.Fatalf("checkpoint JSONL differs from full JSONL:\ncheckpoint=%s\nfull=%s", checkpointJSONL, fullJSONL)
	}
}

func messageRecordsJSONL(t *testing.T, records []harvest.MessageRecord) string {
	t.Helper()
	var output strings.Builder
	encoder := json.NewEncoder(&output)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	return output.String()
}

func TestDailyRecordFilterRunsBeforeMediaProcessing(t *testing.T) {
	start := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	called := false
	opts := harvest.OutgoingRangeOptions{
		Start: start,
		End:   start.AddDate(0, 0, 1),
		IncludeRecord: func(record harvest.MessageRecord) bool {
			called = true
			return false
		},
		History: harvest.HistoryOptions{
			DownloadMedia:   true,
			TranscribeMedia: true,
		},
	}
	session := &Session{}
	target := resolvedTarget{Chat: harvest.Chat{ID: 42, Display: "Notes"}}
	message := &tg.Message{
		ID:      1,
		Date:    int(start.Add(time.Hour).Unix()),
		Out:     true,
		Message: "excluded day",
		Media: &tg.MessageMediaDocument{
			Document: &tg.Document{
				ID:       99,
				MimeType: "audio/ogg",
				Size:     100,
			},
		},
	}

	if _, ok := session.normalizeOutgoingDayRecord(context.Background(), message, target, peer.Entities{}, nil, opts); ok {
		t.Fatal("record rejected by range filter should not be emitted")
	}
	if !called {
		t.Fatal("record filter was not called")
	}
}

func TestNormalizeRecordPreservesForwardOrigin(t *testing.T) {
	date := time.Date(2026, 7, 22, 0, 46, 0, 0, time.UTC)
	channel := &tg.Channel{ID: 123, Title: "Source Channel"}
	channel.SetUsername("source_channel")
	entities := peer.NewEntities(nil, nil, map[int64]*tg.Channel{123: channel})

	header := tg.MessageFwdHeader{Date: int(date.Add(-24 * time.Hour).Unix())}
	header.SetFromID(&tg.PeerChannel{ChannelID: 123})
	header.SetChannelPost(77)
	header.SetPostAuthor("Автор")
	message := &tg.Message{
		ID:      10,
		Date:    int(date.Unix()),
		Out:     true,
		Message: "Пересланный текст",
	}
	message.SetFwdFrom(header)

	record, ok := normalizeRecord(message, harvest.Chat{ID: 42, Display: "Saved Messages"}, entities)
	if !ok {
		t.Fatal("forwarded message was not normalized")
	}
	if record.Forward == nil {
		t.Fatal("forward metadata is missing")
	}
	if record.Forward.Origin == nil || record.Forward.Origin.ID != 123 || record.Forward.Origin.Type != "channel" || record.Forward.Origin.Display != "Source Channel" {
		t.Fatalf("unexpected forward origin: %+v", record.Forward.Origin)
	}
	if record.Forward.OriginName != "Source Channel" {
		t.Fatalf("origin name = %q", record.Forward.OriginName)
	}
	if record.Forward.OriginalMessageID != 77 || record.Forward.SourceURL != "https://t.me/source_channel/77" {
		t.Fatalf("unexpected source pointer: %+v", record.Forward)
	}
	if record.Forward.PostAuthor != "Автор" {
		t.Fatalf("post author = %q", record.Forward.PostAuthor)
	}
	if !record.Forward.OriginalDate.Equal(date.Add(-24 * time.Hour)) {
		t.Fatalf("original date = %s", record.Forward.OriginalDate)
	}
}

func TestNormalizeRecordPreservesHiddenForwardOriginName(t *testing.T) {
	date := time.Date(2026, 7, 22, 0, 46, 0, 0, time.UTC)
	header := tg.MessageFwdHeader{Date: int(date.Add(-time.Hour).Unix())}
	header.SetFromName("Скрытый автор")
	message := &tg.Message{ID: 11, Date: int(date.Unix()), Out: true, Message: "Пересланный текст"}
	message.SetFwdFrom(header)

	record, ok := normalizeRecord(message, harvest.Chat{ID: 42, Display: "Saved Messages"}, peer.Entities{})
	if !ok || record.Forward == nil {
		t.Fatalf("hidden-origin forward was not normalized: ok=%t record=%+v", ok, record)
	}
	if record.Forward.Origin != nil || record.Forward.OriginName != "Скрытый автор" || record.Forward.SourceURL != "" {
		t.Fatalf("unexpected hidden-origin metadata: %+v", record.Forward)
	}
}

func TestWithFloodWaitRetrySleepRetries(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	sleeps := 0

	err := withFloodWaitRetrySleep(ctx, func(_ context.Context, delay time.Duration) error {
		sleeps++
		if delay != 2*time.Second {
			t.Fatalf("sleep delay = %s, want 2s", delay)
		}
		return nil
	}, func() error {
		attempts++
		if attempts == 1 {
			return tgerr.New(420, "FLOOD_WAIT_2")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retry returned error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if sleeps != 1 {
		t.Fatalf("sleeps = %d, want 1", sleeps)
	}
}

func TestNoteFloodWaitPushesFutureRPCSlot(t *testing.T) {
	s := &Session{rpcSpacing: 700 * time.Millisecond}
	s.noteFloodWait("get_history", 5*time.Second)

	now := time.Now()
	if remaining := s.nextRPCAt.Sub(now); remaining < 5*time.Second {
		t.Fatalf("rpc cooldown = %s, want at least 5s", remaining)
	}
	if s.FloodWaits() != 1 {
		t.Fatalf("FloodWaits = %d, want 1", s.FloodWaits())
	}
	events := s.FloodEvents()
	if len(events) != 1 {
		t.Fatalf("len(FloodEvents) = %d, want 1", len(events))
	}
	if events[0].Operation != "get_history" || events[0].Kind != "flood_wait" {
		t.Fatalf("unexpected flood event: %+v", events[0])
	}
}

func TestIsTransportFlood(t *testing.T) {
	if !isTransportFlood(errors.New("rpc failed: transport flood")) {
		t.Fatal("expected transport flood to be detected")
	}
	if isTransportFlood(errors.New("boom")) {
		t.Fatal("did not expect generic error to be treated as transport flood")
	}
}

func TestHistoryProgressCopiesStats(t *testing.T) {
	progress := historyProgress(harvest.HistoryStats{
		Records:    5,
		FirstID:    1,
		LastID:     10,
		Batches:    2,
		FloodWaits: 1,
	}, 3, 7, true)
	if progress.BatchRecords != 3 || progress.Records != 5 || progress.FirstID != 1 || progress.LastID != 10 || progress.Batches != 2 || progress.NextOffsetID != 7 || !progress.Done || progress.FloodWaits != 1 {
		t.Fatalf("unexpected progress: %+v", progress)
	}
}

func TestRunTranscriberUsesExplicitRunnerWithoutCommandConfig(t *testing.T) {
	runner := &fakeTranscriber{text: "from runner"}
	text, err := runTranscriber(context.Background(), runner, transcribe.Options{}, "input.ogg", "out.txt")
	if err != nil {
		t.Fatal(err)
	}
	if text != "from runner" || runner.calls != 1 {
		t.Fatalf("text=%q calls=%d", text, runner.calls)
	}
}

func TestTranscriptErrorMessageClassifiesNoAudioStream(t *testing.T) {
	err := transcriptErrorMessage(errString("ffmpeg: exit status 234: [out#0/wav @ 0x123] Output file does not contain any stream Error opening output file /tmp/.vosk-123.wav"))
	if err != "skipped: media has no audio stream" {
		t.Fatalf("error = %q", err)
	}
	other := transcriptErrorMessage(errString("vosk session: model load failed"))
	if other != "vosk session: model load failed" {
		t.Fatalf("other error = %q", other)
	}
}

func TestGenericVideoTranscriptPolicyKeepsOnlyShortVerticalPhoneVideo(t *testing.T) {
	base := harvest.Attachment{
		Kind:            "video",
		FileName:        "phone.mp4",
		Size:            20 * 1024 * 1024,
		DurationSeconds: 120,
		Width:           1080,
		Height:          1920,
	}
	if ok, reason := genericVideoTranscriptAllowed(base, harvest.HistoryOptions{}); !ok {
		t.Fatalf("phone video rejected: %s", reason)
	}

	cases := []struct {
		name       string
		attachment harvest.Attachment
		wantReason string
	}{
		{
			name:       "horizontal",
			attachment: withVideoShape(base, 1920, 1080, 120, 20*1024*1024),
			wantReason: "not vertical",
		},
		{
			name:       "too long",
			attachment: withVideoShape(base, 1080, 1920, 361, 20*1024*1024),
			wantReason: "duration",
		},
		{
			name:       "too large",
			attachment: withVideoShape(base, 1080, 1920, 120, harvest.DefaultMaxGenericVideoBytes+1),
			wantReason: "size",
		},
		{
			name:       "too high resolution",
			attachment: withVideoShape(base, 1440, 2560, 120, 20*1024*1024),
			wantReason: "resolution",
		},
		{
			name:       "missing metadata",
			attachment: withVideoShape(base, 0, 0, 0, 20*1024*1024),
			wantReason: "duration",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := genericVideoTranscriptAllowed(tc.attachment, harvest.HistoryOptions{})
			if ok {
				t.Fatalf("expected video to be rejected")
			}
			if !strings.Contains(reason, tc.wantReason) {
				t.Fatalf("reason = %q, want contains %q", reason, tc.wantReason)
			}
		})
	}

	if ok, reason := genericVideoTranscriptAllowed(withVideoShape(base, 1920, 1080, 1000, 500*1024*1024), harvest.HistoryOptions{
		VideoTranscribeMode: harvest.VideoTranscribeAll,
	}); !ok {
		t.Fatalf("all mode rejected generic video: %s", reason)
	}
	if ok, _ := genericVideoTranscriptAllowed(base, harvest.HistoryOptions{VideoTranscribeMode: harvest.VideoTranscribeOff}); ok {
		t.Fatalf("off mode should reject generic video")
	}
}

func withVideoShape(base harvest.Attachment, width int, height int, duration float64, size int64) harvest.Attachment {
	base.Width = width
	base.Height = height
	base.DurationSeconds = duration
	base.Size = size
	return base
}

func TestExtractLinksFindsTextAndEntityURLsDedupingTelegramShortLinks(t *testing.T) {
	got := extractLinks(
		"open https://example.com/task, then t.me/group/10 and https://example.com/task",
		[]tg.MessageEntityClass{
			&tg.MessageEntityTextURL{URL: "https://edu.hse.ru/mod/assign/view.php?id=1"},
			&tg.MessageEntityTextURL{URL: "not a url"},
		},
	)
	want := []string{
		"https://example.com/task",
		"https://t.me/group/10",
		"https://edu.hse.ru/mod/assign/view.php?id=1",
	}
	if len(got) != len(want) {
		t.Fatalf("links=%#v want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("links[%d]=%q want %q; all=%#v", i, got[i], want[i], got)
		}
	}
}

type errString string

func (e errString) Error() string {
	return string(e)
}

type fakeTranscriber struct {
	text  string
	calls int
}

func (f *fakeTranscriber) Run(ctx context.Context, inputPath string, outputPath string) (string, error) {
	f.calls++
	return f.text, nil
}

func TestMediaLinksAndAttachmentsKeepDocumentMetadata(t *testing.T) {
	webpage := &tg.MessageMediaWebPage{
		Webpage: &tg.WebPage{URL: "https://edu.hse.ru/mod/page/view.php?id=10", Title: "Task page"},
	}
	if got := extractMediaLinks(webpage); len(got) != 1 || got[0] != "https://edu.hse.ru/mod/page/view.php?id=10" {
		t.Fatalf("webpage media links=%#v", got)
	}
	webpageAttachments := extractAttachments(webpage)
	if len(webpageAttachments) != 1 ||
		webpageAttachments[0].Kind != "webpage" ||
		webpageAttachments[0].Title != "Task page" ||
		webpageAttachments[0].URL != "https://edu.hse.ru/mod/page/view.php?id=10" {
		t.Fatalf("webpage attachments=%#v", webpageAttachments)
	}

	document := &tg.MessageMediaDocument{}
	document.SetDocument(
		&tg.Document{
			MimeType: "application/pdf",
			Size:     123,
			Attributes: []tg.DocumentAttributeClass{
				&tg.DocumentAttributeFilename{FileName: "task.pdf"},
			},
		},
	)
	documentAttachments := extractAttachments(document)
	if len(documentAttachments) != 1 ||
		documentAttachments[0].Kind != "document" ||
		documentAttachments[0].FileName != "task.pdf" ||
		documentAttachments[0].MIMEType != "application/pdf" ||
		documentAttachments[0].Size != 123 {
		t.Fatalf("document attachments=%#v", documentAttachments)
	}

	imageDocument := &tg.MessageMediaDocument{}
	imageDocument.SetDocument(&tg.Document{
		MimeType: "image/png",
		Size:     456,
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeFilename{FileName: "screenshot.png"},
		},
	})
	imageAttachments := extractAttachments(imageDocument)
	if len(imageAttachments) != 1 ||
		imageAttachments[0].Kind != "image" ||
		imageAttachments[0].FileName != "screenshot.png" ||
		imageAttachments[0].MIMEType != "image/png" ||
		imageAttachments[0].Size != 456 {
		t.Fatalf("image document attachments=%#v", imageAttachments)
	}

	if photoAttachments := extractAttachments(&tg.MessageMediaPhoto{}); len(photoAttachments) != 1 || photoAttachments[0].Kind != "photo" {
		t.Fatalf("photo attachments=%#v", photoAttachments)
	}

	video := &tg.MessageMediaDocument{Video: true}
	video.SetDocument(&tg.Document{
		ID:       42,
		MimeType: "video/mp4",
		Size:     12345,
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeFilename{FileName: "talk.mp4"},
			&tg.DocumentAttributeVideo{Duration: 2701.5, W: 1920, H: 1080},
		},
	})
	videoAttachments := extractAttachments(video)
	if len(videoAttachments) != 1 ||
		videoAttachments[0].Kind != "video" ||
		videoAttachments[0].FileName != "talk.mp4" ||
		videoAttachments[0].DurationSeconds != 2701.5 ||
		videoAttachments[0].Width != 1920 ||
		videoAttachments[0].Height != 1080 {
		t.Fatalf("video attachments=%#v", videoAttachments)
	}
}

func TestDailyDocumentAttachmentAddsVideoMetadata(t *testing.T) {
	media := &tg.MessageMediaDocument{Video: true}
	media.SetDocument(&tg.Document{
		ID:       42,
		MimeType: "video/mp4",
		Size:     12345,
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeFilename{FileName: "clip.mp4"},
			&tg.DocumentAttributeVideo{Duration: 12.5, W: 720, H: 1280},
		},
	})

	attachment, ok := dailyDocumentAttachment(media, 99)
	if !ok {
		t.Fatalf("expected daily video attachment")
	}
	if attachment.MediaID != "document:42" ||
		attachment.FileName != "clip.mp4" ||
		attachment.DurationSeconds != 12.5 ||
		attachment.Width != 720 ||
		attachment.Height != 1280 {
		t.Fatalf("unexpected attachment metadata: %+v", attachment)
	}
}

func TestDownloadRecordMediaEnsuresTranscriptAttachmentMetadata(t *testing.T) {
	media := &tg.MessageMediaDocument{Voice: true}
	media.SetDocument(&tg.Document{
		ID:       84,
		MimeType: "audio/ogg",
		Size:     1234,
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeAudio{Voice: true, Duration: 3},
		},
	})
	record := harvest.MessageRecord{MessageID: 55, Kind: "voice"}

	session := &Session{}
	session.downloadRecordMedia(context.Background(), &tg.Message{ID: 55, Media: media}, &record, harvest.HistoryOptions{DownloadMedia: true})

	if len(record.Attachments) != 1 {
		t.Fatalf("attachments len = %d, want 1", len(record.Attachments))
	}
	attachment := record.Attachments[0]
	if attachment.Kind != "voice" ||
		attachment.MediaID != "document:84" ||
		attachment.FileName != "voice-55.oga" ||
		attachment.DurationSeconds != 3 {
		t.Fatalf("unexpected attachment metadata: %+v", attachment)
	}
}

func TestMediaSizeLimitExceededWritesActionableHint(t *testing.T) {
	record := harvest.MessageRecord{
		Chat:      harvest.Chat{ID: 123, Display: "Study"},
		MessageID: 77,
		Attachments: []harvest.Attachment{
			{Kind: "document", FileName: "big.pdf", Size: 11 * 1024 * 1024},
		},
	}
	if !mediaSizeLimitExceeded(&record, 0, harvest.HistoryOptions{
		MaxDocumentBytes:      10 * 1024 * 1024,
		ManualDownloadCommand: "telegram-harvest download-media",
	}) {
		t.Fatalf("expected size limit to be exceeded")
	}
	attachment := record.Attachments[0]
	if attachment.DownloadError == "" || !strings.Contains(attachment.DownloadError, "document cap") {
		t.Fatalf("download error = %q", attachment.DownloadError)
	}
	if attachment.DownloadHint == "" ||
		!strings.Contains(attachment.DownloadHint, "--chat 123") ||
		!strings.Contains(attachment.DownloadHint, "--message-id 77") ||
		!strings.Contains(attachment.DownloadHint, "--index 1") {
		t.Fatalf("download hint = %q", attachment.DownloadHint)
	}
}

func TestExtractAttachmentsIgnoresNonAcademicTelegramMedia(t *testing.T) {
	cases := []struct {
		name  string
		media tg.MessageMediaClass
	}{
		{
			name:  "poll without attached material",
			media: &tg.MessageMediaPoll{Poll: tg.Poll{Question: tg.TextWithEntities{Text: "Readiness?"}}},
		},
		{
			name:  "venue",
			media: &tg.MessageMediaVenue{Title: "Lecture hall", Address: "Campus"},
		},
		{
			name:  "contact",
			media: &tg.MessageMediaContact{FirstName: "Ivan", LastName: "Ivanov"},
		},
		{
			name:  "dice",
			media: &tg.MessageMediaDice{Emoticon: "dice", Value: 6},
		},
		{
			name:  "game",
			media: &tg.MessageMediaGame{},
		},
		{
			name:  "invoice",
			media: &tg.MessageMediaInvoice{Title: "Payment"},
		},
		{
			name:  "voice",
			media: &tg.MessageMediaDocument{Voice: true},
		},
		{
			name:  "round video",
			media: &tg.MessageMediaDocument{Round: true},
		},
		{
			name:  "video",
			media: &tg.MessageMediaDocument{Video: true},
		},
		{
			name:  "unsupported",
			media: &tg.MessageMediaUnsupported{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractAttachments(tc.media); len(got) != 0 {
				t.Fatalf("attachments=%#v; want no academic material attachment", got)
			}
		})
	}
}

func TestEnsureDailyAttachmentsIncludesVoiceAudioRoundVideoAndVideo(t *testing.T) {
	cases := []struct {
		name  string
		media *tg.MessageMediaDocument
		want  string
	}{
		{
			name:  "voice",
			media: &tg.MessageMediaDocument{Voice: true},
			want:  "voice",
		},
		{
			name:  "round video",
			media: &tg.MessageMediaDocument{Round: true},
			want:  "round_video",
		},
		{
			name: "audio document",
			media: func() *tg.MessageMediaDocument {
				media := &tg.MessageMediaDocument{}
				media.SetDocument(&tg.Document{
					MimeType: "audio/ogg",
					Size:     100,
					Attributes: []tg.DocumentAttributeClass{
						&tg.DocumentAttributeAudio{Duration: 5},
					},
				})
				return media
			}(),
			want: "audio",
		},
		{
			name:  "video",
			media: &tg.MessageMediaDocument{Video: true},
			want:  "video",
		},
		{
			name: "video document attribute",
			media: func() *tg.MessageMediaDocument {
				media := &tg.MessageMediaDocument{}
				media.SetDocument(&tg.Document{
					MimeType: "video/mp4",
					Size:     100,
					Attributes: []tg.DocumentAttributeClass{
						&tg.DocumentAttributeVideo{Duration: 5, W: 320, H: 240},
					},
				})
				return media
			}(),
			want: "video",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := harvest.MessageRecord{MessageID: 10}
			ensureDailyAttachments(&tg.Message{ID: 10, Media: tc.media}, &record)
			if len(record.Attachments) != 1 {
				t.Fatalf("attachments=%#v", record.Attachments)
			}
			if record.Attachments[0].Kind != tc.want {
				t.Fatalf("kind=%q want %q", record.Attachments[0].Kind, tc.want)
			}
			if record.Attachments[0].FileName == "" {
				t.Fatalf("expected fallback file name")
			}
		})
	}
}

func TestNormalizeRecordMergesTextAndWebpageLinks(t *testing.T) {
	record, ok := normalizeRecord(
		&tg.Message{
			ID:      10,
			Date:    1,
			Message: "read https://example.com/a",
			Media: &tg.MessageMediaWebPage{
				Webpage: &tg.WebPage{URL: "https://edu.hse.ru/mod/page/view.php?id=10", Title: "Task page"},
			},
		},
		harvest.Chat{ID: 1, Display: "Study"},
		peer.Entities{},
	)
	if !ok {
		t.Fatal("record was not normalized")
	}
	wantLinks := []string{"https://example.com/a", "https://edu.hse.ru/mod/page/view.php?id=10"}
	if len(record.Links) != len(wantLinks) {
		t.Fatalf("links=%#v want %#v", record.Links, wantLinks)
	}
	for i := range wantLinks {
		if record.Links[i] != wantLinks[i] {
			t.Fatalf("links[%d]=%q want %q; all=%#v", i, record.Links[i], wantLinks[i], record.Links)
		}
	}
	if len(record.Attachments) != 1 || record.Attachments[0].Kind != "webpage" {
		t.Fatalf("attachments=%#v", record.Attachments)
	}
}

func TestMessageURLAndMaskPhone(t *testing.T) {
	if got := messageURL(harvest.Chat{Username: "study_group"}, 42); got != "https://t.me/study_group/42" {
		t.Fatalf("username url = %s", got)
	}
	if got := messageURL(harvest.Chat{ID: 1234567890, Type: "supergroup"}, 456); got != "https://t.me/c/1234567890/456" {
		t.Fatalf("private supergroup url = %s", got)
	}
	if got := messageURL(harvest.Chat{ID: 1, Type: "basic_group"}, 7); got != "" {
		t.Fatalf("basic group url = %s", got)
	}
	if got := maskPhone("+10000000017"); got != "+1********17" {
		t.Fatalf("masked phone = %s", got)
	}
	if got := maskPhone("1234"); got != "1234" {
		t.Fatalf("short phone mask = %s", got)
	}
}

func TestRPCTimeoutsAreConservativeForHistoryReads(t *testing.T) {
	if got := rpcTimeoutForOperation("get_history"); got != defaultHistoryTimeout {
		t.Fatalf("history timeout = %s, want %s", got, defaultHistoryTimeout)
	}
	if got := rpcTimeoutForOperation("get_dialogs"); got != defaultDialogTimeout {
		t.Fatalf("dialog timeout = %s, want %s", got, defaultDialogTimeout)
	}
	if got := rpcTimeoutForOperation("unknown"); got != defaultRPCTimeout {
		t.Fatalf("default timeout = %s, want %s", got, defaultRPCTimeout)
	}
}
