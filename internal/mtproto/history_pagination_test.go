package mtproto

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

type historyInvoker struct {
	pages   map[int][]tg.MessageClass
	offsets []int
}

type manyDialogsInvoker struct{}

func (manyDialogsInvoker) Invoke(_ context.Context, input bin.Encoder, output bin.Decoder) error {
	req, ok := input.(*tg.MessagesGetDialogsRequest)
	if !ok {
		return fmt.Errorf("unexpected RPC %T", input)
	}
	count := 601
	if folder, _ := req.GetFolderID(); folder == 1 {
		count = 0
	}
	top := count
	if req.OffsetID > 0 {
		top = req.OffsetID - 1
	}
	page := &tg.MessagesDialogsSlice{Count: count}
	// Sparse pages must not hide the remaining dialogs either.
	for id := top; id > 0 && len(page.Dialogs) < min(req.Limit, 70); id-- {
		page.Dialogs = append(page.Dialogs, &tg.Dialog{Peer: &tg.PeerChat{ChatID: int64(id)}, TopMessage: id})
		page.Chats = append(page.Chats, &tg.Chat{ID: int64(id), Title: fmt.Sprint(id)})
		page.Messages = append(page.Messages, &tg.Message{ID: id, Date: 1000, PeerID: &tg.PeerChat{ChatID: int64(id)}, Message: "old"})
	}
	output.(*tg.MessagesDialogsBox).Dialogs = page
	return nil
}

func TestDailyCoversMoreThan500DialogsAndFlagsExplicitCap(t *testing.T) {
	for _, limit := range []int{0, 100} {
		s := &Session{raw: tg.NewClient(manyDialogsInvoker{}), dialogCache: map[string]resolvedTarget{}}
		stats, err := s.DumpOutgoingRange(context.Background(), harvest.OutgoingRangeOptions{DialogLimit: limit, Start: time.Unix(2000, 0), End: time.Unix(3000, 0)}, nil)
		want := 601
		if limit > 0 {
			want = limit
		}
		if err != nil || stats.DialogsScanned != want || stats.Complete != (limit == 0) {
			t.Fatalf("limit=%d stats=%+v err=%v", limit, stats, err)
		}
	}
}

func (f *historyInvoker) Invoke(_ context.Context, input bin.Encoder, output bin.Decoder) error {
	req, ok := input.(*tg.MessagesGetHistoryRequest)
	if !ok {
		return fmt.Errorf("unexpected RPC %T", input)
	}
	if req.Limit <= 0 || req.Limit > 100 {
		return fmt.Errorf("unsafe page size %d", req.Limit)
	}
	f.offsets = append(f.offsets, req.OffsetID)
	output.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: f.pages[req.OffsetID]}
	return nil
}

func TestHistorySparsePageDoesNotProveCompletion(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(fmt.Sprintf("all=%t", all), func(t *testing.T) {
			fake := &historyInvoker{pages: map[int][]tg.MessageClass{
				0:   {&tg.Message{ID: 250, Date: 1000, Message: "newest"}},
				250: {&tg.MessageService{ID: 249, Date: 999}},
				249: {&tg.Message{ID: 248, Date: 998, Message: "older but required"}},
			}}
			s := &Session{raw: tg.NewClient(fake), dialogCache: map[string]resolvedTarget{"study": {Chat: harvest.Chat{ID: 1, Type: "basic_group"}, InputPeer: &tg.InputPeerChat{ChatID: 1}}}}
			var ids []int
			_, stats, err := s.DumpHistory(context.Background(), "study", harvest.HistoryOptions{All: all, Limit: 100, MinID: 10}, func(r harvest.MessageRecord) error { ids = append(ids, r.MessageID); return nil })
			if err != nil || !stats.Complete || len(fake.offsets) != 4 || len(ids) < 2 {
				t.Fatalf("sparse history: ids=%v stats=%+v offsets=%v err=%v", ids, stats, fake.offsets, err)
			}
			found := false
			for _, id := range ids {
				found = found || id == 248
			}
			if !found {
				t.Fatal("short page skipped older message")
			}
		})
	}
}

func TestHistoryRepeatedPageIsNotCommitted(t *testing.T) {
	page := []tg.MessageClass{&tg.Message{ID: 250, Date: 1000, Message: "same"}}
	fake := &historyInvoker{pages: map[int][]tg.MessageClass{0: page, 250: page}}
	s := &Session{raw: tg.NewClient(fake), dialogCache: map[string]resolvedTarget{"study": {Chat: harvest.Chat{ID: 1}, InputPeer: &tg.InputPeerChat{ChatID: 1}}}}
	commits := 0
	_, _, err := s.DumpHistory(context.Background(), "study", harvest.HistoryOptions{All: true, Progress: func(harvest.HistoryProgress) error { commits++; return nil }}, nil)
	if err == nil || commits != 1 {
		t.Fatalf("repeated page err=%v commits=%d", err, commits)
	}
}

func TestBoundedPreviewDoesNotClaimCompleteRange(t *testing.T) {
	fake := &historyInvoker{pages: map[int][]tg.MessageClass{0: {&tg.Message{ID: 250, Date: 1000, Message: "preview"}}}}
	s := &Session{raw: tg.NewClient(fake), dialogCache: map[string]resolvedTarget{"study": {Chat: harvest.Chat{ID: 1}, InputPeer: &tg.InputPeerChat{ChatID: 1}}}}
	_, stats, err := s.DumpHistory(context.Background(), "study", harvest.HistoryOptions{Limit: 1}, nil)
	if err != nil || stats.Complete || stats.Records != 1 {
		t.Fatalf("preview stats=%+v err=%v", stats, err)
	}
}

type archivedDialogsInvoker struct{ calls [][2]int }

func (f *archivedDialogsInvoker) Invoke(_ context.Context, input bin.Encoder, output bin.Decoder) error {
	req, ok := input.(*tg.MessagesGetDialogsRequest)
	if !ok {
		return fmt.Errorf("unexpected RPC %T", input)
	}
	folder, set := req.GetFolderID()
	if !set || (folder != 0 && folder != 1) {
		return fmt.Errorf("explicit folder missing")
	}
	f.calls = append(f.calls, [2]int{folder, req.OffsetID})
	page := &tg.MessagesDialogsSlice{Count: 2}
	id := 0
	if req.OffsetID == 0 {
		id = 2
	} else if req.OffsetID == 2 {
		id = 1
	}
	if id > 0 {
		// Chat 2 appears in both folders, while chat 1 has a distinct peer type.
		peer := tg.PeerClass(&tg.PeerChat{ChatID: int64(id)})
		page.Chats = []tg.ChatClass{&tg.Chat{ID: int64(id), Title: fmt.Sprint(id)}}
		if folder == 1 && id == 1 {
			peer = &tg.PeerUser{UserID: 1}
			page.Users = []tg.UserClass{&tg.User{ID: 1, FirstName: "Archived", AccessHash: 1}}
			page.Chats = nil
		}
		page.Dialogs = []tg.DialogClass{&tg.Dialog{Peer: peer, TopMessage: id}}
		page.Messages = []tg.MessageClass{&tg.Message{ID: id, Date: 1000, PeerID: peer}}
	}
	output.(*tg.MessagesDialogsBox).Dialogs = page
	return nil
}
func TestListAllDialogsTraversesBothFoldersAndDeduplicatesTypedPeers(t *testing.T) {
	fake := &archivedDialogsInvoker{}
	s := &Session{raw: tg.NewClient(fake), dialogCache: map[string]resolvedTarget{}}
	chats, err := s.ListAllDialogs(context.Background())
	if err != nil || len(chats) != 3 {
		t.Fatalf("chats=%+v err=%v", chats, err)
	}
	want := [][2]int{{0, 0}, {0, 2}, {0, 1}, {1, 0}, {1, 2}, {1, 1}}
	if fmt.Sprint(fake.calls) != fmt.Sprint(want) {
		t.Fatalf("folder pages=%v want=%v", fake.calls, want)
	}
	keys := map[string]bool{}
	for _, chat := range chats {
		keys[harvest.AccountChatKey(chat)] = true
	}
	for _, key := range []string{"basic_group:1", "basic_group:2", "user:1"} {
		if !keys[key] {
			t.Fatalf("missing typed peer %s in %v", key, keys)
		}
	}
}
