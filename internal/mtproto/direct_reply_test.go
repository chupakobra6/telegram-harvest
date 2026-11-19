package mtproto

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

type directReplyWire struct {
	t          *testing.T
	manifest   DirectReplyManifest
	incoming   tg.Message
	outgoing   *tg.Message
	recipient  *tg.User
	failSend   bool
	badReply   bool
	mapped     bool
	sends      int
	fileData   []byte
	fileID     int64
	uploads    int
	downloads  int
	onUpload   func() error
	alterDoc   func(*tg.Message, *tg.Document)
	serverData []byte
	fullRich   *tg.RichMessage
	richReads  int
}

func (w *directReplyWire) Invoke(_ context.Context, input bin.Encoder, output bin.Decoder) error {
	var response bin.Encoder
	switch req := input.(type) {
	case *tg.UsersGetFullUserRequest:
		response = &tg.UsersUserFull{
			FullUser: tg.UserFull{ID: InterviewTopicAccountID},
			Users:    []tg.UserClass{&tg.User{ID: InterviewTopicAccountID, Username: SavedMessagesMainUsername}},
		}
	case *tg.ContactsResolveUsernameRequest:
		if req.Username != w.manifest.Username {
			w.t.Fatal("wrong lookup", req.Username)
		}
		response = &tg.ContactsResolvedPeer{
			Peer:  &tg.PeerUser{UserID: w.recipient.ID},
			Users: []tg.UserClass{w.recipient},
		}
	case *tg.MessagesGetMessagesRequest:
		id := req.ID[0].(*tg.InputMessageID).ID
		msg := &w.incoming
		if id != w.incoming.ID {
			msg = w.outgoing
		}
		if msg == nil || msg.ID != id {
			w.t.Fatal("unexpected lookup", id)
		}
		response = &tg.MessagesMessages{
			Messages: []tg.MessageClass{msg},
			Users:    []tg.UserClass{w.recipient},
		}
	case *tg.MessagesGetRichMessageRequest:
		peer, ok := req.Peer.(*tg.InputPeerUser)
		if w.fullRich == nil || !ok || peer.UserID != w.manifest.PeerID || req.ID != w.manifest.ReplyTo {
			w.t.Fatal("unexpected full rich message lookup")
		}
		w.richReads++
		full := w.incoming
		full.SetRichMessage(*w.fullRich)
		response = &tg.MessagesMessages{Messages: []tg.MessageClass{&full}, Users: []tg.UserClass{w.recipient}}
	case *tg.MessagesSendMessageRequest:
		w.sends++
		user, userOK := req.Peer.(*tg.InputPeerUser)
		reply, replyOK := req.ReplyTo.(*tg.InputReplyToMessage)
		if w.manifest.File != nil || !userOK || user.UserID != w.manifest.PeerID || !replyOK || reply.ReplyToMsgID != w.manifest.ReplyTo ||
			req.Message != w.manifest.Text || req.RandomID == 0 || !req.NoWebpage ||
			req.SendAs != nil || req.AllowPaidStars != 0 || req.ScheduleDate != 0 {
			w.t.Fatalf("unexpected send request: %+v", req)
		}
		if w.failSend {
			return errors.New("lost send response")
		}
		replyID := reply.ReplyToMsgID
		if w.badReply {
			replyID++
		}
		w.outgoing = &tg.Message{
			ID: 52, Out: true, PeerID: &tg.PeerUser{UserID: user.UserID}, Message: req.Message,
			ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: replyID},
			Media:   &tg.MessageMediaEmpty{},
		}
		response = &tg.UpdateShortSentMessage{ID: w.outgoing.ID, Out: true}
		if w.mapped {
			response = &tg.Updates{Updates: []tg.UpdateClass{
				&tg.UpdateMessageID{RandomID: req.RandomID + 1, ID: 59},
				&tg.UpdateMessageID{RandomID: req.RandomID, ID: w.outgoing.ID},
			}}
		}
	case *tg.UploadSaveFilePartRequest:
		w.uploads++
		if req.FilePart != 0 || !bytes.Equal(req.Bytes, w.fileData) {
			w.t.Fatalf("uploaded bytes differ from the reviewed document")
		}
		w.fileID = req.FileID
		if w.onUpload != nil {
			if err := w.onUpload(); err != nil {
				return err
			}
		}
		response = &tg.BoolTrue{}
	case *tg.MessagesSendMediaRequest:
		w.sends++
		user, userOK := req.Peer.(*tg.InputPeerUser)
		reply, replyOK := req.ReplyTo.(*tg.InputReplyToMessage)
		media, mediaOK := req.Media.(*tg.InputMediaUploadedDocument)
		file := w.manifest.File
		if file == nil || !userOK || user.UserID != w.manifest.PeerID || !replyOK || reply.ReplyToMsgID != w.manifest.ReplyTo ||
			req.Message != w.manifest.Text || req.RandomID != directReplyRandomID("42/51") ||
			req.SendAs != nil || req.AllowPaidStars != 0 || req.ScheduleDate != 0 ||
			!mediaOK || !media.ForceFile || media.MimeType != file.MIME || len(media.Attributes) != 1 {
			w.t.Fatalf("unexpected document send request: %+v", req)
		}
		name, nameOK := media.Attributes[0].(*tg.DocumentAttributeFilename)
		uploaded, uploadOK := media.File.(*tg.InputFile)
		if !nameOK || name.FileName != file.Filename || !uploadOK || uploaded.Name != file.Filename || uploaded.ID != w.fileID {
			w.t.Fatalf("unexpected attachment: %+v", media)
		}
		if w.failSend {
			return errors.New("lost media send response")
		}
		document := &tg.Document{ID: 111, MimeType: file.MIME, Size: file.Size, Attributes: media.Attributes}
		attachment := &tg.MessageMediaDocument{}
		attachment.SetDocument(document)
		w.outgoing = &tg.Message{
			ID: 52, Out: true, PeerID: &tg.PeerUser{UserID: user.UserID}, Message: req.Message,
			ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: reply.ReplyToMsgID}, Media: attachment,
		}
		if w.alterDoc != nil {
			w.alterDoc(w.outgoing, document)
		}
		response = &tg.Updates{Updates: []tg.UpdateClass{
			&tg.UpdateMessageID{RandomID: req.RandomID, ID: w.outgoing.ID},
		}}
	case *tg.UploadGetFileRequest:
		w.downloads++
		location, ok := req.Location.(*tg.InputDocumentFileLocation)
		if !ok || location.ID != 111 || req.Offset != 0 {
			return fmt.Errorf("unexpected file readback location")
		}
		response = &tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: w.serverData}
	default:
		w.t.Fatalf("unexpected RPC %T", input)
	}
	var buf bin.Buffer
	if err := response.Encode(&buf); err != nil {
		return err
	}
	return output.Decode(&buf)
}

func directReplyFixture(t *testing.T) (*Session, *directReplyWire, string) {
	t.Helper()
	w := &directReplyWire{
		t:        t,
		manifest: DirectReplyManifest{PeerID: 42, Username: "example_user", ReplyTo: 51, Text: "Ответ\n\nСтрока"},
		incoming: tg.Message{ID: 51, PeerID: &tg.PeerUser{UserID: 42}, Message: "Вопрос"},
		recipient: &tg.User{
			ID: 42, AccessHash: 123, Username: "example_user", FirstName: "Example",
		},
	}
	w.manifest.ReplyToTextSHA256 = topicTextHash(w.incoming.Message)
	return &Session{raw: tg.NewClient(w), dialogCache: make(map[string]resolvedTarget)}, w, filepath.Join(t.TempDir(), "receipts.json")
}

func TestDirectReplyPreviewsSendsAndReopensReceipt(t *testing.T) {
	s, w, journal := directReplyFixture(t)
	w.mapped = true
	ctx := context.Background()
	r, err := s.SendDirectReply(ctx, w.manifest, journal, false)
	if err != nil || r.Status != "ready" || r.IncomingText != w.incoming.Message || w.sends != 0 {
		t.Fatalf("unexpected preview: %+v, %v", r, err)
	}
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Fatalf("preview changed journal: %v", err)
	}
	r, err = s.SendDirectReply(ctx, w.manifest, journal, true)
	if err != nil || r.Status != "sent" || !r.ServerVerified || r.MessageID != 52 || w.sends != 1 {
		t.Fatalf("unexpected send: %+v, %v", r, err)
	}
	legacyJSON := fmt.Sprintf(`{"peer_id":42,"username":"example_user","reply_to":51,"reply_to_text_sha256":"%s","text":"Ответ\n\nСтрока"}`, w.manifest.ReplyToTextSHA256)
	receipts, err := loadPublicationJournal(journal, 0)
	if err != nil || receipts["42/51"].Hash != topicTextHash(legacyJSON) {
		t.Fatalf("text receipt changed from legacy format: %v", err)
	}
	r, err = s.SendDirectReply(ctx, w.manifest, journal, true)
	if err != nil || r.Status != "already_sent" || !r.ServerVerified || w.sends != 1 {
		t.Fatalf("unexpected repeat: %+v, %v", r, err)
	}
}

func TestDirectReplyRefusesOtherRecipientsAndEditedParents(t *testing.T) {
	for _, change := range []struct {
		name  string
		apply func(*directReplyWire)
	}{
		{"reassigned username", func(w *directReplyWire) { w.recipient.ID++ }},
		{"bot", func(w *directReplyWire) { w.recipient.Bot = true }},
		{"other dialog", func(w *directReplyWire) { w.incoming.PeerID = &tg.PeerUser{UserID: 90} }},
		{"outgoing parent", func(w *directReplyWire) { w.incoming.Out = true }},
		{"edited text", func(w *directReplyWire) { w.incoming.Message += "!" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			s, w, journal := directReplyFixture(t)
			change.apply(w)
			if r, err := s.SendDirectReply(context.Background(), w.manifest, journal, true); err == nil || r.ServerVerified || w.sends != 0 {
				t.Fatalf("unsafe send: %+v, %v, sends=%d", r, err, w.sends)
			}
		})
	}
}

func TestDirectReplyUnknownOutcomeNeverSendsAgain(t *testing.T) {
	for _, failure := range []string{"send", "readback"} {
		t.Run(failure, func(t *testing.T) {
			s, w, journal := directReplyFixture(t)
			w.failSend, w.badReply = failure == "send", failure == "readback"
			ctx := context.Background()
			if r, err := s.SendDirectReply(ctx, w.manifest, journal, true); err == nil || r.Status != "outcome_unknown" || r.ServerVerified || w.sends != 1 {
				t.Fatalf("unexpected outcome: %+v, %v, sends=%d", r, err, w.sends)
			}
			for _, text := range []string{w.manifest.Text, "Edited after send"} {
				m := w.manifest
				m.Text = text
				if r, err := s.SendDirectReply(ctx, m, journal, true); err == nil || r.ServerVerified || w.sends != 1 {
					t.Fatalf("unsafe repeat: %+v, %v, sends=%d", r, err, w.sends)
				}
			}
		})
	}
}

type directReplyArchiveEntry struct{ name, text string }

func directReplyArchiveFixture(t *testing.T, caption, name, mime string, entries []directReplyArchiveEntry) (*Session, *directReplyWire, string) {
	t.Helper()
	s, w, journal := directReplyFixture(t)
	var data bytes.Buffer
	archive := zip.NewWriter(&data)
	for _, entry := range entries {
		part, err := archive.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(entry.text)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	w.fileData, w.serverData = data.Bytes(), data.Bytes()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, w.fileData, 0o600); err != nil {
		t.Fatal(err)
	}
	w.manifest.Text = caption
	w.manifest.File = &DirectReplyFile{Path: path, Filename: name, Size: int64(len(w.fileData)), SHA256: topicTextHash(string(w.fileData)), MIME: mime}
	transport := telegram.MiddlewareFunc(func(tg.Invoker) telegram.InvokeFunc { return w.Invoke })
	s.client = telegram.NewClient(1, "fixture", telegram.Options{Middlewares: []telegram.Middleware{transport}})
	return s, w, journal
}

func directReplyXLSXFixture(t *testing.T, caption, contentTypeEntry string) (*Session, *directReplyWire, string) {
	t.Helper()
	return directReplyArchiveFixture(t, caption, "Таблица_2026.xlsx", xlsxMIME, []directReplyArchiveEntry{
		{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` + contentTypeEntry + `</Types>`},
		{"xl/workbook.xml", `<workbook/>`},
	})
}

func directReplyZIPFixture(t *testing.T, caption string) (*Session, *directReplyWire, string) {
	t.Helper()
	return directReplyArchiveFixture(t, caption, "Комплект.zip", zipMIME, []directReplyArchiveEntry{
		{"source/README.md", "Test package"},
	})
}

const xlsxFixtureOverride = `<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>`

func TestDirectReplyDocumentPreviewsSendsAndRechecksBytes(t *testing.T) {
	for _, tc := range []struct {
		name, caption, contentTypeEntry string
		zip                             bool
	}{
		{"default_no_caption", "", `<Default Extension="xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>`, false},
		{"override_with_caption", "Готовая таблица\nСпасибо!", xlsxFixtureOverride, false},
		{"zip_with_caption", "Готовый комплект", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s *Session
			var w *directReplyWire
			var journal string
			if tc.zip {
				s, w, journal = directReplyZIPFixture(t, tc.caption)
			} else {
				s, w, journal = directReplyXLSXFixture(t, tc.caption, tc.contentTypeEntry)
			}
			r, err := s.SendDirectReply(t.Context(), w.manifest, journal, false)
			if err != nil || r.Status != "ready" || r.File == nil || *r.File != *w.manifest.File || r.Text != tc.caption || w.uploads+w.sends+w.downloads != 0 {
				t.Fatalf("unexpected preview: %+v, %v", r, err)
			}
			if _, err := os.Stat(journal); !os.IsNotExist(err) {
				t.Fatalf("preview changed journal: %v", err)
			}
			r, err = s.SendDirectReply(t.Context(), w.manifest, journal, true)
			if err != nil || r.Status != "sent" || !r.ServerVerified || r.MessageID != 52 || w.uploads != 1 || w.sends != 1 || w.downloads != 1 {
				t.Fatalf("unexpected apply: %+v, %v, uploads=%d sends=%d downloads=%d", r, err, w.uploads, w.sends, w.downloads)
			}
			if err := os.Remove(w.manifest.File.Path); err != nil {
				t.Fatal(err)
			}
			if tc.zip {
				w.serverData = bytes.Clone(w.fileData)
				w.serverData[0]++
				r, err = s.SendDirectReply(t.Context(), w.manifest, journal, true)
				if err == nil || !strings.Contains(err.Error(), "size or SHA-256") || r.Status != "reconcile_required" || w.uploads != 1 || w.sends != 1 {
					t.Fatalf("accepted corrupted server ZIP: %+v, %v", r, err)
				}
				w.serverData = w.fileData
			}
			downloadsBeforeRecheck := w.downloads
			r, err = s.SendDirectReply(t.Context(), w.manifest, journal, true)
			if err != nil || r.Status != "already_sent" || !r.ServerVerified || w.uploads != 1 || w.sends != 1 || w.downloads != downloadsBeforeRecheck+1 {
				t.Fatalf("unexpected recheck: %+v, %v", r, err)
			}
		})
	}
}

func TestDirectReplyXLSXRejectsUnreviewedFilesAndChangedTargets(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(*directReplyWire)
		want    string
		uploads int
	}{
		{"invalid MIME", func(w *directReplyWire) { w.manifest.File.MIME = zipMIME }, "extension and MIME", 0},
		{"relative path", func(w *directReplyWire) { w.manifest.File.Path = w.manifest.File.Filename }, "absolute path", 0},
		{"different filename", func(w *directReplyWire) { w.manifest.File.Filename = "Other.xlsx" }, "exact basename", 0},
		{"caption limit", func(w *directReplyWire) { w.manifest.Text = strings.Repeat("🌟", 513) }, "1024 UTF-16", 0},
		{"file hash", func(w *directReplyWire) { w.manifest.File.SHA256 = strings.Repeat("a", 64) }, "SHA-256 mismatch", 0},
		{"disguised file", func(w *directReplyWire) {
			data := []byte("not an XLSX")
			if err := os.WriteFile(w.manifest.File.Path, data, 0o600); err != nil {
				w.t.Fatal(err)
			}
			w.manifest.File.Size, w.manifest.File.SHA256 = int64(len(data)), topicTextHash(string(data))
		}, "invalid XLSX archive", 0},
		{"parent edited during upload", func(w *directReplyWire) {
			w.onUpload = func() error { w.incoming.Message += "!"; return nil }
		}, "reviewed private reply target", 1},
		{"username reassigned during upload", func(w *directReplyWire) {
			w.onUpload = func() error { w.recipient.ID++; return nil }
		}, "reviewed recipient", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, w, journal := directReplyXLSXFixture(t, "Ответ", xlsxFixtureOverride)
			tc.prepare(w)
			if tc.uploads == 0 {
				r, err := s.SendDirectReply(t.Context(), w.manifest, journal, false)
				if err == nil || !strings.Contains(err.Error(), tc.want) || r.Status == "ready" {
					t.Fatalf("unsafe preview: %+v, %v", r, err)
				}
			}
			r, err := s.SendDirectReply(t.Context(), w.manifest, journal, true)
			if err == nil || !strings.Contains(err.Error(), tc.want) || r.ServerVerified || w.uploads != tc.uploads || w.sends+w.downloads != 0 {
				t.Fatalf("unsafe apply: %+v, %v", r, err)
			}
			if _, err := os.Stat(journal); !os.IsNotExist(err) {
				t.Fatalf("pre-send failure changed journal: %v", err)
			}
		})
	}
}

func TestDirectReplyZIPRejectsMismatchedTypeAndMalformedArchive(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		mutate     func(*testing.T, *DirectReplyFile)
	}{
		{"MIME", "extension and MIME", func(_ *testing.T, file *DirectReplyFile) { file.MIME = "application/x-zip-compressed" }},
		{"extension", "extension and MIME", func(_ *testing.T, file *DirectReplyFile) {
			file.Filename += ".txt"
			file.Path += ".txt"
		}},
		{"bytes", "invalid ZIP archive", func(t *testing.T, file *DirectReplyFile) {
			data := []byte("invalid ZIP, with its own size and SHA-256")
			if err := os.WriteFile(file.Path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			file.Size, file.SHA256 = int64(len(data)), topicTextHash(string(data))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, w, journal := directReplyZIPFixture(t, "")
			tc.mutate(t, w.manifest.File)
			for _, apply := range []bool{false, true} {
				r, err := s.SendDirectReply(t.Context(), w.manifest, journal, apply)
				if err == nil || !strings.Contains(err.Error(), tc.want) || r.Status == "ready" || w.uploads+w.sends+w.downloads != 0 {
					t.Fatalf("unsafe document validation with apply=%v: %+v, %v", apply, r, err)
				}
			}
			if _, err := os.Stat(journal); !os.IsNotExist(err) {
				t.Fatalf("pre-send failure changed journal: %v", err)
			}
		})
	}
}

func TestDirectReplyRichTextWithoutPlainBodySendsZIP(t *testing.T) {
	s, w, journal := directReplyZIPFixture(t, "Комплект")
	w.incoming.Message = ""
	w.incoming.SetRichMessage(tg.RichMessage{Part: true, Blocks: []tg.PageBlockClass{
		&tg.PageBlockParagraph{Text: &tg.TextPlain{Text: "Предпросмотр"}},
	}})
	const reviewed = "Полный запрос без обычного текста"
	w.fullRich = &tg.RichMessage{Blocks: []tg.PageBlockClass{
		&tg.PageBlockParagraph{Text: &tg.TextPlain{Text: reviewed}},
	}}
	w.manifest.ReplyToTextSHA256 = topicTextHash(reviewed)
	r, err := s.SendDirectReply(t.Context(), w.manifest, journal, true)
	if err != nil || r.Status != "sent" || !r.ServerVerified || r.IncomingText != reviewed ||
		w.richReads != 2 || w.uploads != 1 || w.sends != 1 || w.downloads != 1 {
		t.Fatalf("unexpected ZIP reply to full rich input: %+v, %v", r, err)
	}
}

func TestDirectReplyRichTextUsesFullHistoryTextAndRechecksAfterUpload(t *testing.T) {
	s, w, journal := directReplyZIPFixture(t, "Комплект")
	w.incoming.Message = "  Вступление  "
	w.incoming.SetRichMessage(tg.RichMessage{Part: true, Blocks: []tg.PageBlockClass{
		&tg.PageBlockParagraph{Text: &tg.TextPlain{Text: "Предпросмотр"}},
	}})
	w.fullRich = &tg.RichMessage{Blocks: []tg.PageBlockClass{
		&tg.PageBlockParagraph{Text: &tg.TextPlain{Text: "Полный запрос"}},
	}}
	const reviewed = "Вступление\n\nПолный запрос"
	w.manifest.ReplyToTextSHA256 = topicTextHash(reviewed)
	r, err := s.SendDirectReply(t.Context(), w.manifest, journal, false)
	if err != nil || r.Status != "ready" || r.IncomingText != reviewed || w.richReads != 1 || w.uploads+w.sends != 0 {
		t.Fatalf("unexpected rich preview: %+v, %v", r, err)
	}
	w.fullRich.Part = true
	r, err = s.SendDirectReply(t.Context(), w.manifest, journal, true)
	if err == nil || !strings.Contains(err.Error(), "remains partial") || r.Status == "ready" || w.uploads+w.sends != 0 {
		t.Fatalf("accepted incomplete rich content: %+v, %v", r, err)
	}
	w.fullRich.Part = false
	w.onUpload = func() error {
		w.fullRich.Blocks[0] = &tg.PageBlockParagraph{Text: &tg.TextPlain{Text: "Изменённый запрос"}}
		return nil
	}
	r, err = s.SendDirectReply(t.Context(), w.manifest, journal, true)
	if err == nil || !strings.Contains(err.Error(), "SHA-256") || r.ServerVerified || w.richReads != 4 || w.uploads != 1 || w.sends != 0 {
		t.Fatalf("accepted changed rich content after upload: %+v, %v", r, err)
	}
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Fatalf("pre-send failure changed journal: %v", err)
	}
}

func TestDirectReplyPlainIncomingHashUsesExactBytes(t *testing.T) {
	s, w, journal := directReplyFixture(t)
	w.incoming.Message += "\n"
	hash := topicTextHash(w.incoming.Message)
	r, err := s.SendDirectReply(t.Context(), w.manifest, journal, false)
	if err == nil || !strings.Contains(err.Error(), hash) || r.IncomingText != w.incoming.Message || r.Status == "ready" || w.sends != 0 {
		t.Fatalf("plain input was normalized or its diagnostic was lost: %+v, %v", r, err)
	}
	w.manifest.ReplyToTextSHA256 = hash
	r, err = s.SendDirectReply(t.Context(), w.manifest, journal, false)
	if err != nil || r.Status != "ready" || w.sends != 0 {
		t.Fatalf("exact plain hash was rejected: %+v, %v", r, err)
	}
}

func TestDirectReplyXLSXUnknownOutcomeNeverSendsAgain(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(*directReplyWire)
		want    string
	}{
		{"lost response", func(w *directReplyWire) { w.failSend = true }, "outcome unknown"},
		{"wrong caption", func(w *directReplyWire) {
			w.alterDoc = func(message *tg.Message, _ *tg.Document) { message.Message += "!" }
		}, "identity or content"},
		{"wrong filename", func(w *directReplyWire) {
			w.alterDoc = func(_ *tg.Message, document *tg.Document) {
				document.Attributes = []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "Other.xlsx"}}
			}
		}, "filename/MIME/size"},
		{"short download", func(w *directReplyWire) { w.serverData = w.fileData[1:] }, "size or SHA-256"},
		{"corrupt download", func(w *directReplyWire) {
			w.serverData = bytes.Clone(w.fileData)
			w.serverData[0]++
		}, "size or SHA-256"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, w, journal := directReplyXLSXFixture(t, "Ответ", xlsxFixtureOverride)
			tc.prepare(w)
			r, err := s.SendDirectReply(t.Context(), w.manifest, journal, true)
			if err == nil || !strings.Contains(err.Error(), tc.want) || r.Status != "outcome_unknown" || r.ServerVerified || w.uploads != 1 || w.sends != 1 {
				t.Fatalf("unexpected failed send: %+v, %v", r, err)
			}
			r, err = s.SendDirectReply(t.Context(), w.manifest, journal, true)
			if err == nil || r.Status != "reconcile_required" || r.ServerVerified || w.uploads != 1 || w.sends != 1 {
				t.Fatalf("unsafe retry: %+v, %v", r, err)
			}
			if tc.name == "corrupt download" {
				w.serverData = w.fileData
				r, err = s.SendDirectReply(t.Context(), w.manifest, journal, true)
				if err != nil || r.Status != "already_sent" || !r.ServerVerified || w.uploads != 1 || w.sends != 1 {
					t.Fatalf("could not recover known pending message: %+v, %v", r, err)
				}
			}
		})
	}
}
