package mtproto

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/gotd/td/tg"
)

func publishTestManifest() InterviewTopicPublishManifest {
	return InterviewTopicPublishManifest{Profile: "main", AccountID: InterviewTopicAccountID, ChannelID: InterviewTopicChannelID, TopicID: InterviewTopicID, Posts: []InterviewTopicPost{{Key: "2026-10-23-screening", Text: "Interview"}, {Key: "2026-10-23-note", Text: "Notes", ParentKey: "2026-10-23-screening"}}}
}

type publishFake struct {
	messages        map[int]*tg.Message
	sends, prepares int
	sendErr         error
	wrongRead       bool
	existing        bool
	account         int64
	beforeSend      func()
}

func (f *publishFake) SelfProfile(context.Context) (harvest.SelfProfile, error) {
	id := f.account
	if id == 0 {
		id = InterviewTopicAccountID
	}
	return harvest.SelfProfile{ID: id, Username: SavedMessagesMainUsername}, nil
}
func (f *publishFake) interviewTopicPrepare(context.Context) error { f.prepares++; return nil }
func (f *publishFake) interviewTopicRead(_ context.Context, id int) (*tg.Message, error) {
	return f.messages[id], nil
}
func (f *publishFake) publishExisting(context.Context, InterviewTopicPost, int) (bool, error) {
	return f.existing, nil
}
func (f *publishFake) publishSend(_ context.Context, p InterviewTopicPost, parent int, _ string, _ int64) (int, error) {
	f.sends++
	if f.beforeSend != nil {
		f.beforeSend()
	}
	if f.sendErr != nil {
		return 0, f.sendErr
	}
	id := 2000 + f.sends
	text := p.Text
	if f.wrongRead {
		text = "wrong"
	}
	entities, _ := topicEntities(p.Text, p.Entities)
	reply := &tg.MessageReplyHeader{ForumTopic: true}
	reply.SetReplyToMsgID(parent)
	reply.SetReplyToTopID(InterviewTopicID)
	f.messages[id] = &tg.Message{ID: id, Out: true, FromID: &tg.PeerUser{UserID: InterviewTopicAccountID}, PeerID: &tg.PeerChannel{ChannelID: InterviewTopicChannelID}, Message: text, Entities: entities, ReplyTo: reply}
	if p.File != nil {
		file := p.File
		attrs := []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: file.Filename}}
		switch file.Kind {
		case "audio":
			attrs = append(attrs, &tg.DocumentAttributeAudio{Duration: int(file.Duration)})
		case "video":
			attrs = append(attrs, &tg.DocumentAttributeVideo{Duration: file.Duration, W: file.Width, H: file.Height, Nosound: !file.HasAudio, SupportsStreaming: true})
		}
		f.messages[id].Media = &tg.MessageMediaDocument{Document: &tg.Document{MimeType: file.MIME, Size: file.Size, Attributes: attrs}}
	}
	return id, nil
}
func TestInterviewTopicPublishDryRunApplyAndRepeat(t *testing.T) {
	ctx := context.Background()
	m := publishTestManifest()
	f := &publishFake{messages: map[int]*tg.Message{}}
	j := topicPublishJournal{}
	saves := 0
	save := func() error { saves++; return nil }
	r, err := publishInterviewTopic(ctx, f, m, j, save, false)
	if err != nil || !r.Ready || r.Complete || f.sends != 0 || saves != 0 {
		t.Fatal(r, err, f.sends, saves)
	}
	f.beforeSend = func() {
		if saves == 0 {
			t.Fatal("send before durable intent")
		}
	}
	r, err = publishInterviewTopic(ctx, f, m, j, save, true)
	if err != nil || !r.Complete || f.sends != 2 || f.prepares < 3 {
		t.Fatal(r, err, f.sends, f.prepares)
	}
	r, err = publishInterviewTopic(ctx, f, m, j, save, true)
	if err != nil || !r.Complete || f.sends != 2 || !r.Rows[0].AlreadyApplied {
		t.Fatal(r, err, f.sends)
	}
	m.Posts[0].Text = "changed"
	if _, err = publishInterviewTopic(ctx, f, m, j, save, true); err == nil {
		t.Fatal("changed payload accepted")
	}
}
func TestInterviewTopicPublishUnknownStopsWithoutRetry(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		t.Run(map[bool]string{false: "send-error", true: "readback-error"}[wrong], func(t *testing.T) {
			f := &publishFake{messages: map[int]*tg.Message{}, wrongRead: wrong}
			if !wrong {
				f.sendErr = errors.New("connection lost")
			}
			j := topicPublishJournal{}
			m := publishTestManifest()
			r, err := publishInterviewTopic(context.Background(), f, m, j, func() error { return nil }, true)
			if err == nil || f.sends != 1 || !r.Rows[0].OutcomeUnknown || r.Complete {
				t.Fatal(r, err, f.sends)
			}
			_, err = publishInterviewTopic(context.Background(), f, m, j, func() error { return nil }, true)
			if err == nil || f.sends != 1 {
				t.Fatal("unknown outcome retried", err, f.sends)
			}
		})
	}
}
func TestInterviewTopicPublishGuards(t *testing.T) {
	for _, kind := range []string{"account", "existing", "parent", "file", "journal"} {
		t.Run(kind, func(t *testing.T) {
			m := publishTestManifest()
			f := &publishFake{messages: map[int]*tg.Message{}}
			j := topicPublishJournal{}
			switch kind {
			case "account":
				f.account = 123
			case "existing":
				f.existing = true
			case "parent":
				m.Posts[0].ParentID = 122
				m.Posts[0].ParentTextHash = strings.Repeat("a", 64)
			case "file":
				m.Posts[1].File = &InterviewTopicFile{Path: "/missing/file.md", Filename: "file.md", MIME: "text/markdown", Size: 2, SHA256: strings.Repeat("a", 64), Kind: "document"}
			case "journal":
				j[m.Posts[0].Key] = topicPublishReceipt{Hash: "bad", ID: 2000}
			}
			if _, err := publishInterviewTopic(context.Background(), f, m, j, func() error { return nil }, true); err == nil || f.sends != 0 {
				t.Fatal("guard allowed send", err, f.sends)
			}
		})
	}
}
func TestInterviewTopicPublishValidationAndHash(t *testing.T) {
	m := publishTestManifest()
	if err := ValidateInterviewTopicPublishManifest(m); err != nil {
		t.Fatal(err)
	}
	id := publishRandomID(m.Posts[0])
	if id <= 0 || id != publishRandomID(m.Posts[0]) || id == publishRandomID(m.Posts[1]) {
		t.Fatal("random IDs are unstable or shared")
	}
	m.Posts = append(m.Posts, InterviewTopicPost{Key: "nested", Text: "bad", ParentKey: m.Posts[1].Key})
	if ValidateInterviewTopicPublishManifest(m) == nil {
		t.Fatal("nested parent accepted")
	}
}
func TestInterviewTopicPublishFileAndJournal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.md")
	if err := os.WriteFile(path, []byte("notes"), 0600); err != nil {
		t.Fatal(err)
	}
	f := &InterviewTopicFile{Path: path, Filename: "note.md", Kind: "document", MIME: "text/markdown", Size: 5, SHA256: topicTextHash("notes")}
	if err := verifyPublishFile(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	f.SHA256 = strings.Repeat("0", 64)
	if verifyPublishFile(context.Background(), f) == nil {
		t.Fatal("corrupt file accepted")
	}
	journal := filepath.Join(dir, "receipts.json")
	if err := savePublishJournal(journal, topicPublishJournal{"key": {Hash: topicTextHash("payload"), Pending: true}}); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(journal)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
}
func TestInterviewTopicPublishReadbackMedia(t *testing.T) {
	p := InterviewTopicPost{Text: "caption", File: &InterviewTopicFile{Kind: "audio", MIME: "audio/mpeg", Filename: "a.mp3", Size: 10, Duration: 20, HasAudio: true}}
	f := &publishFake{messages: map[int]*tg.Message{}}
	id, _ := f.publishSend(context.Background(), p, InterviewTopicID, "", 1)
	m := f.messages[id]
	d := &tg.Document{MimeType: "audio/mpeg", Size: 10, Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "a.mp3"}, &tg.DocumentAttributeAudio{Duration: 20}}}
	m.Media = &tg.MessageMediaDocument{Document: d}
	if err := verifyPublishedPost(m, p, InterviewTopicID); err != nil {
		t.Fatal(err)
	}
	d.Attributes = d.Attributes[:1]
	if verifyPublishedPost(m, p, InterviewTopicID) == nil {
		t.Fatal("generic document accepted as audio")
	}
	d.Attributes = append(d.Attributes, &tg.DocumentAttributeAudio{Duration: 20, Voice: true})
	if verifyPublishedPost(m, p, InterviewTopicID) == nil {
		t.Fatal("voice accepted as audio")
	}
}

func TestInterviewTopicPublishVideoReadbackAndScope(t *testing.T) {
	p := InterviewTopicPost{Text: "video", File: &InterviewTopicFile{Kind: "video", MIME: "video/mp4", Filename: "a.mp4", Size: 100, Duration: 1.25, Width: 1920, Height: 1080, HasAudio: true}}
	for _, bad := range []string{"none", "sender", "topic", "reply", "caption", "filename", "size", "mime", "duration", "streaming", "audio", "resolution", "round"} {
		t.Run(bad, func(t *testing.T) {
			f := &publishFake{messages: map[int]*tg.Message{}}
			id, _ := f.publishSend(context.Background(), p, InterviewTopicID, "", 1)
			m := f.messages[id]
			v := &tg.DocumentAttributeVideo{Duration: 1.25, W: 1920, H: 1080, SupportsStreaming: true}
			name := &tg.DocumentAttributeFilename{FileName: "a.mp4"}
			d := &tg.Document{MimeType: "video/mp4", Size: 100, Attributes: []tg.DocumentAttributeClass{name, v}}
			m.Media = &tg.MessageMediaDocument{Document: d}
			switch bad {
			case "sender":
				m.FromID = &tg.PeerUser{UserID: 123}
			case "topic":
				m.ReplyTo.(*tg.MessageReplyHeader).SetReplyToTopID(999)
			case "reply":
				m.ReplyTo.(*tg.MessageReplyHeader).SetReplyToMsgID(999)
			case "caption":
				m.Message = "changed"
			case "filename":
				name.FileName = "b.mp4"
			case "size":
				d.Size++
			case "mime":
				d.MimeType = "application/octet-stream"
			case "duration":
				v.Duration++
			case "streaming":
				v.SupportsStreaming = false
			case "audio":
				v.Nosound = true
			case "resolution":
				v.W++
			case "round":
				v.RoundMessage = true
			}
			err := verifyPublishedPost(m, p, InterviewTopicID)
			if (err == nil) != (bad == "none") {
				t.Fatalf("%s: %v", bad, err)
			}
		})
	}
}
func TestInterviewTopicPublishJournalFailurePreventsSend(t *testing.T) {
	f := &publishFake{messages: map[int]*tg.Message{}}
	_, err := publishInterviewTopic(context.Background(), f, publishTestManifest(), topicPublishJournal{}, func() error { return errors.New("disk full") }, true)
	if err == nil || f.sends != 0 {
		t.Fatal(err, f.sends)
	}
}

func TestInterviewTopicPublishRejectsInvalidJournalBeforeRuntime(t *testing.T) {
	for _, value := range []string{"null", "{} {}", `{"x":{"hash":"bad","id":1}}`, `{"x":{"hash":"bad","recipient":123}}`} {
		t.Run(value, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "receipts.json")
			if err := os.WriteFile(path, []byte(value), 0600); err != nil {
				t.Fatal(err)
			}
			var session *Session
			if _, err := session.PublishInterviewTopic(context.Background(), publishTestManifest(), path, true); err == nil {
				t.Fatal("invalid journal accepted")
			}
		})
	}
}

func localPublishAudio(t *testing.T, dir, name string) *InterviewTopicFile {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe unavailable")
	}
	path := filepath.Join(dir, name)
	args := []string{"-v", "error", "-f", "lavfi", "-i", "sine=frequency=1000:duration=0.25", "-vn", "-y", path}
	if b, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("local audio fixture: %s: %v", b, err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mime := "audio/mp4"
	if strings.HasSuffix(name, ".mp3") {
		mime = "audio/mpeg"
	}
	return &InterviewTopicFile{Path: path, Filename: name, Kind: "audio", MIME: mime, Size: int64(len(b)), SHA256: topicTextHash(string(b)), Duration: 1, HasAudio: true}
}
func TestInterviewTopicPublishSixPosts(t *testing.T) {
	dir := t.TempDir()
	audio := localPublishAudio(t, dir, "screening.m4a")
	mdPath := filepath.Join(dir, "transcript.md")
	if err := os.WriteFile(mdPath, []byte("# Full transcript"), 0600); err != nil {
		t.Fatal(err)
	}
	md := &InterviewTopicFile{Path: mdPath, Filename: "transcript.md", Kind: "document", MIME: "text/markdown", Size: 17, SHA256: topicTextHash("# Full transcript")}
	m := publishTestManifest()
	m.Posts = nil
	for _, key := range []string{"mt-screening", "sogaz-screening"} {
		m.Posts = append(m.Posts, InterviewTopicPost{Key: key, Text: "#screening " + key, Entities: []InterviewTopicEntity{{Type: "hashtag", Offset: 0, Length: 10}}}, InterviewTopicPost{Key: key + "-audio", Text: "Audio " + key, ParentKey: key, File: audio}, InterviewTopicPost{Key: key + "-md", Text: "Transcript " + key, ParentKey: key, File: md})
	}
	f := &publishFake{messages: map[int]*tg.Message{}}
	j := topicPublishJournal{}
	ctx := context.Background()
	r, err := publishInterviewTopic(ctx, f, m, j, func() error { return nil }, false)
	if err != nil || !r.Ready || f.sends != 0 {
		t.Fatal(r, err, f.sends)
	}
	r, err = publishInterviewTopic(ctx, f, m, j, func() error { return nil }, true)
	if err != nil || !r.Complete || f.sends != 6 || len(j) != 6 {
		t.Fatal(r, err, f.sends)
	}
	r, err = publishInterviewTopic(ctx, f, m, j, func() error { return nil }, true)
	if err != nil || !r.Complete || f.sends != 6 {
		t.Fatal(r, err, f.sends)
	}
}
func TestInterviewTopicPublishRejectsAudioContainerMIMEConflict(t *testing.T) {
	dir := t.TempDir()
	file := localPublishAudio(t, dir, "wrong.wav")
	file.MIME = "audio/mpeg"
	file.Filename = "wrong.mp3"
	newPath := filepath.Join(dir, file.Filename)
	if err := os.Rename(file.Path, newPath); err != nil {
		t.Fatal(err)
	}
	file.Path = newPath
	if err := verifyPublishFile(context.Background(), file); err == nil || !strings.Contains(err.Error(), "container") {
		t.Fatal("WAV falsely declared as MP3 passed preflight", err)
	}
}

func TestInterviewTopicPublishKnownPendingReadbackClearsPending(t *testing.T) {
	m := publishTestManifest()
	m.Posts = m.Posts[:1]
	f := &publishFake{messages: map[int]*tg.Message{}}
	id, _ := f.publishSend(context.Background(), m.Posts[0], InterviewTopicID, "", 1)
	j := topicPublishJournal{m.Posts[0].Key: {Hash: publishPostHash(m.Posts[0]), ID: id, Pending: true}}
	saves := 0
	r, err := publishInterviewTopic(context.Background(), f, m, j, func() error { saves++; return nil }, true)
	if err != nil || !r.Complete || j[m.Posts[0].Key].Pending || saves != 1 || f.sends != 1 {
		t.Fatal(r, err, j, saves, f.sends)
	}
}
