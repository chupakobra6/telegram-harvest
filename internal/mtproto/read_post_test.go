package mtproto

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestParsePostLink(t *testing.T) {
	for _, tc := range []struct {
		input, selector, canonical string
	}{
		{"https://t.me/Example_News/42", "@example_news", "https://t.me/example_news/42"},
		{"t.me/example_news/42?thread=5&single", "@example_news", "https://t.me/example_news/5/42?single"},
		{"https://t.me/c/1234567890/5/42", "1234567890", "https://t.me/c/1234567890/5/42"},
		{"https://t.me/c/1234567890/42?single", "1234567890", "https://t.me/c/1234567890/42?single"},
	} {
		link, err := ParsePostLink(tc.input)
		if err != nil || link.ChatSelector() != tc.selector || link.sourceURL() != tc.canonical {
			t.Fatalf("%q: %+v, %v", tc.input, link, err)
		}
	}
	for _, raw := range []string{
		"t.me/+invite/42", "t.me/joinchat/42", "t.me/m/42", "tg://resolve?domain=example_news&post=42",
		"https://not-t.me/example_news/42", "https://t.me.example.org/example_news/42", "https://t.me:443/example_news/42",
		"https://user:private@example.org/example_news/42", "https://t.me/example_news/42#fragment", "t.me/example_news/42?token=private",
		"t.me/example_news/42?comment=23", "t.me/example_news/42?single=other", "t.me/example_news/42?thread=5&thread=6",
		"t.me/example_news/5/42?thread=5", "t.me/example_news/%34%32", "t.me/example_news/42/", "t.me/example_news/2147483648",
		"t.me/c/0/42", "t.me/c/-1001234567890/42", "t.me/c/1234567890/", "t.me/example_news",
	} {
		if _, err := ParsePostLink(raw); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatalf("unsafe parse of %q: %v", raw, err)
		}
	}
}

type readPostWire struct {
	t        *testing.T
	msg      tg.Message
	channel  tg.Channel
	username string
	richPeer tg.PeerClass
	calls    []string
}

func (f *readPostWire) Invoke(_ context.Context, input bin.Encoder, output bin.Decoder) error {
	switch request := input.(type) {
	case *tg.ContactsResolveUsernameRequest:
		f.calls = append(f.calls, "resolve")
		if request.Username != f.username {
			f.t.Fatalf("unexpected username: %s", request.Username)
		}
		*output.(*tg.ContactsResolvedPeer) = tg.ContactsResolvedPeer{
			Peer: &tg.PeerChannel{ChannelID: f.channel.ID}, Chats: []tg.ChatClass{&f.channel},
		}
	case *tg.MessagesGetDialogsRequest:
		f.calls = append(f.calls, "dialogs")
		if id, _ := request.GetFolderID(); id != 0 || request.OffsetID != 0 {
			f.t.Fatalf("unnecessary dialog page: %+v", request)
		}
		output.(*tg.MessagesDialogsBox).Dialogs = &tg.MessagesDialogs{
			Dialogs: []tg.DialogClass{
				&tg.Dialog{Peer: &tg.PeerChat{ChatID: f.channel.ID}},
				&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: f.channel.ID}, TopMessage: 99},
			},
			Chats: []tg.ChatClass{&tg.Chat{ID: f.channel.ID, Title: "wrong peer"}, &f.channel},
		}
	case *tg.ChannelsGetMessagesRequest:
		f.calls = append(f.calls, "one message")
		channel, ok := request.Channel.(*tg.InputChannel)
		if !ok || channel.ChannelID != f.channel.ID || channel.AccessHash != f.channel.AccessHash ||
			!reflect.DeepEqual(request.ID, []tg.InputMessageClass{&tg.InputMessageID{ID: 42}}) {
			f.t.Fatalf("unexpected message lookup: %+v", request)
		}
		output.(*tg.MessagesMessagesBox).Messages = &tg.MessagesChannelMessages{Messages: []tg.MessageClass{&f.msg}}
	case *tg.MessagesGetRichMessageRequest:
		f.calls = append(f.calls, "one rich message")
		if channel, ok := request.Peer.(*tg.InputPeerChannel); !ok || channel.ChannelID != f.channel.ID || request.ID != 42 {
			f.t.Fatalf("unexpected rich lookup: %+v", request)
		}
		rich := &tg.Message{ID: 42, PeerID: f.richPeer}
		rich.SetRichMessage(tg.RichMessage{Blocks: []tg.PageBlockClass{
			&tg.PageBlockParagraph{Text: &tg.TextPlain{Text: "Rich body"}},
		}})
		output.(*tg.MessagesMessagesBox).Messages = &tg.MessagesChannelMessages{Messages: []tg.MessageClass{rich}}
	default:
		f.t.Fatalf("read-post sent an unexpected RPC: %T", request)
	}
	return nil
}

func TestReadPostExactMessage(t *testing.T) {
	for _, tc := range []struct {
		name, url, wantErr      string
		channelID               int64
		messageID, threadID     int
		denied, rich, wrongRich bool
		wantCalls               []string
	}{
		{name: "public", url: "t.me/example_news/42", channelID: 100, messageID: 42, wantCalls: []string{"resolve", "one message"}},
		{name: "private topic", url: "t.me/c/100/5/42", channelID: 100, messageID: 42, threadID: 5, wantCalls: []string{"dialogs", "one message"}},
		{name: "general forum topic", url: "t.me/c/100/1/42", channelID: 100, messageID: 42, wantCalls: []string{"dialogs", "one message"}},
		{name: "wrong id", url: "t.me/example_news/42", channelID: 100, messageID: 43, wantErr: "no message 42", wantCalls: []string{"resolve", "one message"}},
		{name: "wrong peer", url: "t.me/example_news/42", channelID: 110, messageID: 42, wantErr: "outside the requested chat", wantCalls: []string{"resolve", "one message"}},
		{name: "wrong topic", url: "t.me/c/100/5/42", channelID: 100, messageID: 42, threadID: 6, wantErr: "does not match requested thread", wantCalls: []string{"dialogs", "one message"}},
		{name: "denied peer", url: "t.me/example_news/42", denied: true, wantErr: "blocked by policy", wantCalls: []string{"resolve"}},
		{name: "partial rich post", url: "t.me/example_news/42", channelID: 100, messageID: 42, rich: true, wantCalls: []string{"resolve", "one message", "one rich message"}},
		{name: "wrong rich peer", url: "t.me/example_news/42", channelID: 100, messageID: 42, rich: true, wrongRich: true, wantErr: "remains partial", wantCalls: []string{"resolve", "one message", "one rich message"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := tg.Message{ID: tc.messageID, PeerID: &tg.PeerChannel{ChannelID: tc.channelID}, Date: 1700000000,
				Message: "Caption", Media: &tg.MessageMediaPhoto{}}
			msg.SetGroupedID(123)
			if tc.threadID > 0 {
				reply := &tg.MessageReplyHeader{ForumTopic: true}
				reply.SetReplyToTopID(tc.threadID)
				msg.SetReplyTo(reply)
			}
			richPeer := msg.PeerID
			if tc.rich {
				msg.SetRichMessage(tg.RichMessage{Part: true})
				if tc.wrongRich {
					richPeer = &tg.PeerChannel{ChannelID: 110}
				}
			}
			forum := strings.Contains(tc.url, "/c/")
			wire := &readPostWire{t: t, msg: msg, channel: tg.Channel{ID: 100, Title: "News", Megagroup: forum, Forum: forum},
				username: "example_news", richPeer: richPeer}
			wire.channel.SetAccessHash(456)
			wire.channel.SetUsername(wire.username)
			s := &Session{raw: tg.NewClient(wire), dialogCache: make(map[string]resolvedTarget)}
			link, err := ParsePostLink(tc.url)
			if err != nil {
				t.Fatal(err)
			}
			wantChatType := "channel"
			if forum {
				wantChatType = "supergroup"
			}
			result, err := s.ReadPost(context.Background(), link, func(chat harvest.Chat) error {
				if chat.ID != 100 || chat.Type != wantChatType || chat.Username != "example_news" {
					t.Fatalf("policy check saw wrong peer: %+v", chat)
				}
				if tc.denied {
					return fmt.Errorf("blocked by policy")
				}
				return nil
			})
			if !reflect.DeepEqual(wire.calls, tc.wantCalls) {
				t.Fatalf("RPCs = %v, wanted %v", wire.calls, tc.wantCalls)
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || result.Record.MessageID != 0 {
					t.Fatalf("unexpected result: %+v, %v", result, err)
				}
				return
			}
			wantText := "Caption"
			if tc.rich {
				wantText += "\n\nRich body"
			}
			if err != nil || result.Record.Text != wantText || !result.GroupedMedia || result.MediaDownloaded || result.Scope != "message" ||
				result.Record.ThreadTopMessageID != link.threadID || result.Record.Chat.TopMessageID != 0 ||
				result.Record.SourceURL != link.sourceURL() || len(result.Record.Attachments) != 1 || result.Record.Attachments[0].Kind != "photo" {
				t.Fatalf("unexpected result: %+v, %v", result, err)
			}
		})
	}
}

func TestReadPostRedactsPrivateURLs(t *testing.T) {
	const public = "https://example.org/source?p=5"
	const signed = "https://example.org/object?X-Amz-Signature=S3-secret"
	const credential = "https://user:password@example.org/file"
	const jwt = "https://example.org/file#access_token=private"
	const malformed = "https://example.org/%?sig=hidden"
	record := harvest.MessageRecord{Text: "Keep " + public + " but redact " + signed + " and " + malformed, Links: []string{public, credential, jwt},
		Attachments: []harvest.Attachment{{Kind: "webpage", URL: signed, Title: signed}}}
	redactPostURLs(&record)
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"S3-secret", "user:password", "access_token", "sig=hidden"} {
		if strings.Contains(string(data), private) {
			t.Fatalf("private credential remains in output: %s", data)
		}
	}
	if !strings.Contains(record.Text, public) || record.Links[0] != public {
		t.Fatalf("ordinary links were modified: %+v", record)
	}
}
