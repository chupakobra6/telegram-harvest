package mtproto

import (
	"testing"

	"github.com/chupakobra6/telegram-study-harvest/internal/harvest"
	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
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
	if opts.Limit != 500 || opts.BatchSize != 100 || opts.MaxBatches != 20 {
		t.Fatalf("unexpected normalized opts: %+v", opts)
	}
	if got := initialHistoryCapacity(opts); got != 500 {
		t.Fatalf("initial capacity = %d", got)
	}
	if !shouldContinueHistory(opts, 499, 19) {
		t.Fatalf("expected history to continue before limits")
	}
	if shouldContinueHistory(opts, 500, 19) {
		t.Fatalf("expected history to stop at record limit")
	}
	if shouldContinueHistory(opts, 10, 20) {
		t.Fatalf("expected history to stop at batch limit")
	}
	if got := nextBatchLimit(opts, 460); got != 40 {
		t.Fatalf("next batch limit = %d", got)
	}

	all := normalizeHistoryOptions(harvest.HistoryOptions{All: true})
	if all.Limit != 0 || all.MaxBatches != 0 || all.BatchSize != 80 {
		t.Fatalf("unexpected all-history opts: %+v", all)
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

func TestMediaLinksAndAttachmentsKeepUsefulNonDownloadedMetadata(t *testing.T) {
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

	cases := []struct {
		name  string
		media tg.MessageMediaClass
		kind  string
		title string
	}{
		{
			name:  "poll",
			media: &tg.MessageMediaPoll{Poll: tg.Poll{Question: tg.TextWithEntities{Text: "Readiness?"}}},
			kind:  "poll",
			title: "Readiness?",
		},
		{
			name:  "venue",
			media: &tg.MessageMediaVenue{Title: "Lecture hall", Address: "Campus"},
			kind:  "venue",
			title: "Lecture hall",
		},
		{
			name:  "contact",
			media: &tg.MessageMediaContact{FirstName: "Ivan", LastName: "Ivanov"},
			kind:  "contact",
			title: "Ivan Ivanov",
		},
		{
			name:  "dice",
			media: &tg.MessageMediaDice{Emoticon: "dice", Value: 6},
			kind:  "dice",
			title: "dice",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractAttachments(tc.media)
			if len(got) != 1 || got[0].Kind != tc.kind || got[0].Title != tc.title {
				t.Fatalf("attachments=%#v want kind=%q title=%q", got, tc.kind, tc.title)
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
