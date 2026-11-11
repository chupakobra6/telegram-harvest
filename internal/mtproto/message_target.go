package mtproto

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/gotd/td/tg"
)

func validateMessageSelector(selector string) error {
	if strings.HasPrefix(selector, "@") && directReplyUsername.MatchString(selector[1:]) {
		return nil
	}
	kind, number, ok := strings.Cut(selector, ":")
	id, err := strconv.ParseInt(number, 10, 64)
	if ok && err == nil && id > 0 && strconv.FormatInt(id, 10) == number &&
		(kind == "user" || kind == "basic_group" || kind == "supergroup") {
		return nil
	}
	return fmt.Errorf("destination must be @username, user:ID, basic_group:ID or supergroup:ID")
}

func validateMessageDestination(d MessageDestination, accountID int64, topicID int) error {
	if d.ID <= 0 || d.Title == "" || topicID < 0 || (topicID > 0) != (d.TopicTitle != "") {
		return fmt.Errorf("exact destination and topic identity required")
	}
	return validateMessageRoute(d.Type, d.ID, accountID, topicID)
}

func validateMessageRoute(kind string, id, accountID int64, topicID int) error {
	switch kind {
	case "user":
		if id == accountID {
			return fmt.Errorf("use main send-saved for Saved Messages")
		}
	case "basic_group", "supergroup":
		if kind == "supergroup" && id == PrintShopChannelID {
			return fmt.Errorf("use print-shop-publish for the fixed print-shop group")
		}
		if kind == "supergroup" && id == InterviewTopicChannelID && topicID == InterviewTopicID {
			return fmt.Errorf("use interview-topic-publish for the fixed interview topic")
		}
	default:
		return fmt.Errorf("destination must be an active human user, basic group or supergroup")
	}
	if topicID > 0 && kind != "supergroup" {
		return fmt.Errorf("a topic requires a forum supergroup")
	}
	return nil
}

func (s *Session) checkMessageAccount(ctx context.Context, account MessageAccount) (MessageAccount, error) {
	if err := validateMessageAccount(account); err != nil {
		return MessageAccount{}, err
	}
	self, err := s.SelfProfile(ctx)
	if err != nil {
		return MessageAccount{}, err
	}
	if self.ID != account.ID || readOnlyStudyIdentity(self.ID, self.Username) ||
		(account.Username != "" && !strings.EqualFold(self.Username, account.Username)) {
		return MessageAccount{}, fmt.Errorf("active Telegram account differs from selected writable identity")
	}
	return MessageAccount{Profile: account.Profile, ID: self.ID, Username: self.Username}, nil
}

func (s *Session) checkMessageTarget(ctx context.Context, m MessageManifest, account MessageAccount) (resolvedTarget, error) {
	if err := s.checkReviewedMessageAccount(ctx, m.Account, account); err != nil {
		return resolvedTarget{}, err
	}
	target, err := s.resolveTarget(ctx, m.Destination.Selector)
	if err != nil {
		return target, err
	}
	return s.reviewedMessageDestination(ctx, target, m)
}

func (s *Session) checkReviewedMessageAccount(ctx context.Context, reviewed, account MessageAccount) error {
	self, err := s.checkMessageAccount(ctx, account)
	if err != nil {
		return err
	}
	if self != reviewed {
		return fmt.Errorf("active Telegram account differs from the reviewed identity")
	}
	return nil
}

func (s *Session) refreshReviewedMessageTarget(ctx context.Context, target resolvedTarget, m MessageManifest, account MessageAccount) (resolvedTarget, error) {
	if err := s.checkReviewedMessageAccount(ctx, m.Account, account); err != nil {
		return target, err
	}
	return s.reviewedMessageDestination(ctx, target, m)
}

func (s *Session) reviewedMessageDestination(ctx context.Context, target resolvedTarget, m MessageManifest) (resolvedTarget, error) {
	target, destination, err := s.refreshMessageTarget(ctx, target, m.Destination.Selector, m.Account.ID, m.TopicID)
	if err != nil {
		return target, err
	}
	if destination != m.Destination {
		return target, fmt.Errorf("recipient or topic differs from the reviewed destination")
	}
	if m.ReplyTo > 0 {
		parentHash, err := s.messageParentHash(ctx, target, m.TopicID, m.ReplyTo)
		if err != nil {
			return target, err
		}
		if parentHash != m.ReplyToSHA256 {
			return target, fmt.Errorf("reply parent differs from the reviewed message")
		}
	}
	return target, nil
}

func (s *Session) refreshMessageTarget(ctx context.Context, target resolvedTarget, selector string, accountID int64, topicID int) (resolvedTarget, MessageDestination, error) {
	d := MessageDestination{Type: target.Chat.Type, ID: target.Chat.ID, Selector: selector}
	if err := validateMessageRoute(d.Type, d.ID, accountID, topicID); err != nil {
		return target, d, err
	}
	var err error
	if strings.HasPrefix(selector, "@") && d.Type != "user" {
		return target, d, fmt.Errorf("use an exact typed numeric destination for a group")
	}
	switch peer := target.InputPeer.(type) {
	case *tg.InputPeerUser:
		if d.Type != "user" || peer.UserID != d.ID {
			return target, d, fmt.Errorf("resolved user peer differs from destination")
		}
		var users []tg.UserClass
		err = s.performRPC(ctx, "get_message_recipient", func(ctx context.Context) error {
			var err error
			users, err = s.raw.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUser{UserID: peer.UserID, AccessHash: peer.AccessHash}})
			return err
		})
		if err == nil {
			for _, class := range users {
				if u, ok := class.(*tg.User); ok && u.ID == d.ID && !u.Bot && !u.Self && !u.Deleted {
					d.Username, _ = u.GetUsername()
					d.Title = strings.TrimSpace(strings.Join([]string{u.FirstName, u.LastName}, " "))
					if d.Title == "" {
						d.Title = usernameOrID(d.Username, d.ID)
					}
					break
				}
			}
		}
	case *tg.InputPeerChat:
		if d.Type != "basic_group" || peer.ChatID != d.ID {
			return target, d, fmt.Errorf("resolved basic group peer differs from destination")
		}
		var result tg.MessagesChatsClass
		err = s.performRPC(ctx, "get_message_basic_group", func(ctx context.Context) error {
			var err error
			result, err = s.raw.MessagesGetChats(ctx, []int64{peer.ChatID})
			return err
		})
		if err == nil {
			for _, class := range result.GetChats() {
				if chat, ok := class.(*tg.Chat); ok && chat.ID == d.ID && !chat.Left && !chat.Deactivated && chat.MigratedTo == nil {
					d.Title = chat.Title
					break
				}
			}
		}
	case *tg.InputPeerChannel:
		if d.Type != "supergroup" || peer.ChannelID != d.ID {
			return target, d, fmt.Errorf("resolved supergroup peer differs from destination")
		}
		var result tg.MessagesChatsClass
		err = s.performRPC(ctx, "get_message_supergroup", func(ctx context.Context) error {
			var err error
			result, err = s.raw.ChannelsGetChannels(ctx, []tg.InputChannelClass{&tg.InputChannel{ChannelID: peer.ChannelID, AccessHash: peer.AccessHash}})
			return err
		})
		if err == nil {
			for _, class := range result.GetChats() {
				if chat, ok := class.(*tg.Channel); ok && chat.ID == d.ID && chat.Megagroup && !chat.Broadcast && !chat.Left && !chat.Min && !chat.Monoforum {
					d.Title = chat.Title
					d.Username, _ = chat.GetUsername()
					if chat.Forum != (topicID > 0) {
						return target, d, fmt.Errorf("forum destinations require an explicit topic; ordinary groups accept no topic")
					}
					target.Chat.Forum = chat.Forum
					break
				}
			}
		}
	case *tg.InputPeerSelf:
		return target, d, fmt.Errorf("use main send-saved for Saved Messages")
	default:
		return target, d, fmt.Errorf("unsupported Telegram destination peer")
	}
	if err != nil {
		return target, d, err
	}
	if d.Title == "" {
		return target, d, fmt.Errorf("active human recipient or group membership could not be verified")
	}
	if topicID > 0 && target.Chat.Forum {
		topic, err := s.getDeliveryTopic(ctx, target, topicID)
		if err != nil {
			return target, d, err
		}
		d.TopicTitle = topic.Title
	}
	return target, d, validateMessageDestination(d, accountID, topicID)
}

func (s *Session) getDeliveryTopic(ctx context.Context, target resolvedTarget, topicID int) (*tg.ForumTopic, error) {
	var result *tg.MessagesForumTopics
	err := s.performRPC(ctx, "get_message_topic", func(ctx context.Context) error {
		var err error
		result, err = s.raw.MessagesGetForumTopicsByID(ctx, &tg.MessagesGetForumTopicsByIDRequest{Peer: target.InputPeer, Topics: []int{topicID}})
		return err
	})
	if err != nil {
		return nil, err
	}
	for _, candidate := range result.Topics {
		if t, ok := candidate.(*tg.ForumTopic); ok && t.ID == topicID && !t.Closed && t.Title != "" &&
			(t.Peer == nil || matchesDeliveryPeer(t.Peer, target.Chat)) {
			return t, nil
		}
	}
	return nil, fmt.Errorf("open topic %d could not be verified", topicID)
}

func (s *Session) messageParentHash(ctx context.Context, target resolvedTarget, topicID, id int) (string, error) {
	raw, _, err := s.fetchMessageByIDDirect(ctx, target, id)
	if err != nil {
		return "", err
	}
	m, ok := raw.(*tg.Message)
	if !ok || m.ID != id || !matchesDeliveryPeer(m.PeerID, target.Chat) || deliveryMessageTopic(m.ReplyTo, target.Chat.Forum) != topicID {
		return "", fmt.Errorf("reply parent does not belong to the exact destination and topic")
	}
	text := m.Message
	if _, rich := m.GetRichMessage(); rich {
		if err := s.prepareRichMessage(ctx, target, m); err != nil {
			return "", err
		}
		full, _ := m.GetRichMessage()
		text, _, err = richMessageBody(m.Message, full)
		if err != nil {
			return "", err
		}
	}
	// Bind the full exact text and edit identity, including media-only replies.
	evidence, err := json.Marshal(struct {
		Text     string
		EditDate int
		FromID   tg.PeerClass
		Media    string
	}{text, m.EditDate, m.FromID, deliveryParentMedia(m.Media)})
	if err != nil {
		return "", err
	}
	return topicTextHash(string(evidence)), nil
}

func deliveryParentMedia(media tg.MessageMediaClass) string {
	switch value := media.(type) {
	case nil:
		return ""
	case *tg.MessageMediaDocument:
		if d, ok := value.Document.(*tg.Document); ok {
			return fmt.Sprintf("document:%d", d.ID)
		}
	case *tg.MessageMediaPhoto:
		if p, ok := value.Photo.(*tg.Photo); ok {
			return fmt.Sprintf("photo:%d", p.ID)
		}
	}
	return media.TypeName()
}

func matchesDeliveryPeer(peer tg.PeerClass, chat harvest.Chat) bool {
	switch peer := peer.(type) {
	case *tg.PeerUser:
		return chat.Type == "user" && peer.UserID == chat.ID
	case *tg.PeerChat:
		return chat.Type == "basic_group" && peer.ChatID == chat.ID
	case *tg.PeerChannel:
		return chat.Type == "supergroup" && peer.ChannelID == chat.ID
	default:
		return false
	}
}

func deliveryInputReply(topicID, replyTo int) tg.InputReplyToClass {
	if replyTo == 0 {
		if topicID <= 1 {
			return nil
		}
		return &tg.InputReplyToMessage{ReplyToMsgID: topicID}
	}
	reply := &tg.InputReplyToMessage{ReplyToMsgID: replyTo}
	if topicID > 1 && topicID != replyTo {
		reply.SetTopMsgID(topicID)
	}
	return reply
}

func deliveryMessageTopic(reply tg.MessageReplyHeaderClass, forum bool) int {
	if !forum {
		return 0
	}
	header, ok := reply.(*tg.MessageReplyHeader)
	if !ok || !header.ForumTopic {
		return 1
	}
	replyID, topID := replyInfo(header)
	if topID > 0 {
		return topID
	}
	return replyID
}

func matchesDeliveryReply(raw tg.MessageReplyHeaderClass, chat harvest.Chat, topicID, replyTo int) bool {
	if deliveryMessageTopic(raw, chat.Forum) != topicID {
		return false
	}
	if replyTo == 0 && topicID > 1 {
		replyTo = topicID
	}
	if replyTo == 0 {
		return raw == nil
	}
	reply, ok := raw.(*tg.MessageReplyHeader)
	return ok && reply.ReplyToMsgID == replyTo && (reply.ReplyToPeerID == nil || matchesDeliveryPeer(reply.ReplyToPeerID, chat))
}
