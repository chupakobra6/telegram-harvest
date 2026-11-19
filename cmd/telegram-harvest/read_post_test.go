package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/config"
	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/chupakobra6/telegram-harvest/internal/mtproto"
)

func TestReadPostTextIncludesChatAndBody(t *testing.T) {
	result := mtproto.ReadPostResult{Scope: "message", GroupedMedia: true, Record: harvest.MessageRecord{
		SourceURL: "https://t.me/study_news/42", Date: time.Unix(1700000000, 0).UTC(),
		Chat:      harvest.Chat{ID: 123, Display: "Учебные новости", Username: "study_news"},
		MessageID: 42, Kind: "photo", Attachments: []harvest.Attachment{{Kind: "photo"}}, Text: "Подпись",
	}}
	var out strings.Builder
	if err := writeReadPostText(&out, result); err != nil {
		t.Fatal(err)
	}
	want := "https://t.me/study_news/42\nchat=\"Учебные новости\" @study_news\n" +
		"chat_id=123 message_id=42 date=2023-11-14T22:13:20Z kind=photo scope=message attachments=1 grouped_media=true media_downloaded=false\n\nПодпись\n"
	if out.String() != want {
		t.Fatalf("default output = %q, want %q", out.String(), want)
	}
}

func TestReadPostAllowlistMatchesResolvedChat(t *testing.T) {
	chat := harvest.Chat{ID: 123, Type: "supergroup", Username: "study_news"}
	for _, tc := range []struct {
		selector, allowed string
		mayMatch, matches bool
	}{
		{"@study_news", "123", true, true},
		{"@study_news", "supergroup:123", true, true},
		{"@study_news", "channel:123", true, false},
		{"@study_news", "user:123", false, false},
		{"@study_news", "@different_news", false, false},
		{"@alias_news", "@alias_news", true, true},
		{"123", "@study_news", true, true},
		{"123", "@different_news", true, false},
		{"123", "456", false, false},
	} {
		cfg := config.Config{Mode: config.ModeStudy, AllowedChats: []string{tc.allowed}}
		if got := postLinkMayMatchAllowlist(cfg, tc.selector); got != tc.mayMatch {
			t.Fatalf("selector %q with allowlist %q: preflight=%t, want %t", tc.selector, tc.allowed, got, tc.mayMatch)
		}
		if err := ensureResolvedPostChatAllowed(cfg, tc.selector, chat); (err == nil) != tc.matches {
			t.Fatalf("selector %q with allowlist %q: match error=%v, want %t", tc.selector, tc.allowed, err, tc.matches)
		}
	}
}

func TestReadPostRefusesInvalidAndDisallowedLinksBeforeLock(t *testing.T) {
	session := filepath.Join(t.TempDir(), "sessions", "study.json")
	env := map[string]string{
		"TG_HARVEST_STUDY_SESSION_PATH":  session,
		"TG_HARVEST_STUDY_ALLOWED_CHATS": "@study_news",
	}
	for _, tc := range []struct{ link, wantErr string }{
		{"https://t.me/hide/1?comment=27", "unsupported or repeated t.me query option"},
		{"https://t.me/different_news/10", "outside TG_HARVEST_STUDY_ALLOWED_CHATS"},
	} {
		code, stdout, stderr := runCommand(t, []string{"--profile", "study", "read-post", "--json", tc.link}, env)
		if code != 1 || stdout != "" || !strings.Contains(stderr, tc.wantErr) {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if _, err := os.Stat(session + ".runtime.lock"); !os.IsNotExist(err) {
			t.Fatalf("invalid or formally denied link acquired a runtime lock: %v", err)
		}
	}
}
