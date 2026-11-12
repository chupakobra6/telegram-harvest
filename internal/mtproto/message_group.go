package mtproto

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"
)

const messageGroupLimit = 10

type messageDeliveryBatch struct {
	start, end int
}

// Preserve attachment order and native playback by grouping consecutive files of the same kind.
func messageDeliveryBatches(items []messageDeliveryItem, groupFiles bool) []messageDeliveryBatch {
	var batches []messageDeliveryBatch
	for start := 0; start < len(items); {
		end := start + 1
		first := items[start].file
		if groupFiles && first != nil {
			for end < len(items) && end-start < messageGroupLimit &&
				items[end].file != nil && items[end].file.Kind == first.Kind {
				end++
			}
		}
		for i := start; i < end; i++ {
			items[i].grouped = end-start > 1
		}
		batches = append(batches, messageDeliveryBatch{start, end})
		start = end
	}
	return batches
}

func verifyMessageDeliveryBatch(rows []MessageDeliveryRow) error {
	if len(rows) == 1 {
		return nil
	}
	for i, row := range rows {
		if row.ServerVerified != rows[0].ServerVerified ||
			(row.ServerVerified && (row.GroupedID == 0 || row.GroupedID != rows[0].GroupedID ||
				(i > 0 && row.MessageID <= rows[i-1].MessageID))) {
			return fmt.Errorf("message items %d–%d do not form one verified Telegram group in order", rows[0].Index, rows[len(rows)-1].Index)
		}
	}
	return nil
}

func (s *Session) prepareDeliveryMedia(ctx context.Context, target resolvedTarget, item messageDeliveryItem) (tg.InputMediaClass, error) {
	upload, err := s.uploadPublishFile(ctx, item.file)
	if err != nil {
		return nil, err
	}
	upload.ForceFile = item.file.Kind == "document"
	if !item.grouped {
		return upload, nil
	}
	if err := s.beforeRPC(ctx, "upload_message_delivery_media"); err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultRPCTimeout)
	defer cancel()
	media, err := s.raw.MessagesUploadMedia(callCtx, &tg.MessagesUploadMediaRequest{Peer: target.InputPeer, Media: upload})
	if err != nil {
		return nil, err
	}
	if document, ok := media.(*tg.MessageMediaDocument); ok {
		if doc, ok := document.Document.(*tg.Document); ok && doc.ID != 0 {
			return &tg.InputMediaDocument{ID: &tg.InputDocument{
				ID: doc.ID, AccessHash: doc.AccessHash, FileReference: doc.FileReference,
			}}, nil
		}
	}
	return nil, fmt.Errorf("uploadMedia did not return a reusable document")
}

func (s *Session) sendDeliveryBatch(ctx context.Context, target resolvedTarget, m MessageManifest,
	items []messageDeliveryItem, media []tg.InputMediaClass, intent string, firstIndex int,
) ([]int, error) {
	if len(items) == 1 {
		item := items[0]
		randomID := messageRandomID(intent, firstIndex)
		updates, err := s.sendDeliveryRPC(ctx, target, m, item, media[0], randomID)
		if err != nil {
			return nil, err
		}
		id := deliveryMessageID(updates, randomID, func(out *tg.Message) bool {
			_, err := verifyDeliveredMessage(out, target, m, item)
			return err == nil
		})
		return []int{id}, nil
	}

	req := &tg.MessagesSendMultiMediaRequest{
		Peer: target.InputPeer, ReplyTo: deliveryInputReply(m.TopicID, m.ReplyTo),
		MultiMedia: make([]tg.InputSingleMedia, len(items)),
	}
	if target.Chat.Type == "supergroup" {
		req.SendAs = &tg.InputPeerSelf{}
	}
	for i, item := range items {
		req.MultiMedia[i] = tg.InputSingleMedia{
			Media: media[i], Message: item.text, RandomID: messageRandomID(intent, firstIndex+i),
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultRPCTimeout)
	defer cancel()
	updates, err := s.raw.MessagesSendMultiMedia(callCtx, req)
	if err != nil {
		return nil, err
	}
	ids := make([]int, len(items))
	switch updates.(type) {
	case *tg.Updates, *tg.UpdatesCombined:
		for i, input := range req.MultiMedia {
			// Identical documents can appear twice. Only the server random-ID mapping identifies a member.
			ids[i] = deliveryMessageID(updates, input.RandomID, func(*tg.Message) bool { return false })
		}
	}
	return ids, nil
}
