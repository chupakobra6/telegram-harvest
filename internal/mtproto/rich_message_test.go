package mtproto

import (
	"context"
	"strings"
	"testing"

	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
)

func TestPrepareAndNormalizeRichMessagePreservesCustomEmoji(t *testing.T) {
	for _, tt := range []struct {
		name string
		alt  string
		want string
	}{
		{name: "fallback text", alt: "🏠", want: "🏠"},
		{name: "missing fallback text", want: "[custom emoji -123]"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			msg := &tg.Message{ID: 24474, Date: 1700000000}
			msg.SetRichMessage(tg.RichMessage{Blocks: []tg.PageBlockClass{
				&tg.PageBlockParagraph{Text: &tg.TextConcat{Texts: []tg.RichTextClass{
					&tg.TextPlain{Text: "Plan "},
					&tg.TextBold{Text: &tg.TextCustomEmoji{DocumentID: -123, Alt: tt.alt}},
					&tg.TextURL{Text: &tg.TextPlain{Text: " details"}, URL: "https://example.org/plan"},
				}}},
			}})
			if err := (&Session{}).prepareRichMessage(context.Background(), resolvedTarget{}, msg); err != nil {
				t.Fatalf("complete custom emoji message rejected: %v", err)
			}
			record, ok := normalizeRecord(msg, harvest.Chat{ID: 42, Type: "group"}, peer.Entities{})
			if !ok || record.Text != "Plan "+tt.want+" details" || len(record.Links) != 1 || record.Links[0] != "https://example.org/plan" {
				t.Fatalf("custom emoji content lost: %+v", record)
			}
		})
	}
}

func TestNormalizeRichRecruiterMessagePreservesParagraphsListsAndLinks(t *testing.T) {
	msg := &tg.Message{ID: 170, Date: 1700000000}
	msg.SetRichMessage(tg.RichMessage{Blocks: []tg.PageBlockClass{
		&tg.PageBlockParagraph{Text: &tg.TextConcat{Texts: []tg.RichTextClass{&tg.TextPlain{Text: "Hello, "}, &tg.TextBold{Text: &tg.TextPlain{Text: "candidate!"}}}}},
		&tg.PageBlockList{Items: []tg.PageListItemClass{&tg.PageListItemText{Text: &tg.TextPlain{Text: "Budget up to 300000"}}, &tg.PageListItemText{Text: &tg.TextPlain{Text: "Office or hybrid"}}}},
		&tg.PageBlockParagraph{Text: &tg.TextURL{Text: &tg.TextPlain{Text: "Details"}, URL: "https://example.org/job"}},
	}})
	record, ok := normalizeRecord(msg, harvest.Chat{ID: 42, Type: "user"}, peer.Entities{})
	if !ok || record.MessageID != 170 || record.Text != "Hello, candidate!\n\n- Budget up to 300000\n- Office or hybrid\n\nDetails" || len(record.Links) != 1 || record.Links[0] != "https://example.org/job" {
		t.Fatalf("rich content lost: %+v", record)
	}
	msg.Message = "Separate plain text"
	record, ok = normalizeRecord(msg, record.Chat, peer.Entities{})
	if !ok || !strings.HasPrefix(record.Text, "Separate plain text\n\nHello") {
		t.Fatalf("plain part was lost: %+v", record)
	}
}

func TestRichMessageMediaPreservesCaptionsMetadataAndDownloadIndexes(t *testing.T) {
	document := func(id int64) *tg.Document {
		return &tg.Document{ID: id, MimeType: "application/pdf", Size: 7, Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "notes.pdf"}}}
	}
	ordinary := &tg.MessageMediaDocument{}
	ordinary.SetDocument(document(100))
	msg := &tg.Message{ID: 17, Date: 1700000000, Media: ordinary, Message: "Обычный текст"}
	msg.SetRichMessage(tg.RichMessage{Photos: []tg.PhotoClass{&tg.Photo{ID: 200, Date: 1700000000, Sizes: []tg.PhotoSizeClass{&tg.PhotoSize{Type: "y", W: 640, H: 480, Size: 9}}}}, Documents: []tg.DocumentClass{document(300)}, Blocks: []tg.PageBlockClass{
		&tg.PageBlockCover{Cover: &tg.PageBlockPhoto{PhotoID: 200, Caption: tg.PageCaption{Text: &tg.TextPlain{Text: "Схема"}, Credit: &tg.TextPlain{Text: "Автор"}}}},
		&tg.PageBlockDocument{DocumentID: 300, Caption: tg.PageCaption{Text: &tg.TextURL{Text: &tg.TextPlain{Text: "Материалы"}, URL: "https://example.org/materials"}}},
	}})
	record, ok := normalizeRecord(msg, harvest.Chat{ID: 42, Type: "group"}, peer.Entities{})
	if !ok || record.Text != "Обычный текст\n\nСхема\nАвтор\n\nМатериалы" || len(record.Attachments) != 3 || len(record.Links) != 1 {
		t.Fatalf("rich content lost: %+v", record)
	}
	if record.Attachments[0].MediaID != "document:100" || record.Attachments[1].MediaID != "photo:200:y" || record.Attachments[1].Size != 9 || record.Attachments[2].MediaID != "document:300" || record.Attachments[2].FileName != "notes.pdf" {
		t.Fatalf("rich metadata lost: %+v", record.Attachments)
	}
	session := &Session{client: &telegram.Client{}}
	plan := newMediaDownloadPlan()
	session.planRecordMediaDownloads(msg, &record, harvest.HistoryOptions{DownloadMedia: true, MediaDir: t.TempDir()}, nil, plan)
	if len(plan.tasks) != 3 {
		t.Fatalf("download plan has %d tasks, want 3", len(plan.tasks))
	}
	paths := map[string]bool{}
	for _, attachment := range record.Attachments {
		if attachment.DownloadError != "" || attachment.LocalPath == "" || paths[attachment.LocalPath] {
			t.Fatalf("attachment index collision or missing download: %+v", record.Attachments)
		}
		paths[attachment.LocalPath] = true
	}
}

func TestRichMessageCannotSilentlyDropPartialOrUnsupportedContent(t *testing.T) {
	for _, rich := range []tg.RichMessage{
		{Part: true},
		{Blocks: []tg.PageBlockClass{&tg.PageBlockUnsupported{}}},
		{Documents: []tg.DocumentClass{&tg.DocumentEmpty{ID: 123}}},
		{Blocks: []tg.PageBlockClass{&tg.PageBlockDocument{DocumentID: 123}}},
	} {
		msg := &tg.Message{ID: 1}
		msg.SetRichMessage(rich)
		if _, _, err := richMessageContent(rich); err == nil {
			t.Fatal("incomplete rich content accepted")
		}
		if _, ok := normalizeRecord(msg, harvest.Chat{}, peer.Entities{}); ok {
			t.Fatal("empty record would conceal incomplete content")
		}
	}
}

func TestRichMediaIDsRemainOpaqueSignedValues(t *testing.T) {
	media, err := richMessageMedia(tg.RichMessage{Documents: []tg.DocumentClass{&tg.Document{ID: -123}}})
	if err != nil || len(media) != 1 {
		t.Fatalf("signed media ID was rejected: %v, %v", media, err)
	}
}
