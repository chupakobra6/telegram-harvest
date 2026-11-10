package mtproto

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/gotd/td/tg"
)

type DirectReplyManifest struct {
	PeerID            int64  `json:"peer_id"`
	Username          string `json:"username"`
	ReplyTo           int    `json:"reply_to"`
	ReplyToTextSHA256 string `json:"reply_to_text_sha256"`
	Text              string `json:"text"`
}

type DirectReplyResult struct {
	DryRun         bool   `json:"dry_run"`
	AccountID      int64  `json:"account_id"`
	PeerID         int64  `json:"peer_id"`
	Username       string `json:"username"`
	Recipient      string `json:"recipient"`
	ReplyTo        int    `json:"reply_to"`
	IncomingText   string `json:"incoming_text"`
	Text           string `json:"text"`
	MessageID      int    `json:"message_id,omitempty"`
	Status         string `json:"status"`
	ServerVerified bool   `json:"server_verified"`
}

var directReplyUsername = regexp.MustCompile(`^[a-zA-Z0-9_]{1,32}$`)

func ValidateDirectReplyManifest(m DirectReplyManifest) error {
	if m.PeerID <= 0 || m.PeerID == InterviewTopicAccountID || !directReplyUsername.MatchString(m.Username) {
		return fmt.Errorf("an exact other user ID and username without @ are required")
	}
	if m.ReplyTo <= 0 || !validSHA256(m.ReplyToTextSHA256) {
		return fmt.Errorf("a positive reply_to and exact SHA-256 of incoming text are required")
	}
	if !utf8.ValidString(m.Text) || strings.TrimSpace(m.Text) == "" || len(utf16.Encode([]rune(m.Text))) > 4096 {
		return fmt.Errorf("provide nonblank UTF-8 reply text of at most 4096 UTF-16 units")
	}
	return nil
}

// SendDirectReply sends one authorized plain-text reply in an exact private dialog.
// The caller holds the account runtime lock across receipt access and Telegram RPCs.
func (s *Session) SendDirectReply(ctx context.Context, m DirectReplyManifest, journalPath string, apply bool) (DirectReplyResult, error) {
	r := DirectReplyResult{DryRun: !apply, PeerID: m.PeerID, ReplyTo: m.ReplyTo, Text: m.Text}
	if err := ValidateDirectReplyManifest(m); err != nil {
		return r, err
	}
	journal, err := loadPublicationJournal(journalPath, 0)
	if err != nil {
		return r, err
	}
	self, err := s.SelfProfile(ctx)
	if err != nil {
		return r, err
	}
	if self.ID != InterviewTopicAccountID || !strings.EqualFold(self.Username, SavedMessagesMainUsername) {
		return r, fmt.Errorf("active account differs from main/@Pheik13")
	}
	r.AccountID = self.ID
	target, err := s.resolveTarget(ctx, "@"+m.Username)
	if err != nil {
		return r, err
	}
	userPeer, ok := target.InputPeer.(*tg.InputPeerUser)
	if !ok || target.Chat.Type != "user" || target.Chat.ID != m.PeerID || userPeer.UserID != m.PeerID ||
		!strings.EqualFold(target.Chat.Username, m.Username) {
		return r, fmt.Errorf("resolved user does not match the reviewed recipient")
	}
	r.Recipient, r.Username = target.Chat.Display, target.Chat.Username
	parent, entities, err := s.fetchMessageByID(ctx, target, m.ReplyTo)
	if err != nil {
		return r, err
	}
	incoming, ok := parent.(*tg.Message)
	user, haveUser := entities.User(m.PeerID)
	if !haveUser || user.Bot || user.Deleted || user.Self ||
		!ok || incoming.ID != m.ReplyTo || incoming.Out || !directReplyPeerIs(incoming.PeerID, m.PeerID) ||
		incoming.FromID != nil && !directReplyPeerIs(incoming.FromID, m.PeerID) ||
		strings.TrimSpace(incoming.Message) == "" || topicTextHash(incoming.Message) != m.ReplyToTextSHA256 {
		return r, fmt.Errorf("incoming message does not match the reviewed private reply target")
	}
	r.IncomingText = incoming.Message

	key := fmt.Sprintf("%d/%d", m.PeerID, m.ReplyTo)
	payload, err := json.Marshal(m)
	if err != nil {
		return r, err
	}
	hash := topicTextHash(string(payload))
	if receipt, exists := journal[key]; exists {
		r.MessageID = receipt.ID
		if receipt.Hash != hash || receipt.ID <= m.ReplyTo {
			r.Status = "reconcile_required"
			return r, fmt.Errorf("existing receipt for this reply target requires reconciliation")
		}
		if err := s.verifyDirectReply(ctx, target, m, receipt.ID); err != nil {
			r.Status = "reconcile_required"
			return r, err
		}
		r.Status, r.ServerVerified = "already_sent", true
		if apply && receipt.Pending {
			receipt.Pending = false
			journal[key] = receipt
			return r, savePublishJournal(journalPath, journal)
		}
		return r, nil
	}
	r.Status = "ready"
	if !apply {
		return r, nil
	}

	if err := s.beforeRPC(ctx, "send_direct_reply"); err != nil {
		return r, err
	}
	journal[key] = topicPublishReceipt{Hash: hash, Pending: true}
	if err := savePublishJournal(journalPath, journal); err != nil {
		return r, err
	}
	r.Status = "outcome_unknown"
	randomID := directReplyRandomID(key)
	callCtx, cancel := context.WithTimeout(ctx, defaultRPCTimeout)
	updates, sendErr := s.raw.MessagesSendMessage(callCtx, &tg.MessagesSendMessageRequest{
		Peer:      userPeer,
		ReplyTo:   &tg.InputReplyToMessage{ReplyToMsgID: m.ReplyTo},
		Message:   m.Text,
		RandomID:  randomID,
		NoWebpage: true,
	})
	cancel()
	if sendErr != nil {
		return r, fmt.Errorf("send outcome unknown; reconcile before retry: %w", sendErr)
	}
	id := directReplySentID(updates, randomID, m)
	r.MessageID = id
	if id <= m.ReplyTo {
		return r, fmt.Errorf("send returned no usable message ID; reconcile before retry")
	}
	journal[key] = topicPublishReceipt{Hash: hash, ID: id, Pending: true}
	if err := savePublishJournal(journalPath, journal); err != nil {
		return r, err
	}
	if err := s.verifyDirectReply(ctx, target, m, id); err != nil {
		return r, fmt.Errorf("sent message readback unverified: %w", err)
	}
	r.Status, r.ServerVerified = "sent", true
	journal[key] = topicPublishReceipt{Hash: hash, ID: id}
	return r, savePublishJournal(journalPath, journal)
}

func directReplyPeerIs(peer tg.PeerClass, userID int64) bool {
	user, ok := peer.(*tg.PeerUser)
	return ok && user.UserID == userID
}

func directReplyRandomID(key string) int64 {
	hash := sha256.Sum256([]byte("direct-reply/main/" + key))
	return int64(binary.BigEndian.Uint64(hash[:8]) | 1)
}

func directReplySentID(updates tg.UpdatesClass, randomID int64, m DirectReplyManifest) int {
	var items []tg.UpdateClass
	switch result := updates.(type) {
	case *tg.UpdateShortSentMessage:
		return result.ID
	case *tg.UpdateShort:
		items = []tg.UpdateClass{result.Update}
	case *tg.Updates:
		items = result.Updates
	case *tg.UpdatesCombined:
		items = result.Updates
	}
	var mappedID, exactID int
	ambiguous := false
	for _, item := range items {
		switch update := item.(type) {
		case *tg.UpdateMessageID:
			if update.RandomID == randomID {
				if mappedID != 0 && mappedID != update.ID {
					return 0
				}
				mappedID = update.ID
			}
		case *tg.UpdateNewMessage:
			if msg, ok := update.Message.(*tg.Message); ok && verifyDirectReplyMessage(msg, m) == nil {
				if exactID != 0 && exactID != msg.ID {
					ambiguous = true
				}
				exactID = msg.ID
			}
		}
	}
	if mappedID != 0 {
		return mappedID
	}
	if ambiguous {
		return 0
	}
	return exactID
}

func (s *Session) verifyDirectReply(ctx context.Context, target resolvedTarget, m DirectReplyManifest, id int) error {
	message, _, err := s.fetchMessageByID(ctx, target, id)
	if err != nil {
		return err
	}
	out, ok := message.(*tg.Message)
	if !ok || out.ID != id {
		return fmt.Errorf("server returned a different message")
	}
	return verifyDirectReplyMessage(out, m)
}

func verifyDirectReplyMessage(out *tg.Message, m DirectReplyManifest) error {
	_, emptyMedia := out.Media.(*tg.MessageMediaEmpty)
	if !out.Out || !directReplyPeerIs(out.PeerID, m.PeerID) ||
		out.FromID != nil && !directReplyPeerIs(out.FromID, InterviewTopicAccountID) ||
		out.Message != m.Text || out.Media != nil && !emptyMedia {
		return fmt.Errorf("server reply identity or content differs")
	}
	reply, ok := out.ReplyTo.(*tg.MessageReplyHeader)
	if !ok || reply.ReplyToMsgID != m.ReplyTo ||
		reply.ReplyToPeerID != nil && !directReplyPeerIs(reply.ReplyToPeerID, m.PeerID) {
		return fmt.Errorf("server reply targets a different message")
	}
	return nil
}
