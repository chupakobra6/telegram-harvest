package mtproto

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/gotd/td/tg"
)

type DirectReplyManifest struct {
	PeerID            int64            `json:"peer_id"`
	Username          string           `json:"username"`
	ReplyTo           int              `json:"reply_to"`
	ReplyToTextSHA256 string           `json:"reply_to_text_sha256"`
	Text              string           `json:"text"`
	File              *DirectReplyFile `json:"file,omitempty"`
}

type DirectReplyFile struct {
	Path     string `json:"path"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
	MIME     string `json:"mime"`
}

func (f *DirectReplyFile) publicationFile() *InterviewTopicFile {
	return &InterviewTopicFile{Path: f.Path, Filename: f.Filename, Size: f.Size, SHA256: f.SHA256, MIME: f.MIME, Kind: "document"}
}

type DirectReplyResult struct {
	DryRun         bool             `json:"dry_run"`
	AccountID      int64            `json:"account_id"`
	PeerID         int64            `json:"peer_id"`
	Username       string           `json:"username"`
	Recipient      string           `json:"recipient"`
	ReplyTo        int              `json:"reply_to"`
	IncomingText   string           `json:"incoming_text"`
	Text           string           `json:"text"`
	File           *DirectReplyFile `json:"file,omitempty"`
	MessageID      int              `json:"message_id,omitempty"`
	Status         string           `json:"status"`
	ServerVerified bool             `json:"server_verified"`
}

var directReplyUsername = regexp.MustCompile(`^[a-zA-Z0-9_]{1,32}$`)

func ValidateDirectReplyManifest(m DirectReplyManifest) error {
	if m.PeerID <= 0 || m.PeerID == InterviewTopicAccountID || !directReplyUsername.MatchString(m.Username) {
		return fmt.Errorf("an exact other user ID and username without @ are required")
	}
	if m.ReplyTo <= 0 || !validSHA256(m.ReplyToTextSHA256) {
		return fmt.Errorf("a positive reply_to and exact SHA-256 of incoming text are required")
	}
	if m.File != nil {
		f := m.File
		if !filepath.IsAbs(f.Path) || f.Filename != filepath.Base(f.Path) ||
			!strings.EqualFold(filepath.Ext(f.Filename), ".xlsx") || f.MIME != xlsxMIME || f.Size <= 0 || !validSHA256(f.SHA256) {
			return fmt.Errorf("file requires an absolute XLSX path, exact basename, size, SHA-256 and XLSX MIME type")
		}
		if !utf8.ValidString(m.Text) || (m.Text != "" && strings.TrimSpace(m.Text) == "") || len(utf16.Encode([]rune(m.Text))) > 1024 {
			return fmt.Errorf("provide an empty caption or nonblank UTF-8 caption of at most 1024 UTF-16 units")
		}
	} else if !utf8.ValidString(m.Text) || strings.TrimSpace(m.Text) == "" || len(utf16.Encode([]rune(m.Text))) > 4096 {
		return fmt.Errorf("provide nonblank UTF-8 reply text of at most 4096 UTF-16 units")
	}
	return nil
}

// SendDirectReply sends one authorized text or XLSX reply in an exact private dialog.
// The caller holds the account runtime lock across receipt access and Telegram RPCs.
func (s *Session) SendDirectReply(ctx context.Context, m DirectReplyManifest, journalPath string, apply bool) (DirectReplyResult, error) {
	r := DirectReplyResult{DryRun: !apply, PeerID: m.PeerID, ReplyTo: m.ReplyTo, Text: m.Text, File: m.File}
	if err := ValidateDirectReplyManifest(m); err != nil {
		return r, err
	}
	journal, err := loadPublicationJournal(journalPath, 0)
	if err != nil {
		return r, err
	}
	target, err := s.checkDirectReplyTarget(ctx, m, &r)
	if err != nil {
		return r, err
	}

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
	if !apply {
		if m.File != nil {
			if err := verifyPublishFile(ctx, m.File.publicationFile()); err != nil {
				return r, err
			}
		}
		r.Status = "ready"
		return r, nil
	}

	var media *tg.InputMediaUploadedDocument
	if m.File != nil {
		media, err = s.uploadPublishFile(ctx, m.File.publicationFile())
		if err != nil {
			return r, err
		}
		media.ForceFile = true
		// The upload can take time. Recheck the account, username and parent before sending.
		target, err = s.checkDirectReplyTarget(ctx, m, &r)
		if err != nil {
			return r, err
		}
	}
	r.Status = "ready"
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
	reply := &tg.InputReplyToMessage{ReplyToMsgID: m.ReplyTo}
	var updates tg.UpdatesClass
	var sendErr error
	if media == nil {
		updates, sendErr = s.raw.MessagesSendMessage(callCtx, &tg.MessagesSendMessageRequest{
			Peer: target.InputPeer, ReplyTo: reply, Message: m.Text, RandomID: randomID, NoWebpage: true,
		})
	} else {
		updates, sendErr = s.raw.MessagesSendMedia(callCtx, &tg.MessagesSendMediaRequest{
			Peer: target.InputPeer, ReplyTo: reply, Message: m.Text, RandomID: randomID, Media: media,
		})
	}
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

func (s *Session) checkDirectReplyTarget(ctx context.Context, m DirectReplyManifest, r *DirectReplyResult) (resolvedTarget, error) {
	self, err := s.SelfProfile(ctx)
	if err != nil {
		return resolvedTarget{}, err
	}
	if self.ID != InterviewTopicAccountID || !strings.EqualFold(self.Username, SavedMessagesMainUsername) {
		return resolvedTarget{}, fmt.Errorf("active account differs from main/@Pheik13")
	}
	r.AccountID = self.ID
	username := "@" + m.Username
	delete(s.dialogCache, username)
	target, err := s.resolveTarget(ctx, username)
	if err != nil {
		return resolvedTarget{}, err
	}
	userPeer, ok := target.InputPeer.(*tg.InputPeerUser)
	if !ok || target.Chat.Type != "user" || target.Chat.ID != m.PeerID || userPeer.UserID != m.PeerID ||
		!strings.EqualFold(target.Chat.Username, m.Username) {
		return resolvedTarget{}, fmt.Errorf("resolved user does not match the reviewed recipient")
	}
	r.Recipient, r.Username = target.Chat.Display, target.Chat.Username
	parent, entities, err := s.fetchMessageByID(ctx, target, m.ReplyTo)
	if err != nil {
		return resolvedTarget{}, err
	}
	incoming, ok := parent.(*tg.Message)
	user, haveUser := entities.User(m.PeerID)
	if !haveUser || user.Bot || user.Deleted || user.Self ||
		!ok || incoming.ID != m.ReplyTo || incoming.Out || !directReplyPeerIs(incoming.PeerID, m.PeerID) ||
		incoming.FromID != nil && !directReplyPeerIs(incoming.FromID, m.PeerID) ||
		strings.TrimSpace(incoming.Message) == "" || topicTextHash(incoming.Message) != m.ReplyToTextSHA256 {
		return resolvedTarget{}, fmt.Errorf("incoming message does not match the reviewed private reply target")
	}
	r.IncomingText = incoming.Message
	return target, nil
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
			if msg, ok := update.Message.(*tg.Message); ok {
				if _, err := verifyDirectReplyMessage(msg, m); err != nil {
					continue
				}
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
	document, err := verifyDirectReplyMessage(out, m)
	if err != nil || document == nil {
		return err
	}
	hash := sha256.New()
	writer := &boundedPublicationWriter{writer: hash, remaining: m.File.Size}
	if err := s.downloadPublishedDocument(ctx, document, writer, "direct_reply"); err != nil {
		return fmt.Errorf("verify server document download: %w", err)
	}
	if writer.remaining != 0 || hex.EncodeToString(hash.Sum(nil)) != m.File.SHA256 {
		return fmt.Errorf("server document bytes differ from reviewed size or SHA-256")
	}
	return nil
}

func verifyDirectReplyMessage(out *tg.Message, m DirectReplyManifest) (*tg.Document, error) {
	if !out.Out || !directReplyPeerIs(out.PeerID, m.PeerID) ||
		out.FromID != nil && !directReplyPeerIs(out.FromID, InterviewTopicAccountID) ||
		out.Message != m.Text {
		return nil, fmt.Errorf("server reply identity or content differs")
	}
	reply, ok := out.ReplyTo.(*tg.MessageReplyHeader)
	if !ok || reply.ReplyToMsgID != m.ReplyTo ||
		reply.ReplyToPeerID != nil && !directReplyPeerIs(reply.ReplyToPeerID, m.PeerID) {
		return nil, fmt.Errorf("server reply targets a different message")
	}
	if m.File != nil {
		if out.GroupedID != 0 {
			return nil, fmt.Errorf("server reply document is grouped")
		}
		return verifyPublishedDocument(out.Media, m.File.publicationFile(), false)
	}
	if _, emptyMedia := out.Media.(*tg.MessageMediaEmpty); out.Media != nil && !emptyMedia {
		return nil, fmt.Errorf("server reply contains unexpected media")
	}
	return nil, nil
}
