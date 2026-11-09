package mtproto

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func printShopFixture(t *testing.T) (PrintShopPublishManifest, *printShopFake) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "лист.pdf")
	data := []byte("%PDF-1.7\nprivate fixture\n%%EOF\n")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	m := PrintShopPublishManifest{Profile: "main", AccountID: InterviewTopicAccountID, ChannelID: PrintShopChannelID, Posts: []PrintShopPost{{Key: "notice-v1", Text: "Лист готов"}, {Key: "sheet-v1", Text: "📄 Лист", Entities: []InterviewTopicEntity{{Type: "bold", Offset: 3, Length: 4}}, File: &PrintShopPDF{Path: path, Filename: "лист.pdf", MIME: "application/pdf", SHA256: topicTextHash(string(data)), Size: int64(len(data))}}}}
	return m, &printShopFake{messages: map[int]*tg.Message{}, serverBytes: map[int64][]byte{}, ids: []int64{InterviewTopicAccountID, PrintShopArinaID}}
}

type printShopFake struct {
	serverBytes         map[int64][]byte
	downloadCalls       int
	downloadErr         error
	downloadedDocuments []*tg.Document

	messages            map[int]*tg.Message
	ids                 []int64
	account             int64
	username            string
	sends, prepares     int
	sentKeys            []string
	existing, wrongRead bool
	normalizeFilename   bool
	sendErr             error
	beforeSend          func()
}

func (f *printShopFake) SelfProfile(context.Context) (harvest.SelfProfile, error) {
	id := f.account
	if id == 0 {
		id = InterviewTopicAccountID
	}
	u := f.username
	if u == "" {
		u = SavedMessagesMainUsername
	}
	return harvest.SelfProfile{ID: id, Username: u}, nil
}
func (f *printShopFake) printShopPrepare(context.Context) ([]int64, error) {
	f.prepares++
	return f.ids, nil
}
func (f *printShopFake) printShopRead(_ context.Context, id int) (*tg.Message, error) {
	return f.messages[id], nil
}
func (f *printShopFake) printShopExisting(context.Context, PrintShopPost) (bool, error) {
	return f.existing, nil
}
func (f *printShopFake) printShopSend(_ context.Context, p PrintShopPost, _ int64) (int, error) {
	f.sends++
	f.sentKeys = append(f.sentKeys, p.Key)
	if f.beforeSend != nil {
		f.beforeSend()
	}
	if f.sendErr != nil {
		return 0, f.sendErr
	}
	id := f.sends // Group message IDs may be below the interview topic ID.
	entities, _ := topicEntities(p.Text, p.effectiveEntities())
	msg := &tg.Message{ID: id, Out: true, FromID: &tg.PeerUser{UserID: InterviewTopicAccountID}, PeerID: &tg.PeerChannel{ChannelID: PrintShopChannelID}, Message: p.Text, Entities: entities}
	if f.wrongRead {
		msg.Message = "different"
	}
	if p.File != nil {
		v := p.File
		data, err := os.ReadFile(v.Path)
		if err != nil {
			return 0, err
		}
		f.serverBytes[int64(id)] = data
		name := v.Filename
		if f.normalizeFilename {
			name = strings.ReplaceAll(name, " ", "_")
		}
		attrs := []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: name}}
		if v.MIME == "video/mp4" {
			attrs = append(attrs, &tg.DocumentAttributeVideo{Duration: v.Duration, W: v.Width, H: v.Height, SupportsStreaming: true, Nosound: !v.HasAudio})
		}
		msg.Media = &tg.MessageMediaDocument{Document: &tg.Document{ID: int64(id), MimeType: v.MIME, Size: v.Size, Attributes: attrs}}
	}
	f.messages[id] = msg
	return id, nil
}
func (f *printShopFake) printShopDownloadFile(ctx context.Context, document *tg.Document, output io.Writer) error {
	f.downloadCalls++
	f.downloadedDocuments = append(f.downloadedDocuments, document)
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.downloadErr != nil {
		_, _ = output.Write([]byte("partial"))
		return f.downloadErr
	}
	data, ok := f.serverBytes[document.ID]
	if !ok {
		return errors.New("server document unavailable")
	}
	_, err := output.Write(data)
	return err
}

func TestPrintShopPublishPreviewApplyDurableRepeat(t *testing.T) {
	m, f := printShopFixture(t)
	ctx := context.Background()
	j := topicPublishJournal{}
	path := filepath.Join(t.TempDir(), "receipts.json")
	saves := 0
	save := func() error { saves++; return savePublishJournal(path, j) }
	r, err := publishPrintShop(ctx, f, m, j, save, false)
	if err != nil || !r.Ready || r.Complete || !r.DryRun || f.sends != 0 || saves != 0 || len(r.Rows) != 2 || r.Title != PrintShopTitle || !reflect.DeepEqual(r.Rows[1].Post, m.Posts[1]) || r.Rows[1].Status != "would_send" {
		t.Fatal(r, err, f.sends, saves)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("dry run wrote receipts", err)
	}
	f.beforeSend = func() {
		loaded, err := loadPublicationJournal(path, 0)
		if err != nil || len(loaded) == 0 || !j[m.Posts[f.sends-1].Key].Pending {
			t.Fatal("send preceded durable intent", loaded, err)
		}
	}
	r, err = publishPrintShop(ctx, f, m, j, save, true)
	if err != nil || !r.Complete || f.sends != 2 || !r.Rows[1].ServerVerified || f.downloadCalls != 1 {
		t.Fatal(r, err, f.sends)
	}
	// A fresh process reloads the journal and reads server IDs, never sends again.
	loaded, err := loadPublicationJournal(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	r, err = publishPrintShop(ctx, f, m, loaded, save, true)
	if err != nil || !r.Complete || f.sends != 2 || r.Rows[0].Status != "already_applied" || f.downloadCalls != 2 {
		t.Fatal(r, err, f.sends)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
}
func TestPrintShopPublishUnknownNeverRetries(t *testing.T) {
	for _, bad := range []string{"send", "readback"} {
		t.Run(bad, func(t *testing.T) {
			m, f := printShopFixture(t)
			j := topicPublishJournal{}
			if bad == "send" {
				f.sendErr = errors.New("connection lost")
			} else {
				f.wrongRead = true
			}
			r, err := publishPrintShop(context.Background(), f, m, j, func() error { return nil }, true)
			if err == nil || r.Complete || f.sends != 1 || !r.Rows[0].OutcomeUnknown {
				t.Fatal(r, err, f.sends)
			}
			_, err = publishPrintShop(context.Background(), f, m, j, func() error { return nil }, true)
			if err == nil || f.sends != 1 {
				t.Fatal("unknown retried", err, f.sends)
			}
		})
	}
}
func TestPrintShopPublishAllInputsBeforeFirstSend(t *testing.T) {
	for _, bad := range []string{"account", "username", "arina", "duplicate-participant", "hash", "size", "header", "existing", "receipt", "payload", "journal-save"} {
		t.Run(bad, func(t *testing.T) {
			m, f := printShopFixture(t)
			j := topicPublishJournal{}
			save := func() error { return nil }
			switch bad {
			case "account":
				f.account = 123
			case "username":
				f.username = "other"
			case "arina":
				f.ids = f.ids[:1]
			case "duplicate-participant":
				f.ids = append(f.ids, f.ids[0])
			case "hash":
				m.Posts[1].File.SHA256 = strings.Repeat("0", 64)
			case "size":
				m.Posts[1].File.Size++
			case "header":
				b := []byte("not a pdf")
				_ = os.WriteFile(m.Posts[1].File.Path, b, 0600)
				m.Posts[1].File.Size = int64(len(b))
				m.Posts[1].File.SHA256 = topicTextHash(string(b))
			case "existing":
				f.existing = true
			case "receipt":
				j[m.Posts[0].Key] = topicPublishReceipt{Hash: publishPostHash(m.Posts[0].publicationPost()), Pending: true}
			case "payload":
				j[m.Posts[0].Key] = topicPublishReceipt{Hash: topicTextHash("different"), ID: 1}
			case "journal-save":
				save = func() error { return errors.New("disk full") }
			}
			_, err := publishPrintShop(context.Background(), f, m, j, save, true)
			if err == nil || f.sends != 0 {
				t.Fatal("invalid input reached send", err, f.sends)
			}
		})
	}
}
func TestPrintShopPublishManifestScopeUTF16(t *testing.T) {
	for _, bad := range []string{"valid", "profile", "account", "channel", "empty", "key", "duplicate", "text-limit", "caption-limit", "surrogate", "mime", "extension", "relative", "filename"} {
		t.Run(bad, func(t *testing.T) {
			m, _ := printShopFixture(t)
			switch bad {
			case "profile":
				m.Profile = "study"
			case "account":
				m.AccountID++
			case "channel":
				m.ChannelID++
			case "empty":
				m.Posts = nil
			case "key":
				m.Posts[0].Key = "bad/key"
			case "duplicate":
				m.Posts[1].Key = m.Posts[0].Key
			case "text-limit":
				m.Posts[0].Text = strings.Repeat("😀", 2049)
			case "caption-limit":
				m.Posts[1].Text = strings.Repeat("😀", 513)
				m.Posts[1].Entities = nil
			case "surrogate":
				m.Posts[1].Entities[0].Offset = 1
			case "mime":
				m.Posts[1].File.MIME = "application/zip"
			case "extension":
				m.Posts[1].File.Filename = "file.zip"
			case "relative":
				m.Posts[1].File.Path = "лист.pdf"
			case "filename":
				m.Posts[1].File.Filename = "other.pdf"
			}
			err := ValidatePrintShopPublishManifest(m)
			if (err == nil) != (bad == "valid") {
				t.Fatal(bad, err)
			}
		})
	}
	a := printShopRandomID("sheet-v1")
	if a <= 0 || a != printShopRandomID("sheet-v1") || a == printShopRandomID("sheet-v2") || a == publishRandomID(InterviewTopicPost{Key: "sheet-v1"}) {
		t.Fatal("random ID domain/key unstable")
	}
}
func TestPrintShopPublishStrictReadback(t *testing.T) {
	for _, bad := range []string{"valid", "sender", "peer", "out", "text", "entities", "reply", "album", "filename", "mime", "size", "audio", "missing-media"} {
		t.Run(bad, func(t *testing.T) {
			m, f := printShopFixture(t)
			p := m.Posts[1]
			id, _ := f.printShopSend(context.Background(), p, 1)
			msg := f.messages[id]
			doc := msg.Media.(*tg.MessageMediaDocument).Document.(*tg.Document)
			switch bad {
			case "sender":
				msg.FromID = &tg.PeerUser{UserID: PrintShopArinaID}
			case "peer":
				msg.PeerID = &tg.PeerChannel{ChannelID: InterviewTopicChannelID}
			case "out":
				msg.Out = false
			case "text":
				msg.Message += "x"
			case "entities":
				msg.Entities = nil
			case "reply":
				msg.ReplyTo = &tg.MessageReplyHeader{ReplyToMsgID: 1}
			case "album":
				msg.GroupedID = 1
			case "filename":
				doc.Attributes = nil
			case "mime":
				doc.MimeType = "text/plain"
			case "size":
				doc.Size++
			case "audio":
				doc.Attributes = append(doc.Attributes, &tg.DocumentAttributeAudio{})
			case "missing-media":
				msg.Media = nil
			}
			err := verifyPrintShopPost(msg, p)
			if (err == nil) != (bad == "valid") {
				t.Fatal(bad, err)
			}
		})
	}
}

// This invoker exercises the real transport boundary: no live Telegram messages.
type printShopWire struct {
	t                 *testing.T
	sends, uploads    int
	participantsCalls int
	allowUpload       bool
	video             bool
	uploaded          []byte
	afterUpload       func()
	messageEntities   []tg.MessageEntityClass
	bad               string
}

func (f *printShopWire) Invoke(_ context.Context, input bin.Encoder, output bin.Decoder) error {
	var response bin.Encoder
	switch req := input.(type) {
	case *tg.UsersGetFullUserRequest:
		response = &tg.UsersUserFull{FullUser: tg.UserFull{ID: InterviewTopicAccountID}, Users: []tg.UserClass{&tg.User{ID: InterviewTopicAccountID, Username: SavedMessagesMainUsername}}}
	case *tg.ChannelsGetFullChannelRequest:
		channel, ok := req.Channel.(*tg.InputChannel)
		if !ok || channel.ChannelID != PrintShopChannelID {
			f.t.Fatal("wrong channel", req)
		}
		title := PrintShopTitle
		if f.bad == "title" {
			title = "other"
		}
		response = &tg.MessagesChatFull{FullChat: &tg.ChannelFull{ID: PrintShopChannelID, ChatPhoto: &tg.PhotoEmpty{}}, Chats: []tg.ChatClass{&tg.Channel{ID: PrintShopChannelID, Title: title, Megagroup: true, Photo: &tg.ChatPhotoEmpty{}}}}
	case *tg.ChannelsGetParticipantsRequest:
		f.participantsCalls++
		channel, ok := req.Channel.(*tg.InputChannel)
		if !ok || channel.ChannelID != PrintShopChannelID || req.Offset != 0 || req.Limit != 100 {
			f.t.Fatal(req)
		}
		ids := []int64{InterviewTopicAccountID, PrintShopArinaID}
		if f.bad == "arina" {
			ids[1] = 999
		}
		participants := []tg.ChannelParticipantClass{&tg.ChannelParticipantCreator{UserID: ids[0], AdminRights: tg.ChatAdminRights{}}, &tg.ChannelParticipant{UserID: ids[1]}}
		if f.bad == "audience-change" && f.participantsCalls > 1 {
			participants = append(participants, &tg.ChannelParticipant{UserID: 777})
		}
		count := len(participants)
		if f.bad == "incomplete" {
			count++
		}
		response = &tg.ChannelsChannelParticipants{Count: count, Participants: participants}
	case *tg.MessagesSendMessageRequest:
		f.messageEntities = req.Entities
		f.sends++
		peer, ok := req.Peer.(*tg.InputPeerChannel)
		if !ok || peer.ChannelID != PrintShopChannelID || req.ReplyTo != nil || req.ReplyMarkup != nil || req.ScheduleDate != 0 || req.SendAs != nil || !req.NoWebpage || req.RandomID != printShopRandomID("notice-v1") {
			f.t.Fatal("send escaped scope", req)
		}
		return errors.New("FLOOD_WAIT_1") // A mutation must not be automatically retried.
	case *tg.MessagesSendMediaRequest:
		f.sends++
		peer, ok := req.Peer.(*tg.InputPeerChannel)
		media, mediaOK := req.Media.(*tg.InputMediaUploadedDocument)
		if f.video {
			if !ok || peer.ChannelID != PrintShopChannelID || req.ReplyTo != nil || req.ReplyMarkup != nil || req.ScheduleDate != 0 || req.SendAs != nil || req.RandomID != printShopRandomID("video-v1") || !mediaOK || media.MimeType != "video/mp4" || len(media.Attributes) != 2 {
				f.t.Fatal("video send escaped scope", req)
			}
			attr, valid := media.Attributes[1].(*tg.DocumentAttributeVideo)
			if !valid || attr.W != 64 || attr.H != 64 || attr.Duration != 1 || !attr.SupportsStreaming || attr.Nosound || media.NosoundVideo {
				f.t.Fatal("video attributes differ", req)
			}
			return errors.New("FLOOD_WAIT_1")
		}
		if !ok || peer.ChannelID != PrintShopChannelID || req.ReplyTo != nil || req.ReplyMarkup != nil || req.ScheduleDate != 0 || req.SendAs != nil || req.RandomID != printShopRandomID("sheet-v1") || !mediaOK || media.MimeType != "application/pdf" || len(media.Attributes) != 1 {
			f.t.Fatal("media send escaped scope", req)
		}
		filename, ok := media.Attributes[0].(*tg.DocumentAttributeFilename)
		if !ok || filename.FileName != "лист.pdf" || req.Message != "📄 Лист" || len(req.Entities) != 1 {
			f.t.Fatal("media changed", req)
		}
		return errors.New("FLOOD_WAIT_1")
	case *tg.UploadSaveFilePartRequest:
		f.uploads++
		if !f.allowUpload {
			return errors.New("unexpected upload")
		}
		f.uploaded = append(f.uploaded, req.Bytes...)
		if f.afterUpload != nil {
			f.afterUpload()
		}
		response = &tg.BoolTrue{}
	case *tg.UploadSaveBigFilePartRequest:
		f.t.Fatal("unexpected big upload")
	default:
		f.t.Fatalf("unexpected RPC %T", input)
	}
	var b bin.Buffer
	if err := response.Encode(&b); err != nil {
		return err
	}
	return output.Decode(&b)
}
func TestPrintShopPublishWirePreflightAndOneShot(t *testing.T) {
	for _, bad := range []string{"none", "title", "arina", "incomplete", "hash", "audience-change"} {
		t.Run(bad, func(t *testing.T) {
			m, _ := printShopFixture(t)
			p := m.Posts[0]
			w := &printShopWire{t: t, bad: bad}
			target := resolvedTarget{Chat: harvest.Chat{ID: PrintShopChannelID, Type: "supergroup", Title: PrintShopTitle}, InputPeer: &tg.InputPeerChannel{ChannelID: PrintShopChannelID, AccessHash: 123}}
			s := &printShopSession{Session: &Session{raw: tg.NewClient(w), dialogCache: map[string]resolvedTarget{"supergroup:4415343946": target}}}
			if bad == "hash" {
				p = m.Posts[1]
				p.File.SHA256 = strings.Repeat("0", 64)
			}
			_, err := s.printShopSend(context.Background(), p, printShopRandomID(p.Key))
			expected := 0
			if bad == "none" {
				expected = 1
			}
			if err == nil || w.sends != expected || w.uploads != 0 {
				t.Fatal(err, w.sends, w.uploads)
			}
		})
	}
}

func TestPrintShopPublishWirePDFBytesAndRecheck(t *testing.T) {
	for _, change := range []string{"none", "local", "audience"} {
		t.Run(change, func(t *testing.T) {
			m, _ := printShopFixture(t)
			p := m.Posts[1]
			original, err := os.ReadFile(p.File.Path)
			if err != nil {
				t.Fatal(err)
			}
			w := &printShopWire{t: t, allowUpload: true}
			if change == "local" {
				w.afterUpload = func() {
					if err := os.WriteFile(p.File.Path, []byte("changed"), 0600); err != nil {
						t.Error(err)
					}
				}
			}
			if change == "audience" {
				w.afterUpload = func() { w.bad = "audience-change" }
			}
			target := resolvedTarget{Chat: harvest.Chat{ID: PrintShopChannelID, Type: "supergroup", Title: PrintShopTitle}, InputPeer: &tg.InputPeerChannel{ChannelID: PrintShopChannelID, AccessHash: 123}}
			s := &printShopSession{Session: &Session{raw: tg.NewClient(w), dialogCache: map[string]resolvedTarget{"supergroup:4415343946": target}}}
			_, err = s.printShopSend(context.Background(), p, printShopRandomID(p.Key))
			expected := 0
			if change == "none" {
				expected = 1
			}
			if err == nil || w.sends != expected || w.uploads != 1 || !reflect.DeepEqual(w.uploaded, original) {
				t.Fatal(err, w.sends, w.uploads)
			}
		})
	}
}

func TestPrintShopPublishServerPDFBytesAndCleanup(t *testing.T) {
	for _, bad := range []string{"valid", "same-size-wrong-bytes", "too-long", "short", "download-error", "cancel"} {
		t.Run(bad, func(t *testing.T) {
			tempRoot := t.TempDir()
			t.Setenv("TMPDIR", tempRoot)
			m, f := printShopFixture(t)
			p := m.Posts[1]
			id, err := f.printShopSend(context.Background(), p, 1)
			if err != nil {
				t.Fatal(err)
			}
			msg := f.messages[id]
			document := msg.Media.(*tg.MessageMediaDocument).Document.(*tg.Document)
			original := append([]byte(nil), f.serverBytes[document.ID]...)
			ctx := context.Background()
			switch bad {
			case "same-size-wrong-bytes":
				f.serverBytes[document.ID] = append([]byte("%PDF-"), []byte(strings.Repeat("x", len(original)-5))...)
			case "too-long":
				f.serverBytes[document.ID] = append(original, 'x')
			case "short":
				f.serverBytes[document.ID] = original[:len(original)-1]
			case "download-error":
				f.downloadErr = errors.New("download unavailable")
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			err = verifyPrintShopReadback(ctx, f, msg, p)
			if (err == nil) != (bad == "valid") || f.downloadCalls != 1 || f.downloadedDocuments[0] != document {
				t.Fatal(bad, err, f.downloadCalls)
			}
			entries, readErr := os.ReadDir(tempRoot)
			if readErr != nil || len(entries) != 0 {
				t.Fatal("private readback artifacts leaked", entries, readErr)
			}
		})
	}
}
func TestPrintShopPublishReceiptRechecksPDFWithoutResend(t *testing.T) {
	for _, bad := range []string{"same-size-wrong-bytes", "download-error"} {
		t.Run(bad, func(t *testing.T) {
			m, f := printShopFixture(t)
			m.Posts = m.Posts[1:]
			j := topicPublishJournal{}
			r, err := publishPrintShop(context.Background(), f, m, j, func() error { return nil }, true)
			if err != nil || !r.Complete || f.sends != 1 || f.downloadCalls != 1 {
				t.Fatal(r, err, f.sends, f.downloadCalls)
			}
			if bad == "same-size-wrong-bytes" {
				id := int64(r.Rows[0].ID)
				f.serverBytes[id] = []byte(strings.Repeat("x", len(f.serverBytes[id])))
			} else {
				f.downloadErr = errors.New("download unavailable")
			}
			for i := 0; i < 2; i++ {
				r, err = publishPrintShop(context.Background(), f, m, j, func() error { return nil }, true)
				if err == nil || r.Complete || f.sends != 1 || f.downloadCalls != i+2 {
					t.Fatal("receipt bytes not reverified or resent", r, err, f.sends, f.downloadCalls)
				}
			}
		})
	}
}
func TestPrintShopPublishFailedDownloadRemainsPending(t *testing.T) {
	m, f := printShopFixture(t)
	m.Posts = m.Posts[1:]
	j := topicPublishJournal{}
	f.downloadErr = errors.New("download unavailable")
	r, err := publishPrintShop(context.Background(), f, m, j, func() error { return nil }, true)
	if err == nil || r.Complete || !r.Rows[0].OutcomeUnknown || !j[m.Posts[0].Key].Pending || f.sends != 1 {
		t.Fatal(r, err, f.sends, j)
	}
	_, err = publishPrintShop(context.Background(), f, m, j, func() error { return nil }, true)
	if err == nil || f.sends != 1 {
		t.Fatal("download failure resent", err, f.sends)
	}
	f.downloadErr = nil
	r, err = publishPrintShop(context.Background(), f, m, j, func() error { return nil }, true)
	if err != nil || !r.Complete || f.sends != 1 || j[m.Posts[0].Key].Pending || r.Rows[0].Status != "already_applied" {
		t.Fatal(r, err, f.sends, j)
	}
}
func TestPrintShopPublishDownloadWriterHardBound(t *testing.T) {
	var out strings.Builder
	w := &boundedPrintShopWriter{writer: &out, remaining: 3}
	if n, err := w.Write([]byte("ab")); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if n, err := w.Write([]byte("cd")); err == nil || n != 0 || out.String() != "ab" {
		t.Fatal("overrun accepted", n, err, out.String())
	}
}

// A known pending receipt must be read back and hashed, never sent again.
func TestPrintShopNormalizedFilenameReceiptAndNewSend(t *testing.T) {
	for _, known := range []bool{true, false} {
		t.Run(map[bool]string{true: "pending-169", false: "new"}[known], func(t *testing.T) {
			m, f := printShopFixture(t)
			m.Posts = m.Posts[1:]
			p := &m.Posts[0]
			name := "ЕГЭ профиль №8 – исправленный образец.pdf"
			path := filepath.Join(filepath.Dir(p.File.Path), name)
			if err := os.Rename(p.File.Path, path); err != nil {
				t.Fatal(err)
			}
			p.File.Path, p.File.Filename = path, name
			f.normalizeFilename = true
			j := topicPublishJournal{}
			if known {
				id, err := f.printShopSend(context.Background(), *p, 1)
				if err != nil {
					t.Fatal(err)
				}
				f.messages[169] = f.messages[id]
				f.messages[169].ID = 169
				j[p.Key] = topicPublishReceipt{Hash: publishPostHash(p.publicationPost()), ID: 169, Pending: true}
				f.sends = 0
			}
			if !known {
				preview, err := publishPrintShop(context.Background(), f, m, j, func() error { return nil }, false)
				if err != nil || preview.Complete || preview.Rows[0].ServerVerified || f.downloadCalls != 0 {
					t.Fatal(preview, err)
				}
			}
			r, err := publishPrintShop(context.Background(), f, m, j, func() error { return nil }, true)
			wantSends := 1
			status := "sent_verified"
			if known {
				wantSends = 0
				status = "already_applied"
			}
			if err != nil || !r.Complete || f.sends != wantSends || f.downloadCalls != 1 || j[p.Key].Pending || r.Rows[0].Status != status || !r.Rows[0].FilenameNormalized || r.Rows[0].ServerFilename != strings.ReplaceAll(name, " ", "_") {
				t.Fatal(r, err, f.sends, j)
			}
		})
	}
}
func TestPrintShopNormalizedFilenameRejectsMismatch(t *testing.T) {
	for _, bad := range []string{"name", "partial", "mime", "size", "bytes", "id"} {
		t.Run(bad, func(t *testing.T) {
			m, f := printShopFixture(t)
			m.Posts = m.Posts[1:]
			p := &m.Posts[0]
			name := "два слова здесь.pdf"
			path := filepath.Join(filepath.Dir(p.File.Path), name)
			if err := os.Rename(p.File.Path, path); err != nil {
				t.Fatal(err)
			}
			p.File.Path, p.File.Filename = path, name
			f.normalizeFilename = true
			id, err := f.printShopSend(context.Background(), *p, 1)
			if err != nil {
				t.Fatal(err)
			}
			f.sends = 0
			msg := f.messages[id]
			doc := msg.Media.(*tg.MessageMediaDocument).Document.(*tg.Document)
			switch bad {
			case "name":
				doc.Attributes[0] = &tg.DocumentAttributeFilename{FileName: "чужой.pdf"}
			case "partial":
				doc.Attributes[0] = &tg.DocumentAttributeFilename{FileName: "два_слова здесь.pdf"}
			case "mime":
				doc.MimeType = "text/plain"
			case "size":
				doc.Size++
			case "bytes":
				f.serverBytes[doc.ID] = []byte(strings.Repeat("x", int(doc.Size)))
			case "id":
				msg.ID++
			}
			j := topicPublishJournal{p.Key: {Hash: publishPostHash(p.publicationPost()), ID: id, Pending: true}}
			r, err := publishPrintShop(context.Background(), f, m, j, func() error { return nil }, true)
			if err == nil || r.Complete || f.sends != 0 || !j[p.Key].Pending {
				t.Fatal(r, err, f.sends, j)
			}
		})
	}
}

func TestPrintShopPublishLiteralURLAndPendingReadback(t *testing.T) {
	m, f := printShopFixture(t)
	m.Posts = m.Posts[:1]
	p := &m.Posts[0]
	p.Text = "📄 Привет\nhttps://t.me/ArinaMathMaterialsBot"
	want := []InterviewTopicEntity{{Type: "url", Offset: 10, Length: 34}}
	j := topicPublishJournal{}
	ctx := context.Background()
	preview, err := publishPrintShop(ctx, f, m, j, func() error { t.Fatal("preview saved"); return nil }, false)
	if err != nil || !reflect.DeepEqual(preview.Rows[0].EffectiveEntities, want) || f.sends != 0 || len(p.Entities) != 0 {
		t.Fatal(preview, err)
	}
	result, err := publishPrintShop(ctx, f, m, j, func() error { return nil }, true)
	if err != nil || !result.Complete || f.sends != 1 || j[p.Key].Hash != publishPostHash(p.publicationPost()) {
		t.Fatal(result, err, j)
	}
	receipt := j[p.Key]
	receipt.Pending = true
	j[p.Key] = receipt
	result, err = publishPrintShop(ctx, f, m, j, func() error { return nil }, true)
	if err != nil || !result.Complete || result.Rows[0].Status != "already_applied" || f.sends != 1 || j[p.Key].Pending {
		t.Fatal(result, err, j)
	}
	for _, mutate := range []struct {
		name   string
		change func(*tg.Message)
	}{
		{"extra", func(m *tg.Message) { m.Entities = append(m.Entities, &tg.MessageEntityBold{Offset: 0, Length: 2}) }},
		{"wrong-offset", func(m *tg.Message) { m.Entities = []tg.MessageEntityClass{&tg.MessageEntityURL{Offset: 9, Length: 34}} }},
		{"surrogate", func(m *tg.Message) { m.Entities = []tg.MessageEntityClass{&tg.MessageEntityURL{Offset: 1, Length: 34}} }},
		{"wrong-length", func(m *tg.Message) {
			m.Entities = []tg.MessageEntityClass{&tg.MessageEntityURL{Offset: 10, Length: 33}}
		}},
		{"text-url", func(m *tg.Message) {
			m.Entities = []tg.MessageEntityClass{&tg.MessageEntityTextURL{Offset: 10, Length: 34, URL: "https://evil.example"}}
		}},
		{"changed-text", func(m *tg.Message) { m.Message += "!" }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			original := *f.messages[receipt.ID]
			changed := original
			mutate.change(&changed)
			f.messages[receipt.ID] = &changed
			r := receipt
			r.Pending = true
			j[p.Key] = r
			_, err := publishPrintShop(ctx, f, m, j, func() error { t.Fatal("failed verification saved"); return nil }, true)
			if err == nil || f.sends != 1 || !j[p.Key].Pending {
				t.Fatal(err, f.sends, j)
			}
			f.messages[receipt.ID] = &original
		})
	}
}

func TestPrintShopPublishExplicitEntitiesUnchanged(t *testing.T) {
	p := PrintShopPost{Text: "📄 https://example.com", Entities: []InterviewTopicEntity{{Type: "bold", Offset: 0, Length: 2}}}
	if !reflect.DeepEqual(p.effectiveEntities(), p.Entities) {
		t.Fatal(p.effectiveEntities())
	}
	p.Entities = nil
	p.File = &PrintShopPDF{}
	if len(p.effectiveEntities()) != 0 {
		t.Fatal("PDF caption must not infer entities")
	}
}

func TestPrintShopPublishWireLiteralURL(t *testing.T) {
	m, _ := printShopFixture(t)
	p := m.Posts[0]
	p.Text = "📄 Привет\nhttps://t.me/ArinaMathMaterialsBot"
	w := &printShopWire{t: t}
	target := resolvedTarget{Chat: harvest.Chat{ID: PrintShopChannelID, Type: "supergroup", Title: PrintShopTitle}, InputPeer: &tg.InputPeerChannel{ChannelID: PrintShopChannelID, AccessHash: 123}}
	s := &printShopSession{Session: &Session{raw: tg.NewClient(w), dialogCache: map[string]resolvedTarget{"supergroup:4415343946": target}}}
	_, _ = s.printShopSend(context.Background(), p, printShopRandomID(p.Key))
	want := []tg.MessageEntityClass{&tg.MessageEntityURL{Offset: 10, Length: 34}}
	if w.sends != 1 || !sameTopicEntities(w.messageEntities, want) {
		t.Fatal(w.sends, w.messageEntities)
	}
}

// A copied durable pending receipt must recover ID 319 before sending later posts.
func TestPrintShopPublishPending319FilenameAndContinuation(t *testing.T) {
	for _, tc := range []struct {
		name, server string
		normalized   bool
	}{
		{"exact", "Телефон – без переносов и варианты А-Б.pdf", false},
		{"spaces", "Телефон_–_без_переносов_и_варианты_А-Б.pdf", true},
		{"spaces-hyphen", "Телефон_–_без_переносов_и_варианты_А_Б.pdf", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, f := printShopFixture(t)
			p := m.Posts[1]
			path := filepath.Join(filepath.Dir(p.File.Path), "Телефон – без переносов и варианты А-Б.pdf")
			if err := os.Rename(p.File.Path, path); err != nil {
				t.Fatal(err)
			}
			p.File.Path, p.File.Filename = path, filepath.Base(path)
			m.Posts = []PrintShopPost{p, {Key: "next-1", Text: "один"}, {Key: "next-2", Text: "два"}, {Key: "next-3", Text: "три"}}
			id, err := f.printShopSend(context.Background(), p, 1)
			if err != nil {
				t.Fatal(err)
			}
			msg := f.messages[id]
			msg.ID = 319
			msg.Media.(*tg.MessageMediaDocument).Document.(*tg.Document).Attributes[0] = &tg.DocumentAttributeFilename{FileName: tc.server}
			f.messages[319] = msg
			f.sends, f.sentKeys = 0, nil
			hash := publishPostHash(p.publicationPost())
			original := topicPublishJournal{p.Key: {Hash: hash, ID: 319, Pending: true}}
			receiptPath := filepath.Join(t.TempDir(), "copy.receipts.json")
			if err := savePublishJournal(receiptPath, original); err != nil {
				t.Fatal(err)
			}
			journal, err := loadPublicationJournal(receiptPath, 0)
			if err != nil {
				t.Fatal(err)
			}
			r, err := publishPrintShop(context.Background(), f, m, journal, func() error { return savePublishJournal(receiptPath, journal) }, true)
			if err != nil || !r.Complete || r.Rows[0].Status != "already_applied" || r.Rows[0].ID != 319 || r.Rows[0].ServerFilename != tc.server || r.Rows[0].FilenameNormalized != tc.normalized || journal[p.Key].Hash != hash || journal[p.Key].Pending || f.downloadCalls != 1 || !reflect.DeepEqual(f.sentKeys, []string{"next-1", "next-2", "next-3"}) {
				t.Fatal(r, err, journal, f.sentKeys)
			}
		})
	}
}

func TestPrintShopNativeVideoReadbackAndRepeat(t *testing.T) {
	v := localReplacementVideo(t)
	m, f := printShopFixture(t)
	m.Posts = []PrintShopPost{{Key: "video-v1", Text: "Видео", File: &PrintShopPDF{Path: v.Path, Filename: v.Filename, MIME: v.MIME, SHA256: v.SHA256, Size: v.Size, Duration: v.Duration, Width: v.Width, Height: v.Height, HasAudio: v.HasAudio}}}
	ctx := context.Background()
	j := topicPublishJournal{}
	save := func() error { return nil }
	preview, err := publishPrintShop(ctx, f, m, j, save, false)
	if err != nil || !preview.Ready || preview.Complete || f.sends != 0 {
		t.Fatal(preview, err)
	}
	result, err := publishPrintShop(ctx, f, m, j, save, true)
	if err != nil || !result.Complete || f.sends != 1 || !result.Rows[0].ServerVerified {
		t.Fatal(result, err)
	}
	result, err = publishPrintShop(ctx, f, m, j, save, true)
	if err != nil || !result.Complete || f.sends != 1 {
		t.Fatal(result, err)
	}
	bytes := f.serverBytes[1]
	bytes[len(bytes)-1] ^= 1
	if _, err = publishPrintShop(ctx, f, m, j, save, true); err == nil || f.sends != 1 {
		t.Fatal("changed server bytes accepted", err)
	}
	bytes[len(bytes)-1] ^= 1
	doc := f.messages[1].Media.(*tg.MessageMediaDocument).Document.(*tg.Document)
	doc.Attributes[1].(*tg.DocumentAttributeVideo).W++
	if _, err = publishPrintShop(ctx, f, m, j, save, true); err == nil || f.sends != 1 {
		t.Fatal("changed video attributes accepted", err)
	}
}

func TestPrintShopVideoRejectsBadInputs(t *testing.T) {
	v := localReplacementVideo(t)
	m, f := printShopFixture(t)
	m.Posts = []PrintShopPost{{Key: "video-v1", Text: "Видео", File: &PrintShopPDF{Path: v.Path, Filename: v.Filename, MIME: v.MIME, SHA256: v.SHA256, Size: v.Size, Duration: v.Duration, Width: v.Width, Height: v.Height, HasAudio: v.HasAudio}}}
	for _, mutate := range []func(*PrintShopPDF){func(f *PrintShopPDF) { f.MIME = "application/pdf" }, func(f *PrintShopPDF) { f.Width = 0 }, func(f *PrintShopPDF) { f.Duration = 2 }, func(f *PrintShopPDF) { f.SHA256 = strings.Repeat("0", 64) }} {
		original := *m.Posts[0].File
		mutate(m.Posts[0].File)
		if _, err := publishPrintShop(context.Background(), f, m, topicPublishJournal{}, func() error { return nil }, true); err == nil || f.sends != 0 {
			t.Fatal("invalid input accepted", err)
		}
		*m.Posts[0].File = original
	}
}

func TestPrintShopVideoWireUpload(t *testing.T) {
	v := localReplacementVideo(t)
	p := PrintShopPost{Key: "video-v1", Text: "Видео", File: &PrintShopPDF{Path: v.Path, Filename: v.Filename, MIME: v.MIME, SHA256: v.SHA256, Size: v.Size, Duration: v.Duration, Width: v.Width, Height: v.Height, HasAudio: v.HasAudio}}
	w := &printShopWire{t: t, allowUpload: true, video: true}
	target := resolvedTarget{Chat: harvest.Chat{ID: PrintShopChannelID, Type: "supergroup", Title: PrintShopTitle}, InputPeer: &tg.InputPeerChannel{ChannelID: PrintShopChannelID, AccessHash: 123}}
	session := &printShopSession{Session: &Session{raw: tg.NewClient(w), dialogCache: map[string]resolvedTarget{"supergroup:4415343946": target}}}
	_, err := session.printShopSend(context.Background(), p, printShopRandomID(p.Key))
	original, readErr := os.ReadFile(v.Path)
	if err == nil || readErr != nil || w.sends != 1 || w.uploads != 1 || !reflect.DeepEqual(w.uploaded, original) {
		t.Fatal(err, readErr, w.sends, w.uploads)
	}
}
