package mtproto

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

type replaceVideoFake struct {
	*publishFake
	uploads, edits           int
	failWrite, mutateOnError bool
	afterUpload              func()
}

func (f *replaceVideoFake) replaceVideoUpload(_ context.Context, r InterviewTopicVideoReplacement) (tg.InputMediaClass, error) {
	f.uploads++
	if f.afterUpload != nil {
		f.afterUpload()
	}
	v := r.NewVideo
	return &tg.InputMediaUploadedDocument{MimeType: v.MIME, Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: v.Filename}, &tg.DocumentAttributeVideo{Duration: v.Duration, W: v.Width, H: v.Height, Nosound: !v.HasAudio, SupportsStreaming: true}}}, nil
}
func (f *replaceVideoFake) replaceVideoEdit(_ context.Context, r InterviewTopicVideoReplacement, media tg.InputMediaClass) error {
	f.edits++
	if !f.failWrite || f.mutateOnError {
		doc := media.(*tg.InputMediaUploadedDocument)
		m := f.messages[r.ID]
		m.Message = r.NewCaption
		m.Entities, _ = topicEntities(r.NewCaption, r.NewEntities)
		m.Media = &tg.MessageMediaDocument{Document: &tg.Document{ID: 900 + int64(r.ID), MimeType: doc.MimeType, Size: r.NewVideo.Size, Attributes: doc.Attributes}}
	}
	if f.failWrite {
		return errors.New("connection lost")
	}
	return nil
}
func localReplacementVideo(t *testing.T) InterviewTopicFile {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " unavailable")
		}
	}
	path := filepath.Join(t.TempDir(), "screening.mp4")
	args := []string{"-v", "error", "-f", "lavfi", "-i", "color=size=64x64:rate=4:duration=1", "-f", "lavfi", "-i", "sine=frequency=1000:duration=1", "-c:v", "libx264", "-c:a", "aac", "-movflags", "+faststart", "-shortest", "-y", path}
	if b, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %s: %v", b, err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return InterviewTopicFile{Path: path, Filename: "screening.mp4", Kind: "video", MIME: "video/mp4", Size: int64(len(b)), SHA256: topicTextHash(string(b)), Duration: 1, Width: 64, Height: 64, HasAudio: true}
}
func replacementFixture(video InterviewTopicFile) (InterviewTopicReplaceVideoManifest, *replaceVideoFake) {
	f := &replaceVideoFake{publishFake: &publishFake{messages: map[int]*tg.Message{}}}
	m := InterviewTopicReplaceVideoManifest{Profile: "main", AccountID: InterviewTopicAccountID, ChannelID: InterviewTopicChannelID, TopicID: InterviewTopicID}
	for _, pair := range [][2]int{{2362, 2361}, {2365, 2364}} {
		id, parent := pair[0], pair[1]
		f.messages[parent] = ownTopicMessage(parent, "Parent")
		audio := ownTopicMessage(id, "Audio")
		audio.ReplyTo.(*tg.MessageReplyHeader).SetReplyToMsgID(parent)
		audio.Media = &tg.MessageMediaDocument{Document: &tg.Document{ID: int64(id), MimeType: "audio/mp4", Size: 100, Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "screening.m4a"}, &tg.DocumentAttributeAudio{Duration: 1}}}}
		audioMedia := audio.Media.(*tg.MessageMediaDocument)
		audioMedia.SetDocument(audioMedia.Document)
		f.messages[id] = audio
		f.messages[id+1] = ownTopicMessage(id+1, "MD remains")
		m.Replacements = append(m.Replacements, InterviewTopicVideoReplacement{ID: id, ParentID: parent, ParentTextHash: topicTextHash("Parent"), ExpectedCaptionHash: topicTextHash("Audio"), ExpectedMediaID: "document:" + fmtID(id), OldAudio: InterviewTopicOldAudio{Filename: "screening.m4a", MIME: "audio/mp4", Size: 100, Duration: 1}, NewCaption: "Видеозапись", NewVideo: video})
	}
	return m, f
}
func fmtID(id int) string { return fmt.Sprint(id) }
func TestInterviewTopicReplaceVideoDryRunApplyRepeat(t *testing.T) {
	m, f := replacementFixture(localReplacementVideo(t))
	j := topicPublishJournal{}
	ctx := context.Background()
	saves := 0
	save := func() error { saves++; return nil }
	r, err := replaceInterviewTopicVideo(ctx, f, m, j, save, false)
	if err != nil || !r.Ready || r.Complete || f.uploads != 0 || f.edits != 0 || saves != 0 {
		t.Fatal(r, err, f.uploads, f.edits, saves)
	}
	r, err = replaceInterviewTopicVideo(ctx, f, m, j, save, true)
	if err != nil || !r.Complete || f.edits != 2 || f.sends != 0 {
		t.Fatal(r, err, f.edits, f.sends)
	}
	r, err = replaceInterviewTopicVideo(ctx, f, m, j, save, true)
	if err != nil || !r.Complete || f.edits != 2 || f.uploads != 2 || !r.Rows[0].AlreadyApplied {
		t.Fatal(r, err, f.edits, f.uploads)
	}
	for _, id := range []int{2363, 2366} {
		if f.messages[id].Message != "MD remains" {
			t.Fatal("MD changed")
		}
	}
}
func TestInterviewTopicReplaceVideoGuards(t *testing.T) {
	video := localReplacementVideo(t)
	for _, kind := range []string{"id", "parent", "caption", "media", "filename", "mime", "size", "duration", "album", "account", "localhash", "second-old"} {
		t.Run(kind, func(t *testing.T) {
			m, f := replacementFixture(video)
			r := &m.Replacements[0]
			switch kind {
			case "id":
				r.ID = 2363
			case "parent":
				r.ParentID = 2364
			case "caption":
				r.ExpectedCaptionHash = topicTextHash("changed")
			case "media":
				r.ExpectedMediaID = "document:999"
			case "filename":
				r.OldAudio.Filename = "wrong.m4a"
			case "mime":
				r.OldAudio.MIME = "audio/mpeg"
			case "size":
				r.OldAudio.Size++
			case "duration":
				r.OldAudio.Duration++
			case "album":
				f.messages[r.ID].GroupedID = 10
			case "account":
				f.account = 123
			case "localhash":
				r.NewVideo.SHA256 = topicTextHash("changed")
			case "second-old":
				f.messages[2365].Message = "changed"
			}
			_, err := replaceInterviewTopicVideo(context.Background(), f, m, topicPublishJournal{}, func() error { return nil }, true)
			if err == nil || f.uploads != 0 || f.edits != 0 {
				t.Fatal("invalid preflight mutated", err, f.uploads, f.edits)
			}
		})
	}
}
func TestInterviewTopicReplaceVideoUnknownAndRecovery(t *testing.T) {
	video := localReplacementVideo(t)
	for _, mutate := range []bool{false, true} {
		t.Run(fmt.Sprint(mutate), func(t *testing.T) {
			m, f := replacementFixture(video)
			m.Replacements = m.Replacements[:1]
			f.failWrite = true
			f.mutateOnError = mutate
			j := topicPublishJournal{}
			r, err := replaceInterviewTopicVideo(context.Background(), f, m, j, func() error { return nil }, true)
			if mutate {
				if err != nil || !r.Complete {
					t.Fatal(r, err)
				}
			} else {
				if err == nil || len(r.Rows) == 0 || !r.Rows[0].OutcomeUnknown {
					t.Fatal(r, err)
				}
				_, err = replaceInterviewTopicVideo(context.Background(), f, m, j, func() error { return nil }, true)
				if err == nil || f.edits != 1 {
					t.Fatal("unknown outcome retried", err, f.edits)
				}
			}
		})
	}
}
func TestInterviewTopicReplaceVideoRechecksAfterUpload(t *testing.T) {
	m, f := replacementFixture(localReplacementVideo(t))
	f.afterUpload = func() { f.messages[2362].Message = "changed during upload" }
	_, err := replaceInterviewTopicVideo(context.Background(), f, m, topicPublishJournal{}, func() error { return nil }, true)
	if err == nil || f.edits != 0 {
		t.Fatal(err, f.edits)
	}
}

type replaceWire struct {
	t     *testing.T
	calls int
}

func (f *replaceWire) Invoke(_ context.Context, input bin.Encoder, _ bin.Decoder) error {
	r, ok := input.(*tg.MessagesEditMessageRequest)
	if !ok {
		f.t.Fatalf("unexpected RPC %T", input)
	}
	peer, ok := r.Peer.(*tg.InputPeerChannel)
	if !ok || peer.ChannelID != InterviewTopicChannelID || r.ID != 2362 || r.Message != "Видеозапись" || r.Media == nil || r.ReplyMarkup != nil || r.RichMessage != nil || r.ScheduleDate != 0 {
		f.t.Fatal("edit exceeds scope", r)
	}
	f.calls++
	return errors.New("FLOOD_WAIT_1")
}
func TestInterviewTopicReplaceVideoWireEditsExactlyOnce(t *testing.T) {
	fake := &replaceWire{t: t}
	s := &interviewTopicSession{Session: &Session{raw: tg.NewClient(fake)}, target: resolvedTarget{InputPeer: &tg.InputPeerChannel{ChannelID: InterviewTopicChannelID}}}
	r := InterviewTopicVideoReplacement{ID: 2362, NewCaption: "Видеозапись"}
	err := s.replaceVideoEdit(context.Background(), r, &tg.InputMediaUploadedDocument{})
	if err == nil || fake.calls != 1 {
		t.Fatal(err, fake.calls)
	}
	r.ID = 999
	if err = s.replaceVideoEdit(context.Background(), r, &tg.InputMediaUploadedDocument{}); err == nil || fake.calls != 1 {
		t.Fatal("unscoped edit reached transport")
	}
}

func TestInterviewTopicReplaceVideoRecoversPendingNewWithoutMutation(t *testing.T) {
	m, f := replacementFixture(localReplacementVideo(t))
	m.Replacements = m.Replacements[:1]
	j := topicPublishJournal{}
	ctx := context.Background()
	if _, err := replaceInterviewTopicVideo(ctx, f, m, j, func() error { return nil }, true); err != nil {
		t.Fatal(err)
	}
	key := m.Replacements[0].newPost().Key
	receipt := j[key]
	receipt.Pending = true
	j[key] = receipt
	r, err := replaceInterviewTopicVideo(ctx, f, m, j, func() error { return nil }, true)
	if err != nil || !r.Complete || !r.Rows[0].AlreadyApplied || j[key].Pending || f.edits != 1 || f.uploads != 1 {
		t.Fatal(r, err, j, f.edits, f.uploads)
	}
}

func TestInterviewTopicReplaceVideoRejectsQuickTimeRenamedMP4(t *testing.T) {
	video := localReplacementVideo(t)
	path := filepath.Join(t.TempDir(), "renamed.mp4")
	if b, err := exec.Command("ffmpeg", "-v", "error", "-i", video.Path, "-c", "copy", "-f", "mov", "-y", path).CombinedOutput(); err != nil {
		t.Fatalf("MOV fixture: %s: %v", b, err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	video.Path = path
	video.Filename = "renamed.mp4"
	video.Size = int64(len(b))
	video.SHA256 = topicTextHash(string(b))
	if err = verifyPublishFile(context.Background(), &video); err == nil || !strings.Contains(err.Error(), "major_brand") {
		t.Fatal("QuickTime falsely declared as MP4 passed", err)
	}
}
