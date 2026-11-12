package mtproto

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/gotd/td/tg"
)

type MessageAccount struct {
	Profile  string `json:"profile"`
	ID       int64  `json:"id"`
	Username string `json:"username,omitempty"`
}

type MessagePrepareInput struct {
	To       string
	TopicID  int
	ReplyTo  int
	Text     string
	Files    []MessageFileInput
	Entities []InterviewTopicEntity
	Draft    bool
	Intent   string
}

type MessageFileInput struct {
	Path    string
	Kind    string
	Caption string
}

type MessageDestination struct {
	Type       string `json:"type"`
	ID         int64  `json:"id"`
	Username   string `json:"username,omitempty"`
	Title      string `json:"title"`
	TopicTitle string `json:"topic_title,omitempty"`
	Selector   string `json:"selector"`
}

type MessageManifest struct {
	Account       MessageAccount         `json:"account"`
	Destination   MessageDestination     `json:"destination"`
	TopicID       int                    `json:"topic_id,omitempty"`
	ReplyTo       int                    `json:"reply_to,omitempty"`
	ReplyToSHA256 string                 `json:"reply_to_sha256,omitempty"`
	Text          string                 `json:"text,omitempty"`
	Entities      []InterviewTopicEntity `json:"entities,omitempty"`
	Files         []InterviewTopicFile   `json:"files,omitempty"`
	GroupFiles    bool                   `json:"group_files,omitempty"`
	FileCaptions  []string               `json:"file_captions,omitempty"`
	Draft         bool                   `json:"draft,omitempty"`
	Intent        string                 `json:"intent,omitempty"`
	Fingerprint   string                 `json:"fingerprint"`
}

type MessageResult struct {
	Fingerprint string               `json:"fingerprint"`
	Account     MessageAccount       `json:"account"`
	Destination MessageDestination   `json:"destination"`
	TopicID     int                  `json:"topic_id,omitempty"`
	ReplyTo     int                  `json:"reply_to,omitempty"`
	DryRun      bool                 `json:"dry_run"`
	Ready       bool                 `json:"ready"`
	Complete    bool                 `json:"complete"`
	Status      string               `json:"status"`
	Rows        []MessageDeliveryRow `json:"rows,omitempty"`
}

type MessageDeliveryRow struct {
	Index          int    `json:"index"`
	Kind           string `json:"kind"`
	MessageID      int    `json:"message_id,omitempty"`
	GroupedID      int64  `json:"grouped_id,string,omitempty"`
	Status         string `json:"status"`
	ServerVerified bool   `json:"server_verified"`
	ServerFilename string `json:"server_filename,omitempty"`
}

func messageResult(m MessageManifest, apply bool) MessageResult {
	return MessageResult{Fingerprint: m.Fingerprint, Account: m.Account, Destination: m.Destination,
		TopicID: m.TopicID, ReplyTo: m.ReplyTo, DryRun: !apply, Status: "blocked"}
}

const readOnlyStudyAccountID int64 = 6889183661

func readOnlyStudyIdentity(id int64, username string) bool {
	return id == readOnlyStudyAccountID || strings.EqualFold(username, "Pheik15")
}

func validateMessageAccount(a MessageAccount) error {
	if a.Profile == "" || a.Profile == "study" || a.ID <= 0 || readOnlyStudyIdentity(a.ID, a.Username) {
		return fmt.Errorf("message operations require main or a bound registered account; study is read-only")
	}
	if a.Profile == "main" && (a.ID != InterviewTopicAccountID || !strings.EqualFold(a.Username, SavedMessagesMainUsername)) {
		return fmt.Errorf("main must be bound to @Pheik13")
	}
	return nil
}

func validateMessageIntent(intent string, draft bool) error {
	if intent != "" && (draft || len(intent) > 100 || !utf8.ValidString(intent) || strings.ContainsAny(intent, "/\\") ||
		strings.IndexFunc(intent, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0) {
		return fmt.Errorf("an explicit delivery intent must be valid UTF-8 of at most 100 bytes without whitespace, control characters or slashes, and cannot be used for drafts")
	}
	return nil
}

func validateMessageContent(text string, entities []InterviewTopicEntity, fileCount int, draft bool) error {
	if !utf8.ValidString(text) || (text != "" && strings.TrimSpace(text) == "") || len(utf16.Encode([]rune(text))) > 4096 {
		return fmt.Errorf("message text must be UTF-8, nonblank when supplied, and at most 4096 UTF-16 units")
	}
	if fileCount > 20 || (text == "" && fileCount == 0) || (draft && (text == "" || fileCount != 0)) {
		return fmt.Errorf("provide text and/or up to 20 files; a native draft requires text and accepts no files")
	}
	_, err := topicEntities(text, entities)
	return err
}

func messageFingerprint(m MessageManifest) (string, error) {
	m.Fingerprint = ""
	encoded, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return topicTextHash(string(encoded)), nil
}

func ValidateMessageManifest(m MessageManifest, account MessageAccount) error {
	if err := validateMessageAccount(account); err != nil {
		return err
	}
	if m.Account.Profile != account.Profile || m.Account.ID != account.ID || (account.Username != "" && !strings.EqualFold(m.Account.Username, account.Username)) {
		return fmt.Errorf("manifest account differs from selected profile or bound Telegram account")
	}
	if err := validateMessageSelector(m.Destination.Selector); err != nil {
		return err
	}
	if err := validateMessageDestination(m.Destination, account.ID, m.TopicID); err != nil {
		return err
	}
	if m.ReplyTo < 0 || m.ReplyTo == 0 && m.ReplyToSHA256 != "" || m.ReplyTo > 0 && !validSHA256(m.ReplyToSHA256) {
		return fmt.Errorf("reply requires an exact positive message ID and SHA-256")
	}
	if err := validateMessageContent(m.Text, m.Entities, len(m.Files), m.Draft); err != nil {
		return err
	}
	if err := validateMessageIntent(m.Intent, m.Draft); err != nil {
		return err
	}
	if m.GroupFiles && len(m.Files) < 2 {
		return fmt.Errorf("grouped delivery requires multiple files")
	}
	if len(m.FileCaptions) != 0 && len(m.FileCaptions) != len(m.Files) {
		return fmt.Errorf("file captions must match the attachment list")
	}
	for _, caption := range m.FileCaptions {
		if err := validateMessageCaption(caption); err != nil {
			return err
		}
	}
	for _, file := range m.Files {
		if err := validateMessageFile(file); err != nil {
			return err
		}
	}
	fingerprint, err := messageFingerprint(m)
	if err != nil {
		return err
	}
	if fingerprint != m.Fingerprint {
		return fmt.Errorf("manifest fingerprint changed; prepare and review the current inputs")
	}
	return nil
}

func validateMessageCaption(text string) error {
	if !utf8.ValidString(text) || (text != "" && strings.TrimSpace(text) == "") || len(utf16.Encode([]rune(text))) > 1024 {
		return fmt.Errorf("file caption must be UTF-8, nonblank when supplied, and at most 1024 UTF-16 units")
	}
	return nil
}

// PrepareMessage reads the exact peer and local files. It performs no Telegram writes.
func (s *Session) PrepareMessage(ctx context.Context, input MessagePrepareInput, account MessageAccount) (MessageManifest, error) {
	m := MessageManifest{Account: account, TopicID: input.TopicID, ReplyTo: input.ReplyTo, Text: input.Text, Entities: input.Entities, Draft: input.Draft, Intent: input.Intent}
	if err := validateMessageAccount(account); err != nil {
		return m, err
	}
	if err := validateMessageContent(input.Text, input.Entities, len(input.Files), input.Draft); err != nil {
		return m, err
	}
	if err := validateMessageIntent(input.Intent, input.Draft); err != nil {
		return m, err
	}
	if err := validateMessageSelector(input.To); err != nil {
		return m, err
	}
	if input.TopicID < 0 || input.ReplyTo < 0 {
		return m, fmt.Errorf("topic and reply IDs must be positive when supplied")
	}
	m.GroupFiles = len(input.Files) > 1
	captions := make([]string, len(input.Files))
	for i, inputFile := range input.Files {
		if err := validateMessageCaption(inputFile.Caption); err != nil {
			return m, err
		}
		captions[i] = inputFile.Caption
		if inputFile.Caption != "" {
			m.FileCaptions = captions
		}
		file, err := prepareMessageFile(ctx, inputFile)
		if err != nil {
			return m, err
		}
		m.Files = append(m.Files, file)
	}
	self, err := s.checkMessageAccount(ctx, account)
	if err != nil {
		return m, err
	}
	m.Account = self
	target, err := s.resolveTarget(ctx, input.To)
	if err != nil {
		return m, err
	}
	target, m.Destination, err = s.refreshMessageTarget(ctx, target, input.To, self.ID, m.TopicID)
	if err != nil {
		return m, err
	}
	if m.ReplyTo > 0 {
		m.ReplyToSHA256, err = s.messageParentHash(ctx, target, m.TopicID, m.ReplyTo)
		if err != nil {
			return m, err
		}
	}
	m.Fingerprint, err = messageFingerprint(m)
	return m, err
}

type messageDeliveryItem struct {
	text     string
	entities []tg.MessageEntityClass
	file     *InterviewTopicFile
	grouped  bool
}

func messageItems(m MessageManifest) ([]messageDeliveryItem, error) {
	items := make([]messageDeliveryItem, 0, len(m.Files)+1)
	if m.Text != "" {
		entities, err := topicEntities(m.Text, m.Entities)
		if err != nil {
			return nil, err
		}
		items = append(items, messageDeliveryItem{text: m.Text, entities: entities})
	}
	for i := range m.Files {
		item := messageDeliveryItem{file: &m.Files[i]}
		if len(m.FileCaptions) != 0 {
			item.text = m.FileCaptions[i]
		}
		items = append(items, item)
	}
	return items, nil
}

// Paths and display names can change without creating a new delivery intent.
func messageIntent(m MessageManifest) (string, error) {
	m.Account = MessageAccount{ID: m.Account.ID}
	m.Destination = MessageDestination{Type: m.Destination.Type, ID: m.Destination.ID}
	m.ReplyToSHA256 = ""
	// Changing the transport alone must not bypass receipts from a previous delivery.
	m.GroupFiles = false
	m.Files = append([]InterviewTopicFile(nil), m.Files...)
	for i := range m.Files {
		m.Files[i].Path = ""
	}
	return messageFingerprint(m)
}

func messageRandomID(intent string, index int) int64 {
	hash := sha256.Sum256([]byte(fmt.Sprintf("message-delivery/%s/%d", intent, index)))
	return int64(binary.BigEndian.Uint64(hash[:8]) | 1)
}

// DeliverMessage requires the CLI's account-wide journal lock and session runtime lock.
// All inputs are checked before the first send, and each item has a durable receipt.
func (s *Session) DeliverMessage(ctx context.Context, m MessageManifest, account MessageAccount, journalDir string, apply bool) (MessageResult, error) {
	result := messageResult(m, apply)
	if err := ValidateMessageManifest(m, account); err != nil {
		return result, err
	}
	if m.Draft || !filepath.IsAbs(journalDir) {
		return result, fmt.Errorf("send requires a delivery manifest and a private absolute journal directory")
	}
	items, err := messageItems(m)
	if err != nil {
		return result, err
	}
	batches := messageDeliveryBatches(items, m.GroupFiles)
	intent, err := messageIntent(m)
	if err != nil {
		return result, err
	}
	journalPath := filepath.Join(journalDir, intent+".receipts.json")
	journal, err := loadPublicationJournal(journalPath, 0)
	if err != nil {
		return result, err
	}
	save := func() error { return savePublishJournal(journalPath, journal) }
	target, err := s.checkMessageTarget(ctx, m, account)
	if err != nil {
		return result, err
	}
	for i, item := range items {
		row := MessageDeliveryRow{Index: i + 1, Kind: "text", Status: "would_send"}
		if item.file != nil {
			row.Kind = item.file.Kind
		}
		key := fmt.Sprint(i + 1)
		if receipt, known := journal[key]; known {
			row.MessageID = receipt.ID
			if receipt.Hash != intent || receipt.ID == 0 {
				row.Status = "outcome_unknown"
				result.Rows = append(result.Rows, row)
				result.Status = row.Status
				return result, fmt.Errorf("message item %s has an unresolved receipt; reconcile without repeating the send", key)
			}
			row.ServerFilename, row.GroupedID, err = s.readBackDeliveredMessage(ctx, target, m, item, receipt.ID)
			if err != nil {
				row.Status = "outcome_unknown"
				result.Rows = append(result.Rows, row)
				result.Status = row.Status
				return result, err
			}
			row.Status, row.ServerVerified = "already_sent", true
			if apply && receipt.Pending {
				receipt.Pending = false
				journal[key] = receipt
				if err := save(); err != nil {
					return result, err
				}
			}
		} else if err := verifyPublishFile(ctx, item.file); err != nil {
			return result, fmt.Errorf("message item %s: %w", key, err)
		}
		result.Rows = append(result.Rows, row)
	}
	for _, batch := range batches {
		if err := verifyMessageDeliveryBatch(result.Rows[batch.start:batch.end]); err != nil {
			result.Status = "outcome_unknown"
			return result, err
		}
	}
	result.Ready, result.Status = true, "ready"
	if !apply {
		return result, nil
	}
	if err := os.MkdirAll(journalDir, 0700); err != nil {
		return result, err
	}
	for _, batch := range batches {
		rows := result.Rows[batch.start:batch.end]
		if rows[0].ServerVerified {
			continue
		}
		target, err = s.refreshReviewedMessageTarget(ctx, target, m, account)
		if err != nil {
			result.Status = "blocked"
			return result, err
		}
		batchItems := items[batch.start:batch.end]
		media := make([]tg.InputMediaClass, len(batchItems))
		for i, item := range batchItems {
			if item.file == nil {
				continue
			}
			media[i], err = s.prepareDeliveryMedia(ctx, target, item)
			if err != nil {
				result.Status = "blocked"
				return result, fmt.Errorf("upload item %d: %w", rows[i].Index, err)
			}
		}
		if batchItems[0].file != nil {
			target, err = s.refreshReviewedMessageTarget(ctx, target, m, account)
			if err != nil {
				result.Status = "blocked"
				return result, err
			}
		}
		if err := s.beforeRPC(ctx, "send_message_delivery"); err != nil {
			result.Status = "blocked"
			return result, err
		}
		for _, row := range rows {
			journal[fmt.Sprint(row.Index)] = topicPublishReceipt{Hash: intent, Pending: true}
		}
		if err := save(); err != nil {
			result.Status = "blocked"
			return result, err
		}
		result.Status = "outcome_unknown"
		for i := range rows {
			rows[i].Status = result.Status
		}
		ids, sendErr := s.sendDeliveryBatch(ctx, target, m, batchItems, media, intent, rows[0].Index)
		if sendErr != nil {
			return result, fmt.Errorf("message item %d send outcome unknown; reconcile without retry: %w", rows[0].Index, sendErr)
		}
		for i, id := range ids {
			rows[i].MessageID = id
			journal[fmt.Sprint(rows[i].Index)] = topicPublishReceipt{Hash: intent, ID: id, Pending: true}
		}
		if err := save(); err != nil {
			return result, err
		}
		for i := range rows {
			row := &rows[i]
			if row.MessageID <= 0 {
				return result, fmt.Errorf("message item %d returned no unambiguous ID; reconcile without retry", row.Index)
			}
			row.ServerFilename, row.GroupedID, err = s.readBackDeliveredMessage(ctx, target, m, batchItems[i], row.MessageID)
			if err != nil {
				return result, fmt.Errorf("message item %d readback unverified: %w", row.Index, err)
			}
		}
		for i := range rows {
			rows[i].Status, rows[i].ServerVerified = "sent_verified", true
		}
		if err := verifyMessageDeliveryBatch(rows); err != nil {
			for i := range rows {
				rows[i].Status, rows[i].ServerVerified = "outcome_unknown", false
			}
			return result, err
		}
		for _, row := range rows {
			journal[fmt.Sprint(row.Index)] = topicPublishReceipt{Hash: intent, ID: row.MessageID}
		}
		if err := save(); err != nil {
			return result, err
		}
	}
	result.Complete, result.Status = true, "sent_verified"
	return result, nil
}

func (s *Session) sendDeliveryRPC(ctx context.Context, target resolvedTarget, m MessageManifest, item messageDeliveryItem, media tg.InputMediaClass, randomID int64) (tg.UpdatesClass, error) {
	callCtx, cancel := context.WithTimeout(ctx, defaultRPCTimeout)
	defer cancel()
	var sender tg.InputPeerClass
	if target.Chat.Type == "supergroup" {
		sender = &tg.InputPeerSelf{}
	}
	reply := deliveryInputReply(m.TopicID, m.ReplyTo)
	if media == nil {
		return s.raw.MessagesSendMessage(callCtx, &tg.MessagesSendMessageRequest{
			Peer: target.InputPeer, ReplyTo: reply, Message: item.text, Entities: item.entities, RandomID: randomID, NoWebpage: true, SendAs: sender,
		})
	}
	return s.raw.MessagesSendMedia(callCtx, &tg.MessagesSendMediaRequest{
		Peer: target.InputPeer, ReplyTo: reply, Media: media, Message: item.text, RandomID: randomID, SendAs: sender,
	})
}

func deliveryMessageID(updates tg.UpdatesClass, randomID int64, matches func(*tg.Message) bool) int {
	var items []tg.UpdateClass
	switch value := updates.(type) {
	case *tg.UpdateShortSentMessage:
		return value.ID
	case *tg.UpdateShort:
		items = []tg.UpdateClass{value.Update}
	case *tg.Updates:
		items = value.Updates
	case *tg.UpdatesCombined:
		items = value.Updates
	}
	var mappedID, exactID int
	ambiguous := false
	for _, item := range items {
		var message tg.MessageClass
		switch update := item.(type) {
		case *tg.UpdateMessageID:
			if update.RandomID == randomID && update.ID > 0 {
				if mappedID != 0 && mappedID != update.ID {
					return 0
				}
				mappedID = update.ID
			}
		case *tg.UpdateNewMessage:
			message = update.Message
		case *tg.UpdateNewChannelMessage:
			message = update.Message
		}
		if out, ok := message.(*tg.Message); ok && matches(out) {
			ambiguous = ambiguous || (exactID != 0 && exactID != out.ID)
			exactID = out.ID
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

func (s *Session) readBackDeliveredMessage(ctx context.Context, target resolvedTarget, m MessageManifest, item messageDeliveryItem, id int) (string, int64, error) {
	message, _, err := s.fetchMessageByIDDirect(ctx, target, id)
	if err != nil {
		return "", 0, err
	}
	out, ok := message.(*tg.Message)
	if !ok || out.ID != id {
		return "", 0, fmt.Errorf("server returned a different message")
	}
	doc, err := verifyDeliveredMessage(out, target, m, item)
	if err != nil || doc == nil {
		return "", 0, err
	}
	if err := s.verifyDeliveryFileBytes(ctx, doc, *item.file); err != nil {
		return "", 0, err
	}
	return documentFileName(doc), out.GroupedID, nil
}

func verifyDeliveredMessage(out *tg.Message, target resolvedTarget, m MessageManifest, item messageDeliveryItem) (*tg.Document, error) {
	if !out.Out || !matchesDeliveryPeer(out.PeerID, target.Chat) ||
		(!directReplyPeerIs(out.FromID, m.Account.ID) && !(out.FromID == nil && target.Chat.Type == "user")) ||
		(out.GroupedID != 0) != item.grouped || !out.RichMessage.Zero() || out.Message != item.text {
		return nil, fmt.Errorf("server message sender, destination or content differs")
	}
	if !matchesDeliveryReply(out.ReplyTo, target.Chat, m.TopicID, m.ReplyTo) {
		return nil, fmt.Errorf("server message reply or topic differs")
	}
	if !matchesDeliveryEntities(out.Entities, item.entities, item.text) {
		return nil, fmt.Errorf("server message entities differ")
	}
	if item.file != nil {
		return verifyDeliveryDocument(out.Media, *item.file)
	}
	if _, empty := out.Media.(*tg.MessageMediaEmpty); out.Media != nil && !empty {
		return nil, errors.New("server text message has unexpected media")
	}
	return nil, nil
}
