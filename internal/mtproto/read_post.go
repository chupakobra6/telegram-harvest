package mtproto

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/gotd/td/tg"
)

var postUsernamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
var postIDPattern = regexp.MustCompile(`^[1-9][0-9]*$`)

type PostLink struct {
	username  string
	channelID int64
	messageID int
	threadID  int
	single    bool
}

func ParsePostLink(raw string) (PostLink, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(raw), "t.me/") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Host, "t.me") || (u.Scheme != "https" && u.Scheme != "http") ||
		u.User != nil || u.RawPath != "" || !strings.HasPrefix(u.Path, "/") || strings.Contains(raw, "#") {
		return PostLink{}, fmt.Errorf("expected a t.me message URL without a fragment or embedded credentials")
	}
	path := strings.Split(u.Path[1:], "/")
	link := PostLink{}
	if path[0] == "c" && len(path) >= 2 {
		link.channelID, err = parsePostID(path[1], 64)
		if err != nil {
			return PostLink{}, fmt.Errorf("invalid channel id in /c message link")
		}
		path = path[2:]
	} else {
		link.username = strings.ToLower(path[0])
		if !postUsernamePattern.MatchString(link.username) || reservedPostUsername(link.username) {
			return PostLink{}, fmt.Errorf("expected a channel or supergroup message link")
		}
		path = path[1:]
	}
	if len(path) < 1 || len(path) > 2 {
		return PostLink{}, fmt.Errorf("expected a message id or thread id and message id in t.me URL")
	}
	messageID, err := parsePostID(path[len(path)-1], 32)
	if err != nil {
		return PostLink{}, fmt.Errorf("invalid message id in t.me URL")
	}
	link.messageID = int(messageID)
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return PostLink{}, fmt.Errorf("invalid query in t.me message URL")
	}
	for key, values := range query {
		if len(values) != 1 || (key != "thread" && (key != "single" || values[0] != "")) {
			return PostLink{}, fmt.Errorf("unsupported or repeated t.me query option; only thread and single are supported")
		}
	}
	_, link.single = query["single"]
	if len(path) == 2 || query.Has("thread") {
		if len(path) == 2 && query.Has("thread") {
			return PostLink{}, fmt.Errorf("thread id must appear once, in the path or query")
		}
		thread := query.Get("thread")
		if len(path) == 2 {
			thread = path[0]
		}
		threadID, err := parsePostID(thread, 32)
		if err != nil {
			return PostLink{}, fmt.Errorf("invalid thread id in t.me URL")
		}
		link.threadID = int(threadID)
	}
	return link, nil
}

func reservedPostUsername(username string) bool {
	switch username {
	case "addemoji", "addlist", "addstickers", "addstyle", "addtheme", "auction", "auth", "boost", "c", "call", "confirmphone", "contact", "giftcode", "invoice", "joinchat", "login", "m", "msg", "nft", "proxy", "s", "setlanguage", "share", "socks", "web", "a", "k", "z":
		return true
	default:
		return false
	}
}

func parsePostID(value string, bits int) (int64, error) {
	if !postIDPattern.MatchString(value) {
		return 0, fmt.Errorf("expected a positive decimal id")
	}
	return strconv.ParseInt(value, 10, bits)
}

func (p PostLink) ChatSelector() string {
	if p.channelID != 0 {
		return strconv.FormatInt(p.channelID, 10)
	}
	return "@" + p.username
}

func (p PostLink) sourceURL() string {
	path := p.username
	if p.channelID != 0 {
		path = "c/" + strconv.FormatInt(p.channelID, 10)
	}
	if p.threadID != 0 {
		path += "/" + strconv.Itoa(p.threadID)
	}
	result := "https://t.me/" + path + "/" + strconv.Itoa(p.messageID)
	if p.single {
		result += "?single"
	}
	return result
}

type ReadPostResult struct {
	Record          harvest.MessageRecord `json:"record"`
	Scope           string                `json:"scope"`
	GroupedMedia    bool                  `json:"grouped_media"`
	MediaDownloaded bool                  `json:"media_downloaded"`
}

// ReadPost retrieves one Telegram message. It never falls back to history.
func (s *Session) ReadPost(ctx context.Context, link PostLink, allowChat func(harvest.Chat) error) (ReadPostResult, error) {
	if link.messageID <= 0 {
		return ReadPostResult{}, fmt.Errorf("valid Telegram message link is required")
	}
	target, err := s.resolvePostTarget(ctx, link)
	if err != nil {
		return ReadPostResult{}, err
	}
	if err := allowChat(target.Chat); err != nil {
		return ReadPostResult{}, err
	}
	raw, entities, err := s.fetchMessageByIDDirect(ctx, target, link.messageID)
	if err != nil {
		return ReadPostResult{}, fmt.Errorf("read message %d: %w", link.messageID, err)
	}
	msg, ok := raw.(*tg.Message)
	if !ok || msg.ID != link.messageID || !matchesPostPeer(msg.PeerID, target.Chat.ID) {
		return ReadPostResult{}, fmt.Errorf("received a Telegram message outside the requested chat and id")
	}
	threadID := readPostThread(msg, target.Chat)
	if link.threadID != 0 && link.threadID != threadID {
		return ReadPostResult{}, fmt.Errorf("message %d does not match requested thread or topic %d", link.messageID, link.threadID)
	}
	if err := s.prepareRichMessage(ctx, target, msg); err != nil {
		return ReadPostResult{}, err
	}
	// Dialog discovery for /c links happens before this read. Omit the dialog head and unread state.
	chat := harvest.Chat{ID: target.Chat.ID, Type: target.Chat.Type, Username: target.Chat.Username,
		Title: target.Chat.Title, Display: target.Chat.Display, Forum: target.Chat.Forum}
	record, ok := normalizeRecord(msg, chat, entities)
	if !ok {
		return ReadPostResult{}, fmt.Errorf("could not normalize Telegram message %d", link.messageID)
	}
	record.SourceURL = link.sourceURL()
	record.ThreadTopMessageID = threadID
	redactPostURLs(&record)
	return ReadPostResult{Record: record, Scope: "message", GroupedMedia: msg.GroupedID != 0}, nil
}

func (s *Session) resolvePostTarget(ctx context.Context, link PostLink) (resolvedTarget, error) {
	var target resolvedTarget
	if link.channelID == 0 {
		var err error
		target, err = s.resolveTarget(ctx, link.ChatSelector())
		if err != nil {
			return resolvedTarget{}, err
		}
	} else {
		lookup := func() resolvedTarget {
			for _, kind := range []string{"channel", "supergroup"} {
				if cached, ok := s.dialogCache[kind+":"+link.ChatSelector()]; ok {
					return cached
				}
			}
			return resolvedTarget{}
		}
		target = lookup()
		for _, folder := range []int{0, 1} {
			if target.InputPeer != nil {
				break
			}
			if _, err := s.loadDialogsFolderUntil(ctx, folder, func(chat harvest.Chat) bool {
				return chat.ID == link.channelID && (chat.Type == "channel" || chat.Type == "supergroup")
			}); err != nil {
				return resolvedTarget{}, err
			}
			target = lookup()
		}
		if target.Chat.ID != link.channelID {
			return resolvedTarget{}, fmt.Errorf("channel %d from /c link is unavailable in this account's dialogs", link.channelID)
		}
	}
	input, ok := target.InputPeer.(*tg.InputPeerChannel)
	if !ok || input.ChannelID != target.Chat.ID || (target.Chat.Type != "channel" && target.Chat.Type != "supergroup") {
		return resolvedTarget{}, fmt.Errorf("message links must address a channel or supergroup in this account")
	}
	return target, nil
}

func matchesPostPeer(raw tg.PeerClass, channelID int64) bool {
	peer, ok := raw.(*tg.PeerChannel)
	return ok && peer.ChannelID == channelID
}

func readPostThread(msg *tg.Message, chat harvest.Chat) int {
	reply, hasReply := msg.ReplyTo.(*tg.MessageReplyHeader)
	if hasReply {
		replyID, topID := replyInfo(reply)
		if topID > 0 {
			return topID
		}
		if reply.ForumTopic {
			if reply.ReplyToPeerID == nil || matchesPostPeer(reply.ReplyToPeerID, chat.ID) {
				return replyID
			}
			return 0
		}
	}
	if chat.Forum {
		return 1 // General topic has no explicit top id.
	}
	return 0
}

func redactPostURLs(record *harvest.MessageRecord) {
	record.Text = linkPattern.ReplaceAllStringFunc(record.Text, redactPrivatePostURL)
	record.Chat.Title = linkPattern.ReplaceAllStringFunc(record.Chat.Title, redactPrivatePostURL)
	record.Chat.Display = linkPattern.ReplaceAllStringFunc(record.Chat.Display, redactPrivatePostURL)
	record.Sender.Display = linkPattern.ReplaceAllStringFunc(record.Sender.Display, redactPrivatePostURL)
	if record.Forward != nil {
		record.Forward.PostAuthor = linkPattern.ReplaceAllStringFunc(record.Forward.PostAuthor, redactPrivatePostURL)
		record.Forward.OriginName = linkPattern.ReplaceAllStringFunc(record.Forward.OriginName, redactPrivatePostURL)
		if record.Forward.Origin != nil {
			record.Forward.Origin.Display = linkPattern.ReplaceAllStringFunc(record.Forward.Origin.Display, redactPrivatePostURL)
		}
	}
	for i, link := range record.Links {
		record.Links[i] = redactPrivatePostURL(link)
	}
	for i := range record.Attachments {
		attachment := &record.Attachments[i]
		attachment.URL = redactPrivatePostURL(attachment.URL)
		attachment.Title = linkPattern.ReplaceAllStringFunc(attachment.Title, redactPrivatePostURL)
		attachment.FileName = linkPattern.ReplaceAllStringFunc(attachment.FileName, redactPrivatePostURL)
	}
}

func redactPrivatePostURL(raw string) string {
	base, fragment, _ := strings.Cut(raw, "#")
	authorityPath, query, _ := strings.Cut(base, "?")
	_, authorityPath, _ = strings.Cut(authorityPath, "://")
	authority, _, _ := strings.Cut(authorityPath, "/")
	if strings.Contains(authority, "@") || postURLHasCredentials(query) || postURLHasCredentials(fragment) {
		return "[private URL redacted]"
	}
	return raw
}

func postURLHasCredentials(query string) bool {
	for _, parameter := range strings.FieldsFunc(query, func(r rune) bool { return r == '&' || r == ';' }) {
		key, _, _ := strings.Cut(parameter, "=")
		key, err := url.QueryUnescape(key)
		if err != nil {
			continue
		}
		switch strings.ToLower(key) {
		case "x-amz-signature", "x-amz-credential", "x-amz-security-token", "x-goog-signature", "x-goog-credential", "x-oss-signature", "ossaccesskeyid", "signature", "sig", "hmac", "token", "access_token", "id_token", "refresh_token", "auth", "authkey", "api_key", "apikey", "password", "secret", "key-pair-id", "hdnts", "hdnea", "__gda__":
			return true
		}
	}
	return false
}
