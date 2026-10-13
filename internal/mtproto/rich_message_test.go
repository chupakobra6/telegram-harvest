package mtproto

import (
	"strings"
	"testing"

	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
)

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

func TestRichMessageCannotSilentlyDropPartialOrUnsupportedContent(t *testing.T) {
	for _, rich := range []tg.RichMessage{
		{Part: true},
		{Blocks: []tg.PageBlockClass{&tg.PageBlockUnsupported{}}},
		{Documents: []tg.DocumentClass{&tg.Document{ID: 123}}},
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
