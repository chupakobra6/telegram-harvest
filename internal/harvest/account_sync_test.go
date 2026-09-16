package harvest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeAccountSource struct {
	profile  SelfProfile
	chats    []Chat
	records  map[string][]MessageRecord
	options  map[string][]HistoryOptions
	failChat string
}

func (f *fakeAccountSource) SelfProfile(context.Context) (SelfProfile, error) {
	return f.profile, nil
}

func (f *fakeAccountSource) ListAllDialogs(context.Context) ([]Chat, error) {
	return f.chats, nil
}

func (f *fakeAccountSource) DumpHistory(_ context.Context, chat string, opts HistoryOptions, emit func(MessageRecord) error) (Chat, HistoryStats, error) {
	f.options[chat] = append(f.options[chat], opts)
	if chat == f.failChat {
		return Chat{}, HistoryStats{}, errors.New("chat unavailable")
	}
	var found Chat
	for _, candidate := range f.chats {
		if candidate.ID != 0 && chat == AccountChatKey(candidate) {
			found = candidate
		}
	}
	stats := HistoryStats{Complete: true, Batches: 1}
	for _, record := range f.records[chat] {
		if opts.All && opts.StartOffsetID > 0 && record.MessageID >= opts.StartOffsetID {
			continue
		}
		if record.MessageID <= opts.MinID {
			continue
		}
		if err := emit(record); err != nil {
			return found, stats, err
		}
		stats.Records++
		if stats.FirstID == 0 || record.MessageID < stats.FirstID {
			stats.FirstID = record.MessageID
		}
		if record.MessageID > stats.LastID {
			stats.LastID = record.MessageID
		}
	}
	return found, stats, nil
}

func TestRunAccountSyncMarksFailedChatIncompleteAndContinues(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lumina")
	fake := &fakeAccountSource{
		profile:  SelfProfile{ID: 77},
		chats:    []Chat{{ID: 1, Type: "user", Display: "Unavailable"}, {ID: 2, Type: "user", Display: "Available"}},
		records:  map[string][]MessageRecord{"user:2": {{Chat: Chat{ID: 2, Type: "user"}, MessageID: 1, Text: "kept", Kind: "text"}}},
		options:  make(map[string][]HistoryOptions),
		failChat: "user:1",
	}
	state, err := RunAccountSync(context.Background(), fake, AccountSyncOptions{StateDir: dir, ExpectedAccountID: 77})
	if err == nil || state.Complete || state.Dialogs[0].Status != "error" || state.Dialogs[1].Status != "complete" {
		t.Fatalf("partial account sync = %+v, %v", state, err)
	}
	if records := readAccountRecords(t, filepath.Join(dir, "chats", "user-2", "messages.jsonl")); len(records) != 1 || records[0].Text != "kept" {
		t.Fatalf("available chat was skipped: %+v", records)
	}
	index, err := os.ReadFile(AccountIndexPath(dir))
	if err != nil || !strings.Contains(string(index), "Все доступные диалоги обработаны: `false`") {
		t.Fatalf("incomplete index = %s, %v", index, err)
	}
}

func TestRunAccountSyncKeepsSameNumericIDInDifferentPeerTypes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lumina")
	user := Chat{ID: 42, Type: "user", Display: "Person"}
	channel := Chat{ID: 42, Type: "channel", Display: "Channel"}
	fake := &fakeAccountSource{
		profile: SelfProfile{ID: 77}, chats: []Chat{user, channel},
		records: map[string][]MessageRecord{
			"user:42":    {{Chat: user, MessageID: 1, Text: "private", Kind: "text"}},
			"channel:42": {{Chat: channel, MessageID: 1, Text: "public", Kind: "text"}},
		}, options: make(map[string][]HistoryOptions),
	}
	state, err := RunAccountSync(context.Background(), fake, AccountSyncOptions{StateDir: dir, ExpectedAccountID: 77})
	if err != nil || !state.Complete || len(state.Dialogs) != 2 {
		t.Fatalf("colliding peers = %+v, %v", state, err)
	}
	for path, want := range map[string]string{"user-42": "private", "channel-42": "public"} {
		records := readAccountRecords(t, filepath.Join(dir, "chats", path, "messages.jsonl"))
		if len(records) != 1 || records[0].Text != want {
			t.Fatalf("%s records = %+v", path, records)
		}
	}
}

func TestRunAccountSyncExportsBothDirectionsAndUpdatesIncrementally(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lumina")
	chat := Chat{ID: 42, Type: "user", Display: "Private conversation"}
	fake := &fakeAccountSource{
		profile: SelfProfile{ID: 77, Display: "Lumina 22"},
		chats:   []Chat{chat},
		records: map[string][]MessageRecord{"user:42": {
			{Chat: chat, MessageID: 1, Date: time.Now().UTC(), Sender: Sender{ID: 8, Display: "Other"}, Text: "incoming", Kind: "text"},
			{Chat: chat, MessageID: 2, Date: time.Now().UTC(), Sender: Sender{ID: 77, Self: true}, Outgoing: true, Text: "outgoing", Kind: "text"},
		}},
		options: make(map[string][]HistoryOptions),
	}
	state, err := RunAccountSync(context.Background(), fake, AccountSyncOptions{StateDir: dir, ExpectedAccountID: 77})
	if err != nil || !state.Complete || state.Dialogs[0].Records != 2 {
		t.Fatalf("first account sync = %+v, %v", state, err)
	}
	stream := filepath.Join(dir, "chats", "user-42", "messages.jsonl")
	if got := readAccountRecords(t, stream); len(got) != 2 || got[0].Text != "incoming" || got[1].Text != "outgoing" {
		t.Fatalf("stream = %+v", got)
	}
	index, err := os.ReadFile(AccountIndexPath(dir))
	if err != nil || !strings.Contains(string(index), "Все доступные диалоги обработаны: `true`") || !strings.Contains(string(index), "chats/user-42/messages.jsonl") {
		t.Fatalf("index = %s, %v", index, err)
	}
	if info, err := os.Stat(AccountStatePath(dir)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("account state mode = %v, %v", info, err)
	}
	for id := 3; id <= 252; id++ {
		fake.records["user:42"] = append(fake.records["user:42"], MessageRecord{Chat: chat, MessageID: id, Date: time.Now().UTC(), Text: "new", Kind: "text"})
	}
	state, err = RunAccountSync(context.Background(), fake, AccountSyncOptions{StateDir: dir})
	if err != nil || state.Dialogs[0].Records != 252 || len(readAccountRecords(t, stream)) != 252 {
		t.Fatalf("incremental account sync = %+v, %v", state, err)
	}
	if !fake.options["user:42"][1].All || fake.options["user:42"][1].MinID != 2 {
		t.Fatalf("incremental options = %+v", fake.options["user:42"][1])
	}
}

func TestRunAccountSyncRecoversUncommittedIncrementalAppend(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lumina")
	chat := Chat{ID: 42, Type: "user", Display: "Conversation"}
	fake := &fakeAccountSource{
		profile: SelfProfile{ID: 77}, chats: []Chat{chat},
		records: map[string][]MessageRecord{"user:42": {{Chat: chat, MessageID: 1, Text: "first", Kind: "text"}}},
		options: make(map[string][]HistoryOptions),
	}
	if _, err := RunAccountSync(context.Background(), fake, AccountSyncOptions{StateDir: dir, ExpectedAccountID: 77}); err != nil {
		t.Fatal(err)
	}
	stream := filepath.Join(dir, "chats", "user-42", "messages.jsonl")
	info, err := os.Stat(stream)
	if err != nil {
		t.Fatal(err)
	}
	pendingPath := filepath.Join(filepath.Dir(stream), accountIncrementalPendingName)
	pending, err := json.Marshal(accountIncrementalPending{StreamSize: info.Size(), DeltaSize: 4, OldLastID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivateAtomic(pendingPath, pending); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(stream, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("bad\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	fake.records["user:42"] = append(fake.records["user:42"], MessageRecord{Chat: chat, MessageID: 2, Text: "second", Kind: "text"})
	state, err := RunAccountSync(context.Background(), fake, AccountSyncOptions{StateDir: dir})
	if err != nil || !state.Complete {
		t.Fatalf("recovery sync = %+v, %v", state, err)
	}
	if records := readAccountRecords(t, stream); len(records) != 2 || records[1].MessageID != 2 {
		t.Fatalf("recovered stream = %+v", records)
	}
	if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
		t.Fatalf("pending marker remains: %v", err)
	}
}

func TestRunAccountSyncRefusesDifferentAuthorizedAccountBeforeChangingArchive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lumina")
	fake := &fakeAccountSource{profile: SelfProfile{ID: 77}, records: map[string][]MessageRecord{}, options: map[string][]HistoryOptions{}}
	if _, err := RunAccountSync(context.Background(), fake, AccountSyncOptions{StateDir: dir, ExpectedAccountID: 77}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(AccountStatePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	fake.profile.ID = 88
	if _, err := RunAccountSync(context.Background(), fake, AccountSyncOptions{StateDir: dir}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("different account error = %v", err)
	}
	after, err := os.ReadFile(AccountStatePath(dir))
	if err != nil || string(after) != string(before) {
		t.Fatalf("archive changed after identity failure: %v", err)
	}
}

func TestRunAccountSyncResumesPartialChat(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lumina")
	chat := Chat{ID: 42, Type: "user", Display: "Conversation"}
	fake := &fakeAccountSource{
		profile: SelfProfile{ID: 77}, chats: []Chat{chat},
		records: map[string][]MessageRecord{"user:42": {
			{Chat: chat, MessageID: 1, Text: "oldest", Kind: "text"},
			{Chat: chat, MessageID: 2, Text: "middle", Kind: "text"},
			{Chat: chat, MessageID: 3, Text: "newest", Kind: "text"},
		}}, options: make(map[string][]HistoryOptions),
	}
	chatDir := filepath.Join(dir, "chats", "user-42")
	if err := os.MkdirAll(chatDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stream := filepath.Join(chatDir, "messages.jsonl")
	enc, file, err := OpenJSONL(stream, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode(fake.records["user:42"][2]); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := SaveSyncState(filepath.Join(chatDir, "sync.state.json"), SyncState{
		Chat: chat, Records: 1, Backfill: &Backfill{Active: true, NextOffsetID: 3, LatestID: 3, Records: 1, Batches: 1},
	}); err != nil {
		t.Fatal(err)
	}
	state, err := RunAccountSync(context.Background(), fake, AccountSyncOptions{StateDir: dir, ExpectedAccountID: 77})
	if err != nil || !state.Complete || state.Dialogs[0].Records != 3 {
		t.Fatalf("resumed account sync = %+v, %v", state, err)
	}
	if fake.options["user:42"][0].StartOffsetID != 3 || !fake.options["user:42"][0].All {
		t.Fatalf("resume options = %+v", fake.options["user:42"][0])
	}
	if records := readAccountRecords(t, stream); len(records) != 3 {
		t.Fatalf("resumed stream = %+v", records)
	}
}

func readAccountRecords(t *testing.T, path string) []MessageRecord {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var records []MessageRecord
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record MessageRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return records
}
