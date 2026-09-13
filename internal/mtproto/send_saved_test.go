package mtproto

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chupakobra6/telegram-harvest/internal/config"
	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/gotd/td/tg"
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
