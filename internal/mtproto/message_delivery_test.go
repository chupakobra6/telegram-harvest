package mtproto

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

type messageWire struct {
	t           *testing.T
	account     MessageAccount
	peer        tg.PeerClass
	user        *tg.User
	group       *tg.Channel
	topic       *tg.ForumTopic
	parent      *tg.Message
	draft       tg.DraftMessageClass
	messages    map[int]*tg.Message
	files       map[int64][]byte
	uploads     map[int64][]byte
	sent        []string
	sendCount   int
	draftWrites int
	failAt      int
	onUpload    func() error
	beforeSend  func(int)
	lastReply   tg.InputReplyToClass
	sentMedia   []*tg.InputMediaUploadedDocument
}

func (w *messageWire) Invoke(_ context.Context, input bin.Encoder, output bin.Decoder) error {
	var result bin.Encoder
	switch req := input.(type) {
	case *tg.UsersGetFullUserRequest:
		result = &tg.UsersUserFull{
			FullUser: tg.UserFull{ID: w.account.ID},
			Users:    []tg.UserClass{&tg.User{ID: w.account.ID, Username: w.account.Username}},
		}
	case *tg.ContactsResolveUsernameRequest:
		if req.Username != w.user.Username {
			w.t.Fatalf("unexpected username %q", req.Username)
		}
		result = &tg.ContactsResolvedPeer{Peer: w.peer, Users: []tg.UserClass{w.user}}
	case *tg.UsersGetUsersRequest:
		if req.ID[0].(*tg.InputUser).UserID != w.user.ID {
			w.t.Fatal("wrong user lookup")
		}
		result = &tg.UserClassVector{Elems: []tg.UserClass{w.user}}
	case *tg.ChannelsGetChannelsRequest:
		result = &tg.MessagesChats{Chats: []tg.ChatClass{w.group}}
	case *tg.MessagesGetForumTopicsByIDRequest:
		if w.topic == nil || !reflect.DeepEqual(req.Topics, []int{w.topic.ID}) {
			w.t.Fatalf("unexpected topic read: %v", req.Topics)
		}
		result = &tg.MessagesForumTopics{Topics: []tg.ForumTopicClass{w.topic}}
	case *tg.MessagesGetMessagesRequest:
		result = w.read(req.ID[0].(*tg.InputMessageID).ID)
	case *tg.ChannelsGetMessagesRequest:
		result = w.read(req.ID[0].(*tg.InputMessageID).ID)
	case *tg.MessagesSendMessageRequest:
		if req.ClearDraft || !req.NoWebpage || req.AllowPaidStars != 0 || req.RandomID == 0 {
			w.t.Fatalf("unexpected text send flags: %+v", req)
		}
		if w.group != nil {
			if _, ok := req.SendAs.(*tg.InputPeerSelf); !ok {
				w.t.Fatal("group send did not pin own sender")
			}
		}
		return w.send(output, req.RandomID, req.ReplyTo, req.Message, req.Entities, nil)
	case *tg.UploadSaveFilePartRequest:
		if req.FilePart != 0 {
			w.t.Fatalf("unexpected upload part %d", req.FilePart)
		}
		if w.onUpload != nil {
			if err := w.onUpload(); err != nil {
				return err
			}
		}
		w.uploads[req.FileID] = bytes.Clone(req.Bytes)
		result = &tg.BoolTrue{}
	case *tg.MessagesSendMediaRequest:
		if req.ClearDraft || req.AllowPaidStars != 0 || req.RandomID == 0 {
			w.t.Fatalf("unexpected media send flags: %+v", req)
		}
		media := req.Media.(*tg.InputMediaUploadedDocument)
		w.sentMedia = append(w.sentMedia, media)
		return w.send(output, req.RandomID, req.ReplyTo, req.Message, req.Entities, media)
	case *tg.UploadGetFileRequest:
		id := req.Location.(*tg.InputDocumentFileLocation).ID
		data, ok := w.files[id]
		if !ok || req.Offset != 0 {
			w.t.Fatalf("unexpected server file readback %d", id)
		}
		result = &tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: data}
	case *tg.MessagesGetPeerDialogsRequest:
		dialog := &tg.Dialog{Peer: w.peer}
		if w.draft != nil {
			dialog.SetDraft(w.draft)
		}
		result = &tg.MessagesPeerDialogs{Dialogs: []tg.DialogClass{dialog}}
	case *tg.MessagesSaveDraftRequest:
		w.draftWrites++
		if req.Media != nil || req.Message == "" || !req.NoWebpage {
			w.t.Fatalf("unexpected native draft: %+v", req)
		}
		draft := &tg.DraftMessage{Message: req.Message, Entities: req.Entities, ReplyTo: req.ReplyTo, NoWebpage: true}
		if w.topic != nil {
			w.topic.SetDraft(draft)
		} else {
			w.draft = draft
		}
		result = &tg.BoolTrue{}
	default:
		w.t.Fatalf("unexpected RPC %T", input)
	}
	return encodeMessageWire(result, output)
}

func encodeMessageWire(input bin.Encoder, output bin.Decoder) error {
	var buf bin.Buffer
	if err := input.Encode(&buf); err != nil {
		return err
	}
	return output.Decode(&buf)
}

func (w *messageWire) read(id int) bin.Encoder {
	m := w.messages[id]
	if w.parent != nil && id == w.parent.ID {
		m = w.parent
	}
	if m == nil {
		w.t.Fatalf("unexpected message lookup %d", id)
	}
	return &tg.MessagesMessages{Messages: []tg.MessageClass{m}}
}

func (w *messageWire) send(out bin.Decoder, randomID int64, reply tg.InputReplyToClass, text string, entities []tg.MessageEntityClass, media *tg.InputMediaUploadedDocument) error {
	w.sendCount++
	w.lastReply = reply
	if w.beforeSend != nil {
		w.beforeSend(w.sendCount)
	}
	if w.sendCount == w.failAt {
		return errors.New("send acknowledgement lost")
	}
	id := w.sendCount + 100
	message := &tg.Message{ID: id, Out: true, PeerID: w.peer, FromID: &tg.PeerUser{UserID: w.account.ID}, Message: text, Entities: entities}
	if r, ok := reply.(*tg.InputReplyToMessage); ok {
		message.SetReplyTo(&tg.MessageReplyHeader{ReplyToMsgID: r.ReplyToMsgID, ReplyToTopID: r.TopMsgID, ForumTopic: w.topic != nil && w.topic.ID > 1})
	}
	name := text
	if media != nil {
		f := media.File.(*tg.InputFile)
		data, ok := w.uploads[f.ID]
		if !ok {
			w.t.Fatalf("attachment %s was not uploaded", f.Name)
		}
		name = f.Name
		doc := &tg.Document{ID: int64(id), Size: int64(len(data)), MimeType: media.MimeType, Attributes: media.Attributes}
		message.SetMedia(&tg.MessageMediaDocument{Document: doc})
		w.files[doc.ID] = data
	}
	w.messages[id] = message
	w.sent = append(w.sent, name)
	return encodeMessageWire(&tg.Updates{Updates: []tg.UpdateClass{
		&tg.UpdateMessageID{ID: 700, RandomID: randomID + 1},
		&tg.UpdateMessageID{ID: id, RandomID: randomID},
	}}, out)
}

func newMessageWire(t *testing.T) (*Session, *messageWire, MessageAccount) {
	t.Helper()
	account := MessageAccount{Profile: "work", ID: 17, Username: "worker"}
	w := &messageWire{t: t, account: account, peer: &tg.PeerUser{UserID: 42},
		user:     &tg.User{ID: 42, Username: "colleague", FirstName: "Colleague", AccessHash: 9},
		messages: map[int]*tg.Message{}, files: map[int64][]byte{}, uploads: map[int64][]byte{}}
	transport := telegram.MiddlewareFunc(func(tg.Invoker) telegram.InvokeFunc { return w.Invoke })
	s := &Session{raw: tg.NewClient(w), dialogCache: map[string]resolvedTarget{},
		client: telegram.NewClient(1, "fixture", telegram.Options{Middlewares: []telegram.Middleware{transport}})}
	return s, w, account
}

func makeDeliveryFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMessageDeliveryOrderedOriginalFilesAndRestart(t *testing.T) {
	s, w, account := newMessageWire(t)
	audio := makeDeliveryFile(t, "audio.m4a", "fixture bytes for original audio document")
	transcript := makeDeliveryFile(t, "transcript.md", "fixture transcript")
	input := MessagePrepareInput{To: "@colleague", Text: "Готово", Files: []MessageFileInput{{Path: audio}, {Path: transcript}}}
	m, err := s.PrepareMessage(t.Context(), input, account)
	if err != nil || m.Destination.ID != 42 || m.ReplyTo != 0 || m.Files[0].MIME != "audio/mp4" ||
		m.Files[1].SHA256 != topicTextHash("fixture transcript") || !validSHA256(m.Fingerprint) {
		t.Fatalf("prepare: %+v, %v", m, err)
	}
	journalDir := filepath.Join(t.TempDir(), "work")
	r, err := s.DeliverMessage(t.Context(), m, account, journalDir, false)
	if err != nil || !r.Ready || r.Complete || len(r.Rows) != 3 || len(w.uploads)+w.sendCount != 0 {
		t.Fatalf("preview: %+v, %v", r, err)
	}
	if _, err := os.Stat(journalDir); !os.IsNotExist(err) {
		t.Fatalf("preview changed the journal directory: %v", err)
	}
	intent, _ := messageIntent(m)
	w.beforeSend = func(index int) {
		journal, err := loadPublicationJournal(filepath.Join(journalDir, intent+".receipts.json"), 0)
		receipt := journal[fmt.Sprint(index)]
		if err != nil || !receipt.Pending || receipt.Hash != intent || receipt.ID != 0 {
			t.Fatalf("send without durable intent: %+v, %v", receipt, err)
		}
	}
	r, err = s.DeliverMessage(t.Context(), m, account, journalDir, true)
	if err != nil || !r.Complete || !reflect.DeepEqual(w.sent, []string{"Готово", "audio.m4a", "transcript.md"}) ||
		r.Rows[2].MessageID != 103 || r.Rows[2].ServerFilename != "transcript.md" || !r.Rows[2].ServerVerified ||
		len(w.sentMedia) != 2 || !w.sentMedia[0].ForceFile || !w.sentMedia[1].ForceFile {
		t.Fatalf("apply: %+v, %v, sent=%v", r, err, w.sent)
	}
	if err := os.Remove(audio); err != nil {
		t.Fatal(err)
	}
	r, err = s.DeliverMessage(t.Context(), m, account, journalDir, true)
	if err != nil || !r.Complete || r.Rows[1].Status != "already_sent" || w.sendCount != 3 || len(w.uploads) != 2 {
		t.Fatalf("replay: %+v, %v", r, err)
	}
	w.files[103] = []byte("changed transcript")
	r, err = s.DeliverMessage(t.Context(), m, account, journalDir, true)
	if err == nil || r.Complete || r.Status != "outcome_unknown" || w.sendCount != 3 {
		t.Fatalf("different server bytes were accepted: %+v, %v", r, err)
	}
}

func TestMessageDeliveryPartialUnknownNeverResends(t *testing.T) {
	s, w, account := newMessageWire(t)
	w.failAt = 2
	input := MessagePrepareInput{To: "@colleague", Text: "ready", Files: []MessageFileInput{{Path: makeDeliveryFile(t, "file.txt", "text")}}}
	m, err := s.PrepareMessage(t.Context(), input, account)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for i := 0; i < 2; i++ {
		r, err := s.DeliverMessage(t.Context(), m, account, dir, true)
		if err == nil || r.Complete || r.Status != "outcome_unknown" || len(r.Rows) != 2 || !r.Rows[0].ServerVerified ||
			r.Rows[1].MessageID != 0 || r.Rows[1].Status != "outcome_unknown" || w.sendCount != 2 || len(w.uploads) != 1 {
			t.Fatalf("pass %d: %+v, %v, sends=%d uploads=%d", i, r, err, w.sendCount, len(w.uploads))
		}
	}
}

func TestMessageDeliveryFailedUploadLeavesNoSendIntent(t *testing.T) {
	s, w, account := newMessageWire(t)
	input := MessagePrepareInput{To: "@colleague", Files: []MessageFileInput{{Path: makeDeliveryFile(t, "file.txt", "text")}}}
	m, err := s.PrepareMessage(t.Context(), input, account)
	if err != nil {
		t.Fatal(err)
	}
	w.onUpload = func() error { return errors.New("upload failed") }
	dir := t.TempDir()
	r, err := s.DeliverMessage(t.Context(), m, account, dir, true)
	if err == nil || r.Status != "blocked" || w.sendCount != 0 {
		t.Fatalf("failed upload: %+v, %v", r, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("upload failure left send intent: %v %v", entries, err)
	}
	w.onUpload = nil
	if r, err = s.DeliverMessage(t.Context(), m, account, dir, true); err != nil || !r.Complete || w.sendCount != 1 {
		t.Fatalf("retry after failed upload: %+v, %v", r, err)
	}
}

func TestMessageDeliveryForumAndExactReply(t *testing.T) {
	for _, tc := range []struct {
		name                               string
		topic, parent, wantParent, wantTop int
	}{
		{"new_topic_post", 9, 0, 9, 0}, {"reply_in_topic", 9, 33, 33, 9},
		{"new_general_post", 1, 0, 0, 0}, {"reply_in_general", 1, 33, 33, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, w, account := newMessageWire(t)
			w.group = &tg.Channel{ID: 71, Title: "Group", Megagroup: true, Forum: true, Photo: &tg.ChatPhotoEmpty{}}
			w.peer = &tg.PeerChannel{ChannelID: 71}
			w.topic = &tg.ForumTopic{ID: tc.topic, Title: "Topic", Peer: w.peer, FromID: &tg.PeerUser{UserID: 55}}
			s.cacheTarget(harvest.Chat{ID: 71, Type: "supergroup", Forum: true}, &tg.InputPeerChannel{ChannelID: 71})
			if tc.parent > 0 {
				w.parent = &tg.Message{ID: tc.parent, PeerID: w.peer, FromID: &tg.PeerUser{UserID: 55}, Message: "Question"}
				if tc.topic > 1 {
					w.parent.SetReplyTo(&tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: tc.topic})
				}
			}
			m, err := s.PrepareMessage(t.Context(), MessagePrepareInput{To: "supergroup:71", TopicID: tc.topic, ReplyTo: tc.parent, Text: "Answer"}, account)
			if err != nil {
				t.Fatal(err)
			}
			r, err := s.DeliverMessage(t.Context(), m, account, t.TempDir(), true)
			if err != nil || !r.Complete {
				t.Fatalf("topic result: %+v, %v", r, err)
			}
			if tc.wantParent == 0 {
				if w.lastReply != nil {
					t.Fatal("general post should have no wire reply")
				}
				return
			}
			header := w.lastReply.(*tg.InputReplyToMessage)
			if header.ReplyToMsgID != tc.wantParent || header.TopMsgID != tc.wantTop {
				t.Fatalf("unexpected topic reply: %+v", header)
			}
		})
	}
}

func TestMessageDeliveryChangedParentAndStudyGuards(t *testing.T) {
	s, w, account := newMessageWire(t)
	w.parent = &tg.Message{ID: 55, PeerID: w.peer, Message: "Question"}
	input := MessagePrepareInput{To: "@colleague", ReplyTo: 55, Text: "Answer"}
	m, err := s.PrepareMessage(t.Context(), input, account)
	if err != nil {
		t.Fatal(err)
	}
	w.parent.PeerID = &tg.PeerUser{UserID: 77}
	if r, err := s.DeliverMessage(t.Context(), m, account, t.TempDir(), true); err == nil || r.Ready || w.sendCount != 0 {
		t.Fatalf("accepted globally matching ID from a different peer: %+v, %v", r, err)
	}
	w.parent.PeerID = w.peer
	w.parent.Message = "Changed"
	if r, err := s.DeliverMessage(t.Context(), m, account, t.TempDir(), true); err == nil || r.Ready || w.sendCount != 0 {
		t.Fatalf("accepted edited parent: %+v, %v", r, err)
	}
	if _, err := (&Session{}).PrepareMessage(t.Context(), input, MessageAccount{Profile: "study", ID: 17}); err == nil {
		t.Fatal("study should fail before connecting")
	}
	if _, err := (&Session{}).PrepareMessage(t.Context(), input, MessageAccount{Profile: "alias", ID: 6889183661}); err == nil {
		t.Fatal("canonical study ID should fail before connecting, even without its original username")
	}
	w.account.Username = "Pheik15"
	if _, err := s.PrepareMessage(t.Context(), input, MessageAccount{Profile: "alias", ID: 17}); err == nil {
		t.Fatal("registered alias of study identity accepted")
	}
}

func TestMessageDeliveryExplicitRepeat(t *testing.T) {
	s, w, account := newMessageWire(t)
	input := MessagePrepareInput{To: "@colleague", Text: "Reminder"}
	dir := t.TempDir()
	for i, intent := range []string{"", "", "next-reminder", "next-reminder"} {
		input.Intent = intent
		m, err := s.PrepareMessage(t.Context(), input, account)
		if err != nil {
			t.Fatal(err)
		}
		result, err := s.DeliverMessage(t.Context(), m, account, dir, true)
		if err != nil || !result.Complete || w.sendCount != i/2+1 {
			t.Fatalf("attempt %d: %+v, %v, sends=%d", i, result, err, w.sendCount)
		}
	}
}

func TestMessageDeliveryNativeDraftIdempotence(t *testing.T) {
	s, w, account := newMessageWire(t)
	input := MessagePrepareInput{To: "@colleague", Text: "Проверенный текст", Draft: true}
	m, err := s.PrepareMessage(t.Context(), input, account)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.SaveMessageDraft(t.Context(), m, account, false)
	if err != nil || !r.Ready || r.Complete || w.draftWrites != 0 {
		t.Fatalf("draft preview: %+v, %v", r, err)
	}
	r, err = s.SaveMessageDraft(t.Context(), m, account, true)
	if err != nil || !r.Complete || r.Status != "draft_saved" || w.draftWrites != 1 || w.sendCount+len(w.uploads) != 0 {
		t.Fatalf("draft apply: %+v, %v", r, err)
	}
	r, err = s.SaveMessageDraft(t.Context(), m, account, true)
	if err != nil || !r.Complete || r.Status != "draft_already_present" || w.draftWrites != 1 {
		t.Fatalf("draft replay: %+v, %v", r, err)
	}
	w.draft = &tg.DraftMessage{Message: "manual draft"}
	r, err = s.SaveMessageDraft(t.Context(), m, account, true)
	if err == nil || r.Ready || r.Status != "draft_conflict" || w.draftWrites != 1 {
		t.Fatalf("draft overwrite: %+v, %v", r, err)
	}
}

func TestMessageDeliveryForumDraft(t *testing.T) {
	s, w, account := newMessageWire(t)
	w.group = &tg.Channel{ID: 71, Title: "Group", Megagroup: true, Forum: true, Photo: &tg.ChatPhotoEmpty{}}
	w.peer = &tg.PeerChannel{ChannelID: 71}
	w.topic = &tg.ForumTopic{ID: 9, Title: "Topic", Peer: w.peer, FromID: &tg.PeerUser{UserID: 55}}
	s.cacheTarget(harvest.Chat{ID: 71, Type: "supergroup", Forum: true}, &tg.InputPeerChannel{ChannelID: 71})
	m, err := s.PrepareMessage(t.Context(), MessagePrepareInput{To: "supergroup:71", TopicID: 9, Draft: true, Text: "Draft"}, account)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.SaveMessageDraft(t.Context(), m, account, true)
	if err != nil || !result.Complete || w.draftWrites != 1 || w.sendCount != 0 {
		t.Fatalf("topic draft: %+v, %v", result, err)
	}
}

func TestMessageDeliveryNativeMediaWire(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is required")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is required")
	}
	s, w, account := newMessageWire(t)
	audio := filepath.Join(t.TempDir(), "audio.m4a")
	video := filepath.Join(t.TempDir(), "clip.mp4")
	for _, fixture := range []struct{ source, codec, path string }{
		{"sine=frequency=400:duration=0.3", "aac", audio},
		{"color=black:s=32x24:r=5:d=0.4", "mpeg4", video},
	} {
		if out, err := exec.CommandContext(t.Context(), "ffmpeg", "-v", "error", "-f", "lavfi", "-i", fixture.source, "-c", fixture.codec, fixture.path).CombinedOutput(); err != nil {
			t.Fatalf("prepare media fixture: %v %s", err, out)
		}
	}
	m, err := s.PrepareMessage(t.Context(), MessagePrepareInput{To: "@colleague", Files: []MessageFileInput{
		{Path: audio, Kind: "audio"}, {Path: video, Kind: "video"},
	}}, account)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.DeliverMessage(t.Context(), m, account, t.TempDir(), true)
	if err != nil || !result.Complete || len(w.sentMedia) != 2 || !result.Rows[0].ServerVerified || !result.Rows[1].ServerVerified {
		t.Fatalf("native media: %+v, %v", result, err)
	}
	audioMedia, videoMedia := w.sentMedia[0], w.sentMedia[1]
	audioAttr, audioOK := audioMedia.Attributes[1].(*tg.DocumentAttributeAudio)
	videoAttr, videoOK := videoMedia.Attributes[1].(*tg.DocumentAttributeVideo)
	if audioMedia.ForceFile || videoMedia.ForceFile || !audioOK || audioAttr.Voice || audioAttr.Duration != 1 ||
		!videoOK || !videoAttr.SupportsStreaming || !videoAttr.Nosound || videoAttr.W != 32 || videoAttr.H != 24 {
		t.Fatalf("incorrect native audio/video wire attrs: %+v %+v", audioMedia, videoMedia)
	}
}

func TestMessageDeliveryOfficeAndFileGuards(t *testing.T) {
	for _, tc := range []struct{ name, part, mime string }{
		{"Report.docx", "word/document.xml", docxMIME},
		{"Slides.pptx", "ppt/presentation.xml", pptxMIME},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testMessageOfficePackage(t, tc.name, tc.part, tc.mime)
		})
	}
	for _, tc := range []struct{ name, text, mime string }{
		{"report.csv", "field,value\na,3\n", "text/csv"},
		{"report.tsv", "field\tvalue\na\t3\n", "text/tab-separated-values"},
		{"report.json", "{\"field\":3}", "application/json"},
	} {
		path := makeDeliveryFile(t, tc.name, tc.text)
		file, err := prepareMessageFile(t.Context(), MessageFileInput{Path: path})
		if err != nil || file.MIME != tc.mime || file.SHA256 != topicTextHash(tc.text) {
			t.Fatalf("valid %s: %+v, %v", tc.name, file, err)
		}
	}
}

func testMessageOfficePackage(t *testing.T, name, partPath, mime string) {
	t.Helper()
	var data bytes.Buffer
	archive := zip.NewWriter(&data)
	for partName, content := range map[string]string{
		"[Content_Types].xml": `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/` + partPath + `" ContentType="` + mime + `.main+xml"/></Types>`,
		partPath:              `<office-part/>`,
	} {
		part, err := archive.Create(partName)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	path := makeDeliveryFile(t, name, data.String())
	file, err := prepareMessageFile(t.Context(), MessageFileInput{Path: path})
	if err != nil || file.MIME != mime || file.SHA256 != topicTextHash(data.String()) {
		t.Fatalf("valid %s: %+v, %v", name, file, err)
	}
	if _, err := prepareMessageFile(t.Context(), MessageFileInput{Path: makeDeliveryFile(t, name, "no archive")}); err == nil {
		t.Fatalf("malformed %s accepted", name)
	}
}
