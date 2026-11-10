package mtproto

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

type directReplyWire struct {
	t         *testing.T
	manifest  DirectReplyManifest
	incoming  tg.Message
	outgoing  *tg.Message
	recipient *tg.User
	failSend  bool
	badReply  bool
	mapped    bool
	sends     int
}

func (w *directReplyWire) Invoke(_ context.Context, input bin.Encoder, output bin.Decoder) error {
	var response bin.Encoder
	switch req := input.(type) {
	case *tg.UsersGetFullUserRequest:
		response = &tg.UsersUserFull{
			FullUser: tg.UserFull{ID: InterviewTopicAccountID},
			Users:    []tg.UserClass{&tg.User{ID: InterviewTopicAccountID, Username: SavedMessagesMainUsername}},
		}
	case *tg.ContactsResolveUsernameRequest:
		if req.Username != w.manifest.Username {
			w.t.Fatal("wrong lookup", req.Username)
		}
		response = &tg.ContactsResolvedPeer{
			Peer:  &tg.PeerUser{UserID: w.recipient.ID},
			Users: []tg.UserClass{w.recipient},
		}
	case *tg.MessagesGetMessagesRequest:
		id := req.ID[0].(*tg.InputMessageID).ID
		msg := &w.incoming
		if id != w.incoming.ID {
			msg = w.outgoing
		}
		if msg == nil || msg.ID != id {
			w.t.Fatal("unexpected lookup", id)
		}
		response = &tg.MessagesMessages{
			Messages: []tg.MessageClass{msg},
			Users:    []tg.UserClass{w.recipient},
		}
	case *tg.MessagesSendMessageRequest:
		w.sends++
		user, userOK := req.Peer.(*tg.InputPeerUser)
		reply, replyOK := req.ReplyTo.(*tg.InputReplyToMessage)
		if !userOK || user.UserID != w.manifest.PeerID || !replyOK || reply.ReplyToMsgID != w.manifest.ReplyTo ||
			req.Message != w.manifest.Text || req.RandomID == 0 || !req.NoWebpage ||
			req.SendAs != nil || req.AllowPaidStars != 0 || req.ScheduleDate != 0 {
			w.t.Fatalf("unexpected send request: %+v", req)
		}
		if w.failSend {
			return errors.New("lost send response")
		}
		replyID := reply.ReplyToMsgID
		if w.badReply {
			replyID++
		}
		w.outgoing = &tg.Message{
			ID: 52, Out: true, PeerID: &tg.PeerUser{UserID: user.UserID}, Message: req.Message,
			ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: replyID},
			Media:   &tg.MessageMediaEmpty{},
		}
		response = &tg.UpdateShortSentMessage{ID: w.outgoing.ID, Out: true}
		if w.mapped {
			response = &tg.Updates{Updates: []tg.UpdateClass{
				&tg.UpdateMessageID{RandomID: req.RandomID + 1, ID: 59},
				&tg.UpdateMessageID{RandomID: req.RandomID, ID: w.outgoing.ID},
			}}
		}
	default:
		w.t.Fatalf("unexpected RPC %T", input)
	}
	var buf bin.Buffer
	if err := response.Encode(&buf); err != nil {
		return err
	}
	return output.Decode(&buf)
}

func directReplyFixture(t *testing.T) (*Session, *directReplyWire, string) {
	t.Helper()
	w := &directReplyWire{
		t:        t,
		manifest: DirectReplyManifest{PeerID: 42, Username: "example_user", ReplyTo: 51, Text: "Ответ\n\nСтрока"},
		incoming: tg.Message{ID: 51, PeerID: &tg.PeerUser{UserID: 42}, Message: "Вопрос"},
		recipient: &tg.User{
			ID: 42, AccessHash: 123, Username: "example_user", FirstName: "Example",
		},
	}
	w.manifest.ReplyToTextSHA256 = topicTextHash(w.incoming.Message)
	return &Session{raw: tg.NewClient(w), dialogCache: make(map[string]resolvedTarget)}, w, filepath.Join(t.TempDir(), "receipts.json")
}

func TestDirectReplyPreviewsSendsAndReopensReceipt(t *testing.T) {
	s, w, journal := directReplyFixture(t)
	w.mapped = true
	ctx := context.Background()
	r, err := s.SendDirectReply(ctx, w.manifest, journal, false)
	if err != nil || r.Status != "ready" || r.IncomingText != w.incoming.Message || w.sends != 0 {
		t.Fatalf("unexpected preview: %+v, %v", r, err)
	}
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Fatalf("preview changed journal: %v", err)
	}
	r, err = s.SendDirectReply(ctx, w.manifest, journal, true)
	if err != nil || r.Status != "sent" || !r.ServerVerified || r.MessageID != 52 || w.sends != 1 {
		t.Fatalf("unexpected send: %+v, %v", r, err)
	}
	r, err = s.SendDirectReply(ctx, w.manifest, journal, true)
	if err != nil || r.Status != "already_sent" || !r.ServerVerified || w.sends != 1 {
		t.Fatalf("unexpected repeat: %+v, %v", r, err)
	}
}

func TestDirectReplyRefusesOtherRecipientsAndEditedParents(t *testing.T) {
	for _, change := range []struct {
		name  string
		apply func(*directReplyWire)
	}{
		{"reassigned username", func(w *directReplyWire) { w.recipient.ID++ }},
		{"bot", func(w *directReplyWire) { w.recipient.Bot = true }},
		{"other dialog", func(w *directReplyWire) { w.incoming.PeerID = &tg.PeerUser{UserID: 90} }},
		{"outgoing parent", func(w *directReplyWire) { w.incoming.Out = true }},
		{"edited text", func(w *directReplyWire) { w.incoming.Message += "!" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			s, w, journal := directReplyFixture(t)
			change.apply(w)
			if r, err := s.SendDirectReply(context.Background(), w.manifest, journal, true); err == nil || r.ServerVerified || w.sends != 0 {
				t.Fatalf("unsafe send: %+v, %v, sends=%d", r, err, w.sends)
			}
		})
	}
}

func TestDirectReplyUnknownOutcomeNeverSendsAgain(t *testing.T) {
	for _, failure := range []string{"send", "readback"} {
		t.Run(failure, func(t *testing.T) {
			s, w, journal := directReplyFixture(t)
			w.failSend, w.badReply = failure == "send", failure == "readback"
			ctx := context.Background()
			if r, err := s.SendDirectReply(ctx, w.manifest, journal, true); err == nil || r.Status != "outcome_unknown" || r.ServerVerified || w.sends != 1 {
				t.Fatalf("unexpected outcome: %+v, %v, sends=%d", r, err, w.sends)
			}
			for _, text := range []string{w.manifest.Text, "Edited after send"} {
				m := w.manifest
				m.Text = text
				if r, err := s.SendDirectReply(ctx, m, journal, true); err == nil || r.ServerVerified || w.sends != 1 {
					t.Fatalf("unsafe repeat: %+v, %v, sends=%d", r, err, w.sends)
				}
			}
		})
	}
}
