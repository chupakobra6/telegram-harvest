package mtproto

import (
	"context"
	"errors"
	"fmt"

	"github.com/gotd/td/tg"
)

// SaveMessageDraft saves text only. A different existing draft requires the user
// to handle it in Telegram. The caller serializes writes for this account.
func (s *Session) SaveMessageDraft(ctx context.Context, m MessageManifest, account MessageAccount, apply bool) (MessageResult, error) {
	result := messageResult(m, apply)
	if err := ValidateMessageManifest(m, account); err != nil {
		return result, err
	}
	if !m.Draft {
		return result, fmt.Errorf("draft requires a manifest prepared for a native text draft")
	}
	target, err := s.checkMessageTarget(ctx, m, account)
	if err != nil {
		return result, err
	}
	entities, err := topicEntities(m.Text, m.Entities)
	if err != nil {
		return result, err
	}
	reply := deliveryInputReply(m.TopicID, m.ReplyTo)
	draft, err := s.readDeliveryDraft(ctx, target, m.TopicID)
	if err != nil {
		return result, err
	}
	row := MessageDeliveryRow{Index: 1, Kind: "draft"}
	result.Rows = append(result.Rows, row)
	rowResult := &result.Rows[0]
	switch draft.(type) {
	case nil, *tg.DraftMessageEmpty:
		result.Ready, result.Status, rowResult.Status = true, "ready", "would_save_draft"
	default:
		if !matchesDeliveryDraft(draft, m.Text, reply, entities) {
			result.Status, rowResult.Status = "draft_conflict", "draft_conflict"
			return result, fmt.Errorf("a different draft already exists at the destination")
		}
		result.Ready, result.Complete, result.Status = true, apply, "draft_already_present"
		rowResult.Status, rowResult.ServerVerified = result.Status, true
		return result, nil
	}
	if !apply {
		return result, nil
	}
	if err := s.beforeRPC(ctx, "save_message_draft"); err != nil {
		return result, err
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultRPCTimeout)
	saved, saveErr := s.raw.MessagesSaveDraft(callCtx, &tg.MessagesSaveDraftRequest{
		Peer: target.InputPeer, ReplyTo: reply, Message: m.Text, Entities: entities, NoWebpage: true,
	})
	cancel()
	// Reconciliation is a read even if the save RPC returned an error.
	draft, readErr := s.readDeliveryDraft(ctx, target, m.TopicID)
	if readErr == nil && matchesDeliveryDraft(draft, m.Text, reply, entities) {
		result.Complete, result.Status = true, "draft_saved"
		rowResult.Status, rowResult.ServerVerified = result.Status, true
		return result, nil
	}
	result.Status, rowResult.Status = "outcome_unknown", "outcome_unknown"
	if saveErr == nil && !saved {
		saveErr = fmt.Errorf("server did not confirm saveDraft")
	}
	if readErr == nil {
		readErr = fmt.Errorf("server draft differs")
	}
	return result, fmt.Errorf("native draft readback unverified: %w", errors.Join(saveErr, readErr))
}

func (s *Session) readDeliveryDraft(ctx context.Context, target resolvedTarget, topicID int) (tg.DraftMessageClass, error) {
	if topicID > 0 {
		topic, err := s.getDeliveryTopic(ctx, target, topicID)
		if err != nil {
			return nil, err
		}
		draft, _ := topic.GetDraft()
		return draft, nil
	}
	var result *tg.MessagesPeerDialogs
	err := s.performRPC(ctx, "read_message_draft", func(ctx context.Context) error {
		var err error
		result, err = s.raw.MessagesGetPeerDialogs(ctx, []tg.InputDialogPeerClass{&tg.InputDialogPeer{Peer: target.InputPeer}})
		return err
	})
	if err != nil {
		return nil, err
	}
	for _, entry := range result.Dialogs {
		if dialog, ok := entry.(*tg.Dialog); ok && matchesDeliveryPeer(dialog.Peer, target.Chat) {
			draft, _ := dialog.GetDraft()
			return draft, nil
		}
	}
	if len(result.Dialogs) > 0 {
		return nil, fmt.Errorf("server returned a different draft dialog")
	}
	return nil, nil
}

func matchesDeliveryDraft(draft tg.DraftMessageClass, text string, reply tg.InputReplyToClass, entities []tg.MessageEntityClass) bool {
	d, ok := draft.(*tg.DraftMessage)
	if !ok || d.Message != text || !d.NoWebpage || d.Media != nil || d.InvertMedia || d.Effect != 0 ||
		!d.RichMessage.Zero() || !d.SuggestedPost.Zero() || !matchesDeliveryEntities(d.Entities, entities, text) {
		return false
	}
	if reply == nil {
		return d.ReplyTo == nil
	}
	actual, actualOK := d.ReplyTo.(*tg.InputReplyToMessage)
	expected, expectedOK := reply.(*tg.InputReplyToMessage)
	return actualOK && expectedOK && actual.ReplyToMsgID == expected.ReplyToMsgID && actual.TopMsgID == expected.TopMsgID &&
		actual.ReplyToPeerID == nil && actual.QuoteText == "" && len(actual.QuoteEntities) == 0 && actual.QuoteOffset == 0 &&
		actual.MonoforumPeerID == nil && actual.TodoItemID == 0 && len(actual.PollOption) == 0
}
