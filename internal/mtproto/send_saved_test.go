package mtproto

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/chupakobra6/telegram-harvest/internal/config"
	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestSendSavedRejectsStudyProfileBeforeConnecting(t *testing.T) {
	client := New(config.Config{Mode: config.ModeStudy})
	_, err := client.SendSaved(context.Background(), SendSavedOptions{Text: "test"})
	if err == nil || !strings.Contains(err.Error(), "only for profile main") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateSavedMessagesProfileRequiresPheik13(t *testing.T) {
	if err := validateSavedMessagesProfile(harvest.SelfProfile{Username: "pheik13"}); err != nil {
		t.Fatal(err)
	}
	for _, username := range []string{"Pheik4", "Pheik15", ""} {
		err := validateSavedMessagesProfile(harvest.SelfProfile{Username: username})
		if err == nil || !strings.Contains(err.Error(), "@Pheik13 is required") {
			t.Fatalf("username=%q err=%v", username, err)
		}
	}
}

func TestNormalizeSendSavedOptionsRequiresExactlyOnePayload(t *testing.T) {
	for _, opts := range []SendSavedOptions{
		{},
		{Text: "hello", FilePath: "/tmp/file.pdf"},
		{Text: "hello", SourceChat: "123", SourceMessageID: 7},
		{FilePath: "/tmp/file.pdf", SourceChat: "123", SourceMessageID: 7},
		{SourceChat: "123"},
		{SourceMessageID: 7},
		{SourceChat: "123", SourceMessageID: 7, Caption: "changed"},
	} {
		if _, _, err := normalizeSendSavedOptions(opts); err == nil {
			t.Fatalf("expected payload validation error for %+v", opts)
		}
	}
	if _, _, err := normalizeSendSavedOptions(SendSavedOptions{Text: "hello", Caption: "caption"}); err == nil {
		t.Fatal("caption without file must fail")
	}
}

func TestNormalizeSendSavedOptionsAcceptsTelegramVideoSource(t *testing.T) {
	normalized, metadata, err := normalizeSendSavedOptions(SendSavedOptions{
		SourceChat:      " 3978717463 ",
		SourceMessageID: 14787,
	})
	if err != nil {
		t.Fatal(err)
	}
	if metadata != nil || normalized.SourceChat != "3978717463" || normalized.SourceMessageID != 14787 {
		t.Fatalf("normalized=%+v metadata=%+v", normalized, metadata)
	}
}

func TestNormalizeSendSavedFilePinsFilenameMIMEAndSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Договор.pdf")
	if err := os.WriteFile(path, []byte("pdf-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	normalized, metadata, err := normalizeSendSavedOptions(SendSavedOptions{FilePath: path, Caption: "Подписать"})
	if err != nil {
		t.Fatal(err)
	}
	if normalized.FilePath != path || metadata.Path != path {
		t.Fatalf("paths = %q, %q", normalized.FilePath, metadata.Path)
	}
	if metadata.Name != "Договор.pdf" || metadata.MIMEType != "application/pdf" || metadata.Size != 9 {
		t.Fatalf("metadata = %+v", metadata)
	}
}

func TestVerifySavedMessageChecksSelfChatAndFileMetadata(t *testing.T) {
	profile := harvest.SelfProfile{ID: 42, Username: SavedMessagesMainUsername}
	opts := SendSavedOptions{FilePath: "/tmp/Договор.pdf", Caption: "Подписать"}
	metadata := &savedFileMetadata{Name: "Договор.pdf", MIMEType: "application/pdf", Size: 123}
	record := harvest.MessageRecord{
		Chat: harvest.Chat{ID: 42, Username: SavedMessagesMainUsername},
		Text: "Подписать",
		Attachments: []harvest.Attachment{{
			FileName: "Договор.pdf",
			MIMEType: "application/pdf",
			Size:     123,
		}},
	}
	if err := verifySavedMessage(record, profile, opts, metadata); err != nil {
		t.Fatal(err)
	}
	if record.Outgoing {
		t.Fatal("fixture must cover Telegram's Saved Messages readback without an outgoing flag")
	}
	record.Chat.ID = 99
	if err := verifySavedMessage(record, profile, opts, metadata); err == nil {
		t.Fatal("different chat must fail verification")
	}
}

type savedLocalFileWire struct {
	message      *tg.Message
	upload       []byte
	uploadedName string
	sentName     string
	sends        int
	mutate       func(*tg.Message)
	nativeVideo  bool
	files        map[int64][]byte
	sentMedia    *tg.InputMediaUploadedDocument
	sendErr      error
	afterUpload  func()
}

func (f *savedLocalFileWire) Invoke(_ context.Context, input bin.Encoder, output bin.Decoder) error {
	switch req := input.(type) {
	case *tg.UsersGetFullUserRequest:
		user := &tg.User{ID: 42}
		user.SetUsername(SavedMessagesMainUsername)
		*output.(*tg.UsersUserFull) = tg.UsersUserFull{
			FullUser: tg.UserFull{ID: 42},
			Users:    []tg.UserClass{user},
		}
	case *tg.UploadSaveFilePartRequest:
		f.upload = append(f.upload, req.Bytes...)
		if f.files == nil {
			f.files = map[int64][]byte{}
		}
		f.files[req.FileID] = append(f.files[req.FileID], req.Bytes...)
		if f.afterUpload != nil {
			f.afterUpload()
		}
		output.(*tg.BoolBox).Bool = &tg.BoolTrue{}
	case *tg.MessagesSendMediaRequest:
		f.sends++
		var encoded bin.Buffer
		if err := req.Encode(&encoded); err != nil {
			return err
		}
		var wire tg.MessagesSendMediaRequest
		if err := wire.Decode(&encoded); err != nil {
			return err
		}
		if _, ok := wire.Peer.(*tg.InputPeerSelf); !ok {
			return fmt.Errorf("unexpected recipient %T", wire.Peer)
		}
		media, ok := wire.Media.(*tg.InputMediaUploadedDocument)
		if !ok || media.ForceFile == f.nativeVideo {
			return fmt.Errorf("unexpected uploaded document kind, got %T", wire.Media)
		}
		f.sentMedia = media
		if f.sendErr != nil {
			return f.sendErr
		}
		var fileID int64
		switch file := media.File.(type) {
		case *tg.InputFile:
			f.uploadedName = file.Name
			fileID = file.ID
		case *tg.InputFileBig:
			f.uploadedName = file.Name
			fileID = file.ID
		default:
			return fmt.Errorf("unexpected uploaded file %T", media.File)
		}
		attributes := append([]tg.DocumentAttributeClass(nil), media.Attributes...)
		for i, attr := range attributes {
			if filename, ok := attr.(*tg.DocumentAttributeFilename); ok {
				f.sentName = filename.FileName
				name := filename.FileName
				// Model the two observed M4A server readbacks, independently
				// of the client's expected metadata.
				if strings.EqualFold(filepath.Ext(name), ".m4a") {
					name = strings.ReplaceAll(name, "-", "_")
				}
				attributes[i] = &tg.DocumentAttributeFilename{FileName: name}
			}
		}
		readbackMedia := &tg.MessageMediaDocument{}
		readbackMedia.SetDocument(&tg.Document{
			ID: 456, MimeType: media.MimeType, Size: int64(len(f.files[fileID])), Attributes: attributes,
		})
		f.message = &tg.Message{
			ID: 123, PeerID: &tg.PeerUser{UserID: 42}, Message: wire.Message, Media: readbackMedia,
		}
		output.(*tg.UpdatesBox).Updates = &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: f.message}}}
	case *tg.MessagesGetMessagesRequest:
		if f.mutate != nil {
			f.mutate(f.message)
		}
		output.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{f.message}}
	default:
		return fmt.Errorf("unexpected RPC %T", input)
	}
	return nil
}

func TestSendSavedLocalVideoPreparesPreviewAndPreservesBytes(t *testing.T) {
	video := localReplacementVideo(t)
	opts, metadata, err := normalizeSendSavedOptions(SendSavedOptions{FilePath: video.Path, Caption: "  Доклад\n00:00 Начало  "})
	if err != nil {
		t.Fatal(err)
	}
	f, dir, err := prepareSavedLocalVideo(context.Background(), metadata)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if !reflect.DeepEqual(*f, video) {
		t.Fatalf("prepared metadata = %+v, want %+v", f, video)
	}
	preview, err := os.Open(filepath.Join(dir, "preview.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	defer preview.Close()
	config, format, err := image.DecodeConfig(preview)
	if err != nil || format != "jpeg" || config.Width > 320 || config.Height > 320 {
		t.Fatalf("preview = %+v, %s, %v", config, format, err)
	}
	for _, scenario := range []string{"missing-preview", "unknown-outcome", "changed-during-upload"} {
		t.Run(scenario, func(t *testing.T) {
			wire := &savedLocalFileWire{nativeVideo: true}
			if scenario == "unknown-outcome" {
				wire.sendErr = errors.New("connection lost")
			}
			if scenario == "changed-during-upload" {
				original, err := os.ReadFile(video.Path)
				if err != nil {
					t.Fatal(err)
				}
				defer os.WriteFile(video.Path, original, 0o600)
				wire.afterUpload = func() {
					changed := append([]byte(nil), original...)
					changed[len(changed)-1] ^= 1
					if err := os.WriteFile(video.Path, changed, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			session := &Session{raw: tg.NewClient(wire)}
			result, err := session.sendSaved(context.Background(), opts, metadata)
			if err == nil || result.Verified {
				t.Fatalf("unverified video accepted: %+v, %v", result, err)
			}
			if scenario == "changed-during-upload" {
				if wire.sends != 0 || !strings.Contains(err.Error(), "SHA-256 mismatch") {
					t.Fatalf("changed bytes dispatched: sends=%d err=%v", wire.sends, err)
				}
				return
			}
			if wire.sends != 1 || !strings.Contains(err.Error(), "do not retry blindly") {
				t.Fatalf("sends=%d err=%v", wire.sends, err)
			}
			media := wire.sentMedia
			if media.Thumb == nil || media.MimeType != "video/mp4" || media.NosoundVideo {
				t.Fatalf("native video media = %+v", media)
			}
			attrs := media.Attributes
			if len(attrs) != 2 || attrs[0].(*tg.DocumentAttributeFilename).FileName != video.Filename {
				t.Fatalf("attributes = %+v", attrs)
			}
			v := attrs[1].(*tg.DocumentAttributeVideo)
			if v.Duration != video.Duration || v.W != video.Width || v.H != video.Height || v.Nosound || !v.SupportsStreaming || v.RoundMessage {
				t.Fatalf("video attributes = %+v", v)
			}
			data, readErr := os.ReadFile(video.Path)
			if readErr != nil || !bytes.Equal(wire.files[media.File.(*tg.InputFile).ID], data) {
				t.Fatalf("uploaded video bytes changed: %v", readErr)
			}
			if scenario == "missing-preview" && (!strings.Contains(err.Error(), "preview readback failed") || wire.message.Message != "Доклад\n00:00 Начало") {
				t.Fatalf("caption/thumbnail guard: %+v, %v", wire.message, err)
			}
		})
	}
}

func TestSendSavedLocalVideoRejectsInvalidMP4BeforeUpload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.mp4")
	if err := os.WriteFile(path, []byte("not a video"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts, metadata, err := normalizeSendSavedOptions(SendSavedOptions{FilePath: path})
	if err != nil {
		t.Fatal(err)
	}
	wire := &savedLocalFileWire{nativeVideo: true}
	_, err = (&Session{raw: tg.NewClient(wire)}).sendSaved(context.Background(), opts, metadata)
	if err == nil || len(wire.upload) != 0 || wire.sends != 0 {
		t.Fatalf("invalid MP4 dispatched: upload=%d sends=%d err=%v", len(wire.upload), wire.sends, err)
	}
}

func TestSendSavedLocalVideoSilentAndAudioOnly(t *testing.T) {
	video := localReplacementVideo(t)
	for _, audioOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("audio-only=%t", audioOnly), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fixture.MP4")
			remove := "-an"
			if audioOnly {
				remove = "-vn"
			}
			if b, err := exec.Command("ffmpeg", "-v", "error", "-i", video.Path, remove, "-c", "copy", "-y", path).CombinedOutput(); err != nil {
				t.Fatalf("remux fixture: %s: %v", b, err)
			}
			opts, metadata, err := normalizeSendSavedOptions(SendSavedOptions{FilePath: path})
			if err != nil {
				t.Fatal(err)
			}
			wire := &savedLocalFileWire{nativeVideo: true, sendErr: errors.New("connection lost")}
			result, err := (&Session{raw: tg.NewClient(wire)}).sendSaved(context.Background(), opts, metadata)
			if err == nil || result.Verified {
				t.Fatalf("unexpected verification: %+v, %v", result, err)
			}
			if audioOnly {
				if wire.sends != 0 || len(wire.upload) != 0 || !strings.Contains(err.Error(), "must contain a video stream") {
					t.Fatalf("audio-only MP4 dispatched: sends=%d err=%v", wire.sends, err)
				}
				return
			}
			if wire.sends != 1 || !wire.sentMedia.NosoundVideo || !wire.sentMedia.Attributes[1].(*tg.DocumentAttributeVideo).Nosound {
				t.Fatalf("silent video metadata: media=%+v sends=%d err=%v", wire.sentMedia, wire.sends, err)
			}
		})
	}
}

func TestSendSavedLocalVideoPreviewLimitedRangeHEVC(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " unavailable")
		}
	}
	path := filepath.Join(t.TempDir(), "limited-hevc.mp4")
	args := []string{"-v", "error", "-f", "lavfi", "-i", "color=size=128x72:rate=4:duration=1", "-an",
		"-c:v", "libx265", "-preset", "ultrafast", "-x265-params", "pools=1:frame-threads=1:log-level=error",
		"-pix_fmt", "yuv420p", "-color_range", "tv", "-tag:v", "hvc1", "-movflags", "+faststart", "-y", path}
	if b, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("HEVC fixture: %s: %v", b, err)
	}
	stream, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_name,color_range", "-of", "default=nw=1", path).Output()
	if err != nil || !strings.Contains(string(stream), "codec_name=hevc") || !strings.Contains(string(stream), "color_range=tv") {
		t.Fatalf("fixture is not limited-range HEVC: %s: %v", stream, err)
	}
	_, metadata, err := normalizeSendSavedOptions(SendSavedOptions{FilePath: path})
	if err != nil {
		t.Fatal(err)
	}
	f, dir, err := prepareSavedLocalVideo(context.Background(), metadata)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if f.Width != 128 || f.Height != 72 || f.HasAudio || f.Duration != 1 {
		t.Fatalf("HEVC metadata = %+v", f)
	}
	preview, err := os.Open(filepath.Join(dir, "preview.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	defer preview.Close()
	img, format, err := image.Decode(preview)
	if err != nil || format != "jpeg" || img.Bounds().Dx() <= 0 || img.Bounds().Dy() <= 0 {
		t.Fatalf("generated HEVC preview is not a readable JPEG: %s: %v", format, err)
	}
	if err := verifyPublishFile(context.Background(), f); err != nil {
		t.Fatalf("HEVC source bytes changed during preview generation: %v", err)
	}
}

func TestSendSavedLocalFileUsesExactOutgoingFilename(t *testing.T) {
	data := []byte("unchanged audio bytes")
	for _, names := range []struct{ source, outgoing string }{
		{"01_Андрей_Зорин_Домашняя_ИИ-лаборатория.m4a", "01_Андрей_Зорин_Домашняя_ИИ_лаборатория.m4a"},
		{"03_Александр_Подмосковный_Флант_AI-ассистент.m4a", "03_Александр_Подмосковный_Флант_AI_ассистент.m4a"},
		{"Talk-AI.M4A", "Talk_AI.M4A"},
		{"Доклад-пример.pdf", "Доклад-пример.pdf"},
	} {
		path := filepath.Join(t.TempDir(), names.source)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		for _, caption := range []string{"", "Описание доклада"} {
			for _, changedName := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/caption=%t/changed_filename=%t", names.source, caption != "", changedName), func(t *testing.T) {
					opts, metadata, err := normalizeSendSavedOptions(SendSavedOptions{FilePath: path, Caption: caption})
					if err != nil {
						t.Fatal(err)
					}
					if opts.FilePath != path || metadata.Path != path {
						t.Fatalf("source path changed: opts=%+v metadata=%+v", opts, metadata)
					}
					fake := &savedLocalFileWire{}
					if changedName {
						fake.mutate = func(msg *tg.Message) {
							doc := msg.Media.(*tg.MessageMediaDocument).Document.(*tg.Document)
							doc.Attributes = []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: names.outgoing + ".unexpected"}}
						}
					}
					session := &Session{raw: tg.NewClient(fake)}
					result, err := session.sendSaved(context.Background(), opts, metadata)
					if changedName {
						if err == nil || result.Verified || !strings.Contains(err.Error(), "filename readback mismatch") {
							t.Fatalf("changed filename was accepted: result=%+v err=%v", result, err)
						}
					} else if err != nil || !result.Verified || result.Message.Attachments[0].FileName != names.outgoing {
						t.Fatalf("exact filename failed: result=%+v err=%v", result, err)
					}
					if fake.sends != 1 || !bytes.Equal(fake.upload, data) {
						t.Fatalf("sends=%d uploaded=%q", fake.sends, fake.upload)
					}
					if metadata.Name != names.outgoing || fake.uploadedName != names.outgoing || fake.sentName != names.outgoing {
						t.Fatalf("wire names: upload=%q document=%q, want %q", fake.uploadedName, fake.sentName, names.outgoing)
					}
				})
			}
		}
	}
}

func TestVerifyCopiedSavedMessagePreservesTelegramVideo(t *testing.T) {
	profile := harvest.SelfProfile{ID: 42, Username: SavedMessagesMainUsername}
	attachment := harvest.Attachment{
		Kind:            "video",
		MediaID:         "document:9001",
		FileName:        "Доклад.mp4",
		MIMEType:        "video/mp4",
		Size:            123456,
		DurationSeconds: 2701.5,
		Width:           1920,
		Height:          1080,
	}
	source := harvest.MessageRecord{
		Chat:        harvest.Chat{ID: 77, Display: "Source"},
		MessageID:   123,
		Text:        "Доклад\nТаймкоды",
		Attachments: []harvest.Attachment{attachment},
	}
	destination := source
	destination.Chat = harvest.Chat{ID: 42, Username: SavedMessagesMainUsername}
	destination.MessageID = 456
	destination.Attachments = append([]harvest.Attachment(nil), source.Attachments...)
	destination.Attachments[0].MediaID = "document:9002"
	evidence := savedVideoEvidence{DocumentID: 9001, HasPreview: true, Video: true, SupportsStreaming: true}
	destinationEvidence := evidence
	destinationEvidence.DocumentID = 9002
	if err := verifyCopiedSavedMessage(destination, source, profile, evidence, destinationEvidence); err != nil {
		t.Fatal(err)
	}

	destination.Attachments[0].MediaID = ""
	if err := verifyCopiedSavedMessage(destination, source, profile, evidence, destinationEvidence); err == nil {
		t.Fatal("missing destination Telegram media id must fail")
	}
	destination.Attachments[0].MediaID = "document:9002"
	withoutPreview := destinationEvidence
	withoutPreview.HasPreview = false
	if err := verifyCopiedSavedMessage(destination, source, profile, evidence, withoutPreview); err == nil {
		t.Fatal("missing destination preview must fail")
	}
}

func TestSavedVideoEvidenceReadsPreviewAndStreamingAttributes(t *testing.T) {
	message := &tg.Message{
		Media: &tg.MessageMediaDocument{
			Video: true,
			Document: &tg.Document{
				ID:       9001,
				MimeType: "video/mp4",
				Thumbs: []tg.PhotoSizeClass{
					&tg.PhotoSize{Type: "m", W: 320, H: 180, Size: 1234},
				},
				Attributes: []tg.DocumentAttributeClass{
					&tg.DocumentAttributeFilename{FileName: "Доклад.mp4"},
					&tg.DocumentAttributeVideo{Duration: 2701.5, W: 1920, H: 1080, SupportsStreaming: true},
				},
			},
		},
	}
	evidence, err := savedVideoEvidenceFromMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.DocumentID != 9001 || !evidence.HasPreview || !evidence.Video || !evidence.SupportsStreaming {
		t.Fatalf("evidence=%+v", evidence)
	}
}

func TestSavedVideoThumbnailLocationSelectsLargestStaticPreview(t *testing.T) {
	document := &tg.Document{
		ID:            9001,
		AccessHash:    42,
		FileReference: []byte("reference"),
		Thumbs: []tg.PhotoSizeClass{
			&tg.PhotoSize{Type: "s", W: 90, H: 90, Size: 100},
			&tg.PhotoSize{Type: "m", W: 320, H: 180, Size: 200},
		},
	}
	location, size, ok := savedVideoThumbnailLocation(document)
	if !ok || size != 200 {
		t.Fatalf("location=%+v size=%d ok=%t", location, size, ok)
	}
	typed, ok := location.(*tg.InputDocumentFileLocation)
	if !ok || typed.ID != document.ID || typed.AccessHash != document.AccessHash || typed.ThumbSize != "m" {
		t.Fatalf("location=%+v", location)
	}
}

func TestSavedVideoServerReuseSendsSelfDocumentWithoutFullTransfer(t *testing.T) {
	source := &tg.Message{Message: " exact caption\n", Entities: []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 1, Length: 5}}, Media: &tg.MessageMediaDocument{Spoiler: true}}
	document := &tg.Document{ID: 9001, AccessHash: 42, FileReference: []byte("current-reference"), Size: 2300000000}
	request := savedDocumentReuseRequest(source, document, 123)
	calls := 0
	updates := &tg.Updates{}
	got, method, hash, err := sendSavedVideoCopy(context.Background(), request, true, func(_ context.Context, req *tg.MessagesSendMediaRequest) (tg.UpdatesClass, error) {
		calls++
		// Roundtrip verifies that Telegram receives entities and spoiler flags.
		var encoded bin.Buffer
		if err := req.Encode(&encoded); err != nil {
			t.Fatal(err)
		}
		var wire tg.MessagesSendMediaRequest
		if err := wire.Decode(&encoded); err != nil {
			t.Fatal(err)
		}
		if _, ok := wire.Peer.(*tg.InputPeerSelf); !ok {
			t.Fatalf("recipient = %T", wire.Peer)
		}
		media, ok := wire.Media.(*tg.InputMediaDocument)
		if !ok || !media.Spoiler {
			t.Fatalf("media = %+v", wire.Media)
		}
		input, ok := media.ID.(*tg.InputDocument)
		if !ok || input.ID != document.ID || input.AccessHash != document.AccessHash || !reflect.DeepEqual(input.FileReference, document.FileReference) {
			t.Fatalf("document = %+v", media.ID)
		}
		if wire.Message != source.Message || !reflect.DeepEqual(wire.Entities, source.Entities) || wire.RandomID != 123 {
			t.Fatalf("caption or identity changed: %+v", wire)
		}
		return updates, nil
	}, func() (tg.InputMediaClass, string, error) {
		t.Fatal("server reuse must not download/hash/upload full video")
		return nil, "", nil
	})
	if err != nil || got != updates || method != savedVideoServerReuse || hash != "" || calls != 1 {
		t.Fatalf("got=%v method=%q hash=%q calls=%d err=%v", got, method, hash, calls, err)
	}
}

func TestSavedVideoCopyStopsOnUncertainAndPrivacyErrors(t *testing.T) {
	for _, sendErr := range []error{context.DeadlineExceeded, errors.New("connection lost"), tgerr.New(400, "CHAT_FORWARDS_RESTRICTED"), tgerr.New(420, "FLOOD_WAIT_3"), tgerr.New(500, "FILE_REFERENCE_EXPIRED"), tgerr.New(400, "DOCUMENT_INVALID")} {
		t.Run(sendErr.Error(), func(t *testing.T) {
			calls := 0
			_, method, hash, err := sendSavedVideoCopy(context.Background(), &tg.MessagesSendMediaRequest{Peer: &tg.InputPeerSelf{}}, true, func(context.Context, *tg.MessagesSendMediaRequest) (tg.UpdatesClass, error) {
				calls++
				return nil, sendErr
			}, func() (tg.InputMediaClass, string, error) {
				t.Fatal("uncertain or privacy error must not fall back")
				return nil, "", nil
			})
			if !errors.Is(err, sendErr) || calls != 1 || method != "" || hash != "" {
				t.Fatalf("method=%q hash=%q calls=%d err=%v", method, hash, calls, err)
			}
		})
	}
}

func TestSavedVideoCopyFallsBackOnlyAfterFileReferenceRejection(t *testing.T) {
	for _, reject := range []string{"FILE_REFERENCE_EXPIRED", "FILE_REFERENCE_INVALID"} {
		t.Run(reject, func(t *testing.T) {
			calls, uploads := 0, 0
			request := &tg.MessagesSendMediaRequest{Peer: &tg.InputPeerSelf{}, Media: &tg.InputMediaDocument{}, RandomID: 123, Message: "unchanged"}
			_, method, hash, err := sendSavedVideoCopy(context.Background(), request, true, func(_ context.Context, req *tg.MessagesSendMediaRequest) (tg.UpdatesClass, error) {
				calls++
				if req.RandomID != 123 || req.Message != "unchanged" {
					t.Fatal("fallback changed send identity or caption")
				}
				if calls == 1 {
					return nil, fmt.Errorf("wrapped rejection: %w", tgerr.New(400, reject))
				}
				if _, ok := req.Media.(*tg.InputMediaUploadedDocument); !ok {
					t.Fatalf("fallback media = %T", req.Media)
				}
				return &tg.Updates{}, nil
			}, func() (tg.InputMediaClass, string, error) {
				uploads++
				return &tg.InputMediaUploadedDocument{}, "source-hash", nil
			})
			if err != nil || calls != 2 || uploads != 1 || method != savedVideoExactUpload || hash != "source-hash" {
				t.Fatalf("method=%q hash=%q calls=%d uploads=%d err=%v", method, hash, calls, uploads, err)
			}
		})
	}
}

func TestSavedVideoProtectedSourceKeepsExistingUploadPath(t *testing.T) {
	calls, uploads := 0, 0
	_, method, hash, err := sendSavedVideoCopy(context.Background(), &tg.MessagesSendMediaRequest{Peer: &tg.InputPeerSelf{}, Media: &tg.InputMediaDocument{}}, false, func(_ context.Context, req *tg.MessagesSendMediaRequest) (tg.UpdatesClass, error) {
		calls++
		if uploads != 1 {
			t.Fatal("protected source attempted native reuse before upload")
		}
		if _, ok := req.Media.(*tg.InputMediaUploadedDocument); !ok {
			t.Fatalf("media = %T", req.Media)
		}
		return &tg.Updates{}, nil
	}, func() (tg.InputMediaClass, string, error) {
		uploads++
		return &tg.InputMediaUploadedDocument{}, "hash", nil
	})
	if err != nil || calls != 1 || uploads != 1 || method != savedVideoExactUpload || hash != "hash" {
		t.Fatalf("method=%q hash=%q calls=%d uploads=%d err=%v", method, hash, calls, uploads, err)
	}
}

func TestSavedVideoReadbackRequiresExactEntitiesAndSameDocumentForReuse(t *testing.T) {
	source := &tg.Message{Message: "caption", Entities: []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 0, Length: 7}}}
	destination := &tg.Message{Message: source.Message, Entities: source.Entities}
	before := savedVideoEvidence{DocumentID: 9001}
	after := savedVideoEvidence{DocumentID: 9002}
	if err := verifySavedVideoCopyMethod(source, destination, savedVideoServerReuse, before, after); err == nil {
		t.Fatal("reuse must reject different document bytes")
	}
	if err := verifySavedVideoCopyMethod(source, destination, savedVideoExactUpload, before, after); err != nil {
		t.Fatal(err)
	}
	after.DocumentID = before.DocumentID
	if err := verifySavedVideoCopyMethod(source, destination, savedVideoServerReuse, before, after); err != nil {
		t.Fatal(err)
	}
	destination.Entities = nil
	if err := verifySavedVideoCopyMethod(source, destination, savedVideoServerReuse, before, after); err == nil {
		t.Fatal("changed entities must fail readback")
	}
	destination.Entities = source.Entities
	destination.Message = "caption "
	if err := verifySavedVideoCopyMethod(source, destination, savedVideoServerReuse, before, after); err == nil {
		t.Fatal("changed whitespace must fail exact caption readback")
	}
}

func TestSavedVideoWithoutPreviewCanReuseSameServerDocument(t *testing.T) {
	source := &tg.Message{Message: "caption", Media: &tg.MessageMediaDocument{Document: &tg.Document{ID: 9001, MimeType: "video/mp4", Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeVideo{Duration: 15, W: 1920, H: 1080}}}}}
	evidence, err := savedVideoEvidenceFromMessage(source)
	if err != nil || evidence.HasPreview {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
	if err := verifySavedVideoCopyMethod(source, source, savedVideoServerReuse, evidence, evidence); err != nil {
		t.Fatal(err)
	}
	profile := harvest.SelfProfile{ID: 42, Username: SavedMessagesMainUsername}
	record := harvest.MessageRecord{Chat: harvest.Chat{ID: 42, Username: SavedMessagesMainUsername}, Text: "caption", Attachments: []harvest.Attachment{{Kind: "video", MediaID: "document:9001", MIMEType: "video/mp4", Width: 1920, Height: 1080, DurationSeconds: 15}}}
	if err := verifyCopiedSavedMessage(record, record, profile, evidence, evidence); err != nil {
		t.Fatal(err)
	}
	_, document, _ := savedVideoDocumentFromMessage(source)
	_, method, _, err := sendSavedVideoCopy(context.Background(), savedDocumentReuseRequest(source, document, 123), true, func(context.Context, *tg.MessagesSendMediaRequest) (tg.UpdatesClass, error) {
		return &tg.Updates{}, nil
	}, func() (tg.InputMediaClass, string, error) {
		t.Fatal("video without thumbnail must use server reuse")
		return nil, "", nil
	})
	if err != nil || method != savedVideoServerReuse {
		t.Fatalf("method=%q err=%v", method, err)
	}
	if err := validateSavedVideoUploadPreview(document); err == nil {
		t.Fatal("upload without static preview must stop before full transfer")
	}
	document.Thumbs = []tg.PhotoSizeClass{&tg.PhotoSize{Type: "m", W: 320, H: 180, Size: 100}}
	if err := validateSavedVideoUploadPreview(document); err != nil {
		t.Fatal(err)
	}
}

func TestSavedVideoCoverIsCopiedAndUsedAsPreview(t *testing.T) {
	cover := &tg.Photo{ID: 7001, AccessHash: 42, FileReference: []byte("cover-reference"), Sizes: []tg.PhotoSizeClass{&tg.PhotoSize{Type: "m", W: 320, H: 180, Size: 100}}}
	document := &tg.Document{ID: 9001}
	source := &tg.Message{Media: &tg.MessageMediaDocument{Document: document, VideoCover: cover}}
	request := savedDocumentReuseRequest(source, document, 123)
	inputCover, ok := request.Media.(*tg.InputMediaDocument).VideoCover.(*tg.InputPhoto)
	if !ok || inputCover.ID != cover.ID || inputCover.AccessHash != cover.AccessHash || !reflect.DeepEqual(inputCover.FileReference, cover.FileReference) {
		t.Fatalf("cover=%+v", inputCover)
	}
	location, size, ok := savedVideoPreviewLocation(source)
	if !ok || size != 100 || location.(*tg.InputPhotoFileLocation).ID != cover.ID {
		t.Fatalf("location=%+v size=%d", location, size)
	}
	evidence := savedVideoEvidence{DocumentID: document.ID, HasPreview: true}
	if err := verifySavedVideoCopyMethod(source, source, savedVideoServerReuse, evidence, evidence); err != nil {
		t.Fatal(err)
	}
	destination := &tg.Message{Media: &tg.MessageMediaDocument{Document: document}}
	if err := verifySavedVideoCopyMethod(source, destination, savedVideoServerReuse, evidence, evidence); err == nil {
		t.Fatal("missing custom cover must fail readback")
	}
}

func nativeSavedAudioFixture() (harvest.MessageRecord, harvest.MessageRecord, harvest.SelfProfile, *tg.Message, *tg.Message) {
	profile := harvest.SelfProfile{ID: 42, Username: SavedMessagesMainUsername}
	source := harvest.MessageRecord{Chat: harvest.Chat{ID: 77}, Text: "call", Attachments: []harvest.Attachment{{Kind: "audio", MediaID: "document:9003", FileName: "call.m4a", MIMEType: "audio/mp4", Size: 12345}}}
	destination := source
	destination.Chat = harvest.Chat{ID: 42, Username: SavedMessagesMainUsername}
	destination.Attachments = append([]harvest.Attachment(nil), source.Attachments...)
	makeMessage := func() *tg.Message {
		return &tg.Message{Message: "call", Entities: []tg.MessageEntityClass{&tg.MessageEntityBold{Length: 4}}, Media: &tg.MessageMediaDocument{Document: &tg.Document{ID: 9003, Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeAudio{Duration: 120, Title: "Call", Performer: "Andrey"}}}}}
	}
	return source, destination, profile, makeMessage(), makeMessage()
}

func TestNativeSavedAudioReuseKeepsSelfDocumentAndCaption(t *testing.T) {
	source, destination, profile, original, copied := nativeSavedAudioFixture()
	if err := verifyCopiedSavedAudio(destination, source, profile, original, copied); err != nil {
		t.Fatal(err)
	}
	_, document, _ := savedVideoDocumentFromMessage(original)
	request := savedDocumentReuseRequest(original, document, 123)
	var encoded bin.Buffer
	if err := request.Encode(&encoded); err != nil {
		t.Fatal(err)
	}
	var wire tg.MessagesSendMediaRequest
	if err := wire.Decode(&encoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := wire.Peer.(*tg.InputPeerSelf); !ok {
		t.Fatalf("recipient=%T", wire.Peer)
	}
	if media, ok := wire.Media.(*tg.InputMediaDocument); !ok || media.ID.(*tg.InputDocument).ID != document.ID {
		t.Fatalf("media=%+v", wire.Media)
	}
	if wire.Message != original.Message || !reflect.DeepEqual(wire.Entities, original.Entities) {
		t.Fatal("native audio source caption/entities changed")
	}
}

func TestNativeSavedAudioReadbackRejectsChangedAttributesAndDestination(t *testing.T) {
	for _, field := range []string{"duration", "title", "performer", "voice", "docid", "entities", "caption", "self", "filename", "mime", "size"} {
		t.Run(field, func(t *testing.T) {
			source, destination, profile, original, copied := nativeSavedAudioFixture()
			_, document, _ := savedVideoDocumentFromMessage(copied)
			audio, _ := savedNativeAudioAttribute(document)
			switch field {
			case "duration":
				audio.Duration++
			case "title":
				audio.Title = "other"
			case "performer":
				audio.Performer = "other"
			case "voice":
				audio.Voice = true
			case "docid":
				document.ID++
			case "entities":
				copied.Entities = nil
			case "caption":
				copied.Message += " "
			case "self":
				destination.Chat.ID++
			case "filename":
				destination.Attachments[0].FileName = "other"
			case "mime":
				destination.Attachments[0].MIMEType = "application/octet-stream"
			case "size":
				destination.Attachments[0].Size++
			}
			if err := verifyCopiedSavedAudio(destination, source, profile, original, copied); err == nil {
				t.Fatal("changed native audio readback accepted")
			}
		})
	}
}

func TestNativeSavedAudioRejectsVoiceSource(t *testing.T) {
	if _, err := savedNativeAudioAttribute(&tg.Document{Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeAudio{Voice: true}}}); err == nil {
		t.Fatal("voice source accepted")
	}
	if audio, err := savedNativeAudioAttribute(&tg.Document{}); err != nil || audio != nil {
		t.Fatalf("generic document classified as audio: %v %v", audio, err)
	}
}
