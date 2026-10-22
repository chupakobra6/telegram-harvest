package mtproto

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

type topicUpdateFake struct {
	self       harvest.SelfProfile
	messages   map[int]*tg.Message
	reads      map[int]int
	edits      []int
	deletes    []int
	beforeRead func(int, int)
	failEdit   int
	mutateFail bool
	corrupt    string
}

func ownTopicMessage(id int, text string) *tg.Message {
	reply := &tg.MessageReplyHeader{ForumTopic: true}
	reply.SetReplyToMsgID(InterviewTopicID)
	reply.SetReplyToTopID(InterviewTopicID)
	return &tg.Message{ID: id, Out: true, FromID: &tg.PeerUser{UserID: InterviewTopicAccountID}, PeerID: &tg.PeerChannel{ChannelID: InterviewTopicChannelID}, ReplyTo: reply, Message: text}
}

func topicUpdateFixture(t *testing.T) (InterviewTopicUpdateManifest, *topicUpdateFake) {
	t.Helper()
	var data bytes.Buffer
	zw := zip.NewWriter(&data)
	file, _ := zw.Create("transcript.txt")
	_, _ = file.Write([]byte("complete local backup"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup.zip")
	if err := os.WriteFile(backup, data.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	m := InterviewTopicUpdateManifest{Profile: "main", AccountID: InterviewTopicAccountID, ChannelID: InterviewTopicChannelID, TopicID: InterviewTopicID,
		Edits:     []InterviewTopicEdit{{ID: 2300, ExpectedTextHash: topicTextHash("old caption"), NewText: "Unified caption", ExpectedMediaID: "document:42"}},
		DeleteZIP: []InterviewTopicDeleteZIP{{ID: 2348, ExpectedTextHash: topicTextHash("ZIP post"), ExpectedMediaID: "document:43", Filename: "backup.zip", LocalBackupPath: backup, SHA256: topicTextHash(data.String())}}}
	audio, zipPost := ownTopicMessage(2300, "old caption"), ownTopicMessage(2348, "ZIP post")
	audioMedia, zipMedia := &tg.MessageMediaDocument{}, &tg.MessageMediaDocument{}
	audioMedia.SetDocument(&tg.Document{ID: 42, AccessHash: 987654321, FileReference: []byte("private-reference"), MimeType: "audio/mp4", Size: 4, Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "recording.m4a"}}})
	zipMedia.SetDocument(&tg.Document{ID: 43, MimeType: "application/zip", Size: int64(data.Len()), Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "backup.zip"}}})
	audio.Media, zipPost.Media = audioMedia, zipMedia
	f := &topicUpdateFake{self: harvest.SelfProfile{ID: InterviewTopicAccountID, Username: SavedMessagesMainUsername}, messages: map[int]*tg.Message{2300: audio, 2348: zipPost}, reads: map[int]int{}}
	return m, f
}

func (f *topicUpdateFake) SelfProfile(context.Context) (harvest.SelfProfile, error) {
	return f.self, nil
}
func (f *topicUpdateFake) interviewTopicPrepare(context.Context) error { return nil }
func (f *topicUpdateFake) interviewTopicRead(_ context.Context, id int) (*tg.Message, error) {
	f.reads[id]++
	if f.beforeRead != nil {
		f.beforeRead(id, f.reads[id])
	}
	if f.messages[id] == nil {
		return nil, nil
	}
	copy := *f.messages[id]
	copy.Entities = append([]tg.MessageEntityClass(nil), copy.Entities...)
	return &copy, nil
}
func (f *topicUpdateFake) interviewTopicEdit(_ context.Context, id int, text string, entities []tg.MessageEntityClass) error {
	f.edits = append(f.edits, id)
	if f.failEdit == id && !f.mutateFail {
		return errors.New("unknown edit transport outcome")
	}
	f.messages[id].Message, f.messages[id].Entities = text, entities
	switch f.corrupt {
	case "media":
		f.messages[id].Media = nil
	case "topic":
		f.messages[id].ReplyTo = &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 999}
	case "entities":
		f.messages[id].Entities = []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 0, Length: 1}}
	}
	if f.failEdit == id {
		return errors.New("unknown edit transport outcome")
	}
	return nil
}
func (f *topicUpdateFake) interviewTopicDeleteZIP(_ context.Context, id int) error {
	f.deletes = append(f.deletes, id)
	delete(f.messages, id)
	return nil
}

func TestInterviewTopicUpdateDryRunApplyAndIdempotency(t *testing.T) {
	m, f := topicUpdateFixture(t)
	protected, _ := topicProtectedSnapshot(f.messages[2300])
	result, err := updateInterviewTopic(context.Background(), f, m, false)
	if err != nil || !result.Ready || !result.DryRun || result.Complete || len(f.edits)+len(f.deletes) != 0 {
		t.Fatal("dry-run performed writes or claimed completion", result, err)
	}
	result, err = updateInterviewTopic(context.Background(), f, m, true)
	if err != nil || !result.Complete || !reflect.DeepEqual(f.edits, []int{2300}) || !reflect.DeepEqual(f.deletes, []int{2348}) {
		t.Fatal("exact operations not completed", result, err)
	}
	after, _ := topicProtectedSnapshot(f.messages[2300])
	if !bytes.Equal(protected, after) || !result.Rows[0].ServerVerified || !result.Rows[1].ServerVerified {
		t.Fatal("edit changed protected media/reply metadata or missed proof", result)
	}
	result, err = updateInterviewTopic(context.Background(), f, m, true)
	if err != nil || !result.Complete || !result.Rows[0].AlreadyApplied || !result.Rows[1].AlreadyApplied || len(f.edits) != 1 || len(f.deletes) != 1 {
		t.Fatal("repeat mutated an applied edit/deletion", result, err, f.edits, f.deletes)
	}
}

func TestInterviewTopicUpdatePreflightRejectsAllUnsafeInputsWithoutWrites(t *testing.T) {
	for _, scenario := range []string{"account", "username", "channel", "topic", "sender", "incoming", "hash", "media", "duplicate", "other-delete", "backup-hash", "backup-missing", "filename", "caption-limit", "text-limit", "missing-entities", "entity-splits-emoji"} {
		t.Run(scenario, func(t *testing.T) {
			m, f := topicUpdateFixture(t)
			switch scenario {
			case "account":
				f.self.ID++
			case "username":
				f.self.Username = "other"
			case "channel":
				m.ChannelID++
			case "topic":
				f.messages[2300].ReplyTo = &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 999}
			case "sender":
				f.messages[2300].FromID = &tg.PeerUser{UserID: 1}
			case "incoming":
				f.messages[2300].Out = false
			case "hash":
				m.Edits[0].ExpectedTextHash = strings.Repeat("a", 64)
			case "media":
				m.Edits[0].ExpectedMediaID = "document:999"
			case "duplicate":
				m.Edits = append(m.Edits, m.Edits[0])
			case "other-delete":
				m.DeleteZIP[0].ID = 999
			case "backup-hash":
				m.DeleteZIP[0].SHA256 = strings.Repeat("b", 64)
			case "backup-missing":
				m.DeleteZIP[0].LocalBackupPath = filepath.Join(t.TempDir(), "missing.zip")
			case "filename":
				m.DeleteZIP[0].Filename = "other.zip"
			case "caption-limit":
				m.Edits[0].NewText = strings.Repeat("😀", 513)
			case "text-limit":
				m.Edits[0].NewText = strings.Repeat("x", 4097)
			case "missing-entities":
				f.messages[2300].Entities = []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 0, Length: 3}}
			case "entity-splits-emoji":
				m.Edits[0].NewText = "😀 title"
				m.Edits[0].NewEntities = &[]InterviewTopicEntity{{Type: "bold", Offset: 1, Length: 1}}
			}
			result, err := updateInterviewTopic(context.Background(), f, m, true)
			if err == nil || result.Ready || result.Complete || len(f.edits)+len(f.deletes) != 0 {
				t.Fatal("unsafe input passed all-before-write preflight", scenario, result, err)
			}
		})
	}
}

func TestInterviewTopicUpdateFreshRecheckAndPartialFailure(t *testing.T) {
	for _, scenario := range []string{"changed-edit", "changed-zip", "changed-backup"} {
		t.Run(scenario, func(t *testing.T) {
			m, f := topicUpdateFixture(t)
			f.beforeRead = func(id, read int) {
				if read != 2 {
					return
				}
				if scenario == "changed-edit" && id == 2300 || scenario == "changed-zip" && id == 2348 {
					f.messages[id].Message = "parallel edit"
				}
				if scenario == "changed-backup" && id == 2348 {
					if err := os.WriteFile(m.DeleteZIP[0].LocalBackupPath, []byte("changed"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			result, err := updateInterviewTopic(context.Background(), f, m, true)
			if err == nil || result.Complete || len(f.deletes) != 0 {
				t.Fatal("stale state deleted a ZIP", result, err)
			}
			if scenario != "changed-edit" && (!result.Rows[0].ServerVerified || len(f.edits) != 1) {
				t.Fatal("earlier successful edit lost from partial result", result)
			}
		})
	}
}

func TestInterviewTopicUpdateUnknownRPCStopsWithoutRetryAndRecoversFromReadback(t *testing.T) {
	for _, didApply := range []bool{false, true} {
		t.Run(map[bool]string{false: "not-applied", true: "applied"}[didApply], func(t *testing.T) {
			m, f := topicUpdateFixture(t)
			f.failEdit, f.mutateFail = 2300, didApply
			result, err := updateInterviewTopic(context.Background(), f, m, true)
			if err == nil || result.Complete || len(f.edits) != 1 || len(f.deletes) != 0 || result.Rows[0].ServerVerified != didApply || result.Rows[0].OutcomeUnknown == didApply {
				t.Fatal("uncertain RPC retried or lost outcome evidence", result, err)
			}
			if didApply {
				f.failEdit = 0
				result, err = updateInterviewTopic(context.Background(), f, m, true)
				if err != nil || !result.Complete || len(f.edits) != 1 || len(f.deletes) != 1 {
					t.Fatal("server-applied edit sent again on recovery", result, err)
				}
			}
		})
	}
}

func TestInterviewTopicUpdateExactMediaReplyEntitiesReadback(t *testing.T) {
	for _, corrupt := range []string{"media", "topic", "entities"} {
		t.Run(corrupt, func(t *testing.T) {
			m, f := topicUpdateFixture(t)
			f.corrupt = corrupt
			result, err := updateInterviewTopic(context.Background(), f, m, true)
			if err == nil || result.Complete || result.Rows[0].ServerVerified || !result.Rows[0].OutcomeUnknown || len(f.deletes) != 0 {
				t.Fatal("protected field corruption called successful", result, err)
			}
		})
	}
}

type topicUpdateWireInvoker struct {
	t     *testing.T
	calls []string
}

func (f *topicUpdateWireInvoker) Invoke(_ context.Context, input bin.Encoder, _ bin.Decoder) error {
	switch request := input.(type) {
	case *tg.MessagesEditMessageRequest:
		f.calls = append(f.calls, "edit")
		peer, ok := request.Peer.(*tg.InputPeerChannel)
		if !ok || peer.ChannelID != InterviewTopicChannelID || request.Media != nil || request.ReplyMarkup != nil || request.ScheduleDate != 0 || request.RichMessage != nil || request.Message != "new caption" {
			f.t.Fatal("wire edit exceeded text/entities scope", request)
		}
	case *tg.ChannelsDeleteMessagesRequest:
		f.calls = append(f.calls, "delete_zip")
		channel, ok := request.Channel.(*tg.InputChannel)
		if !ok || channel.ChannelID != InterviewTopicChannelID || !reflect.DeepEqual(request.ID, []int{2348}) {
			f.t.Fatal("wire deletion exceeded authorized IDs", request)
		}
	default:
		f.t.Fatalf("unauthorized RPC type %T", input)
	}
	return nil
}

func TestInterviewTopicUpdateWireNeverSendsUploadsOrMarksRead(t *testing.T) {
	fake := &topicUpdateWireInvoker{t: t}
	source := &interviewTopicSession{Session: &Session{raw: tg.NewClient(fake)}, target: resolvedTarget{InputPeer: &tg.InputPeerChannel{ChannelID: InterviewTopicChannelID}}}
	if err := source.interviewTopicEdit(context.Background(), 2300, "new caption", nil); err != nil {
		t.Fatal(err)
	}
	if err := source.interviewTopicDeleteZIP(context.Background(), 2348); err != nil {
		t.Fatal(err)
	}
	if err := source.interviewTopicDeleteZIP(context.Background(), 999); err == nil || !reflect.DeepEqual(fake.calls, []string{"edit", "delete_zip"}) {
		t.Fatal("broad deletion or another RPC reached transport", err, fake.calls)
	}
}

func TestInterviewTopicUpdateIgnoresOnlyEphemeralDocumentFileReference(t *testing.T) {
	for _, apply := range []bool{false, true} {
		t.Run(map[bool]string{false: "dry-run", true: "apply"}[apply], func(t *testing.T) {
			m, f := topicUpdateFixture(t)
			f.beforeRead = func(id, read int) {
				if id == 2300 {
					document := f.messages[id].Media.(*tg.MessageMediaDocument).Document.(*tg.Document)
					document.FileReference = []byte(strings.Repeat("new-reference", read))
				}
			}
			result, err := updateInterviewTopic(context.Background(), f, m, apply)
			if err != nil || !result.Ready || result.Complete != apply {
				t.Fatal("ephemeral reference blocked unchanged content", result, err)
			}
			if !apply && !reflect.DeepEqual(result.Rows[0].ProtectedChangedPaths, []string{"media.Document.FileReference"}) {
				t.Fatal("diagnostics lost the concrete changing path", result.Rows[0])
			}
			if len(f.messages[2300].Media.(*tg.MessageMediaDocument).Document.(*tg.Document).FileReference) == 0 {
				t.Fatal("snapshot normalization mutated the server message")
			}
		})
	}
}

func TestInterviewTopicUpdateStillRejectsSemanticMediaAndReplyChanges(t *testing.T) {
	for _, field := range []string{"id", "size", "mime", "filename", "duration", "codec", "reply", "access-hash"} {
		t.Run(field, func(t *testing.T) {
			m, f := topicUpdateFixture(t)
			document := f.messages[2300].Media.(*tg.MessageMediaDocument).Document.(*tg.Document)
			document.Attributes = append(document.Attributes, &tg.DocumentAttributeAudio{Duration: 10}, &tg.DocumentAttributeVideo{Duration: 10, VideoCodec: "h264"})
			f.beforeRead = func(id, read int) {
				if id != 2300 || read != 2 {
					return
				}
				document.FileReference = []byte("temporary-reference-also-changed")
				switch field {
				case "id":
					document.ID++
				case "size":
					document.Size++
				case "mime":
					document.MimeType = "video/mp4"
				case "filename":
					document.Attributes[0].(*tg.DocumentAttributeFilename).FileName = "different.m4a"
				case "duration":
					document.Attributes[1].(*tg.DocumentAttributeAudio).Duration++
				case "codec":
					document.Attributes[2].(*tg.DocumentAttributeVideo).VideoCodec = "h265"
				case "reply":
					f.messages[2300].ReplyTo.(*tg.MessageReplyHeader).SetReplyToMsgID(222)
				case "access-hash":
					document.AccessHash++
				}
			}
			result, err := updateInterviewTopic(context.Background(), f, m, true)
			if err == nil || result.Complete || len(f.edits)+len(f.deletes) != 0 {
				t.Fatal("semantic change bypassed protected comparison", result, err)
			}
		})
	}
}

func TestInterviewTopicUpdateDiagnosticsContainPathsAndExactIntegerChangesOnly(t *testing.T) {
	_, f := topicUpdateFixture(t)
	document := f.messages[2300].Media.(*tg.MessageMediaDocument).Document.(*tg.Document)
	document.AccessHash = 9876543210123456
	before, _ := topicRawProtectedSnapshot(f.messages[2300])
	document.AccessHash++
	document.FileReference = []byte("private-value-that-must-not-be-printed")
	after, _ := topicRawProtectedSnapshot(f.messages[2300])
	paths := topicProtectedChangedPaths(before, after)
	if !reflect.DeepEqual(paths, []string{"media.Document.AccessHash", "media.Document.FileReference"}) {
		t.Fatal("diagnostics leaked values or rounded away an integer change", paths)
	}
}

func TestInterviewTopicUpdateNativeHashtagEntitiesUseUTF16(t *testing.T) {
	entities, err := topicEntities("🎙 #тест", []InterviewTopicEntity{{Type: "hashtag", Offset: 3, Length: 5}})
	if err != nil || len(entities) != 1 {
		t.Fatal("native hashtag not accepted", err)
	}
	tag, ok := entities[0].(*tg.MessageEntityHashtag)
	if !ok || tag.Offset != 3 || tag.Length != 5 {
		t.Fatal("hashtag type or UTF-16 offsets changed", entities)
	}
	if _, err := topicEntities("🎙 #тест", []InterviewTopicEntity{{Type: "hashtag", Offset: 1, Length: 1}}); err == nil {
		t.Fatal("hashtag range split an emoji")
	}
}
