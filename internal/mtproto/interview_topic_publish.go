package mtproto

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/unpack"
	"github.com/gotd/td/tg"
)

// This is a separate fixed-destination operation; SendSaved remains self-only.
type InterviewTopicPublishManifest struct {
	Profile   string               `json:"profile"`
	AccountID int64                `json:"account_id"`
	ChannelID int64                `json:"channel_id"`
	TopicID   int                  `json:"topic_id"`
	Posts     []InterviewTopicPost `json:"posts"`
}
type InterviewTopicPost struct {
	Key            string                 `json:"key"`
	Text           string                 `json:"text"`
	Entities       []InterviewTopicEntity `json:"entities,omitempty"`
	ParentID       int                    `json:"parent_id,omitempty"`
	ParentTextHash string                 `json:"parent_text_hash,omitempty"`
	ParentKey      string                 `json:"parent_key,omitempty"`
	File           *InterviewTopicFile    `json:"file,omitempty"`
}
type InterviewTopicFile struct {
	Path     string  `json:"path"`
	SHA256   string  `json:"sha256"`
	Kind     string  `json:"kind"`
	Filename string  `json:"filename"`
	MIME     string  `json:"mime"`
	Size     int64   `json:"size"`
	Duration float64 `json:"duration,omitempty"`
	Width    int     `json:"width,omitempty"`
	Height   int     `json:"height,omitempty"`
	HasAudio bool    `json:"has_audio,omitempty"`
}
type InterviewTopicPublishRow struct {
	Key            string `json:"key"`
	ID             int    `json:"id,omitempty"`
	ServerVerified bool   `json:"server_verified"`
	AlreadyApplied bool   `json:"already_applied"`
	OutcomeUnknown bool   `json:"outcome_unknown"`
}
type InterviewTopicPublishResult struct {
	DryRun   bool                       `json:"dry_run"`
	Ready    bool                       `json:"ready"`
	Complete bool                       `json:"complete"`
	Rows     []InterviewTopicPublishRow `json:"rows"`
}
type topicPublishReceipt struct {
	Hash    string `json:"hash"`
	ID      int    `json:"id,omitempty"`
	Pending bool   `json:"pending"`
}
type topicPublishJournal map[string]topicPublishReceipt

func publishPostHash(p InterviewTopicPost) string {
	b, _ := json.Marshal(p)
	return topicTextHash(string(b))
}
func publishRandomID(p InterviewTopicPost) int64 {
	h := sha256.Sum256([]byte("interview-topic-publish/main/2621478426/115/" + p.Key))
	n := int64(binary.BigEndian.Uint64(h[:8]) & math.MaxInt64)
	if n == 0 {
		return 1
	}
	return n
}
func ValidateInterviewTopicPublishManifest(m InterviewTopicPublishManifest) error {
	if m.Profile != "main" || m.AccountID != InterviewTopicAccountID || m.ChannelID != InterviewTopicChannelID || m.TopicID != InterviewTopicID {
		return fmt.Errorf("publish is bound to main/@Pheik13 and fixed interview topic")
	}
	if len(m.Posts) < 1 || len(m.Posts) > 20 {
		return fmt.Errorf("provide 1..20 ordered posts")
	}
	seen := map[string]bool{}
	roots := map[string]bool{}
	parents := 0
	for _, p := range m.Posts {
		if p.Key == "" || len(p.Key) > 100 || strings.ContainsAny(p.Key, "/\\ \t\r\n") || seen[p.Key] {
			return fmt.Errorf("invalid or duplicate post key")
		}
		limit := 4096
		if p.File != nil {
			limit = 1024
		}
		if !utf8.ValidString(p.Text) || strings.TrimSpace(p.Text) == "" || len(utf16.Encode([]rune(p.Text))) > limit {
			return fmt.Errorf("invalid post text/caption")
		}
		if _, err := topicEntities(p.Text, p.Entities); err != nil {
			return err
		}
		if p.ParentKey != "" && (!roots[p.ParentKey] || p.ParentID != 0 || p.ParentTextHash != "") {
			return fmt.Errorf("parent_key must reference an earlier parent, exclusively")
		}
		if p.ParentID != 0 && (p.ParentID <= InterviewTopicID || !validSHA256(p.ParentTextHash)) {
			return fmt.Errorf("existing parent requires exact ID and text hash")
		}
		if p.ParentID == 0 && p.ParentTextHash != "" {
			return fmt.Errorf("parent hash requires parent ID")
		}
		if p.ParentKey == "" && p.ParentID == 0 {
			roots[p.Key] = true
			parents++
			if p.File != nil {
				return fmt.Errorf("new parent must be a text post")
			}
		}
		if p.File != nil {
			f := p.File
			if !filepath.IsAbs(f.Path) || !validSHA256(f.SHA256) || f.Size <= 0 || f.Filename != filepath.Base(f.Path) || f.Filename != filepath.Base(f.Filename) {
				return fmt.Errorf("file requires absolute path, exact basename, size and SHA-256")
			}
			switch f.Kind {
			case "document":
				ext := strings.ToLower(filepath.Ext(f.Filename))
				if !(ext == ".md" && f.MIME == "text/markdown" || ext == ".pdf" && f.MIME == "application/pdf") || f.Duration != 0 || f.Width != 0 || f.Height != 0 || f.HasAudio {
					return fmt.Errorf("documents are MD/PDF only")
				}
			case "audio":
				ext := strings.ToLower(filepath.Ext(f.Filename))
				if !(f.MIME == "audio/mp4" && ext == ".m4a" || f.MIME == "audio/mpeg" && ext == ".mp3") || f.Duration < 1 || f.Duration != math.Trunc(f.Duration) || !f.HasAudio || f.Width != 0 || f.Height != 0 {
					return fmt.Errorf("audio requires M4A/audio-mp4 or MP3/audio-mpeg, integral duration and an audio stream")
				}
			case "video":
				if f.MIME != "video/mp4" || f.Duration <= 0 || f.Width <= 0 || f.Height <= 0 {
					return fmt.Errorf("video requires MP4, duration and dimensions")
				}
			default:
				return fmt.Errorf("unsupported media kind")
			}
		}
		seen[p.Key] = true
	}
	if parents > 2 {
		return fmt.Errorf("at most two new parent posts")
	}
	return nil
}

// Probe uses local ffprobe only, without invoking ASR or Telegram.
func verifyPublishFile(ctx context.Context, f *InterviewTopicFile) error {
	if f == nil {
		return nil
	}
	file, err := os.Open(f.Path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != f.Size {
		return fmt.Errorf("local file size/type changed")
	}
	h := sha256.New()
	if _, err = io.Copy(h, file); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != f.SHA256 {
		return fmt.Errorf("local SHA-256 mismatch")
	}
	if f.Kind == "document" {
		if f.MIME == "application/pdf" {
			b := make([]byte, 5)
			if _, err = file.ReadAt(b, 0); err != nil || string(b) != "%PDF-" {
				return fmt.Errorf("invalid PDF header")
			}
		}
		return nil
	}
	var probe struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
		Format struct {
			Duration   string `json:"duration"`
			FormatName string `json:"format_name"`
		} `json:"format"`
	}
	b, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "stream=codec_type,width,height:format=duration,format_name", "-of", "json", f.Path).Output()
	if err != nil {
		return fmt.Errorf("ffprobe failed: %w", err)
	}
	if err = json.Unmarshal(b, &probe); err != nil {
		return err
	}
	var duration float64
	if _, err = fmt.Sscan(probe.Format.Duration, &duration); err != nil {
		return err
	}
	hasAudio := false
	w, hgt := 0, 0
	for _, s := range probe.Streams {
		if s.CodecType == "audio" {
			hasAudio = true
		}
		if s.CodecType == "video" {
			w = s.Width
			hgt = s.Height
		}
	}
	expectedDuration := duration
	if f.Kind == "audio" {
		expectedDuration = math.Ceil(duration)
	}
	if math.Abs(expectedDuration-f.Duration) > 0.1 || hasAudio != f.HasAudio || w != f.Width || hgt != f.Height {
		return fmt.Errorf("local media streams/duration differ from manifest")
	}
	if f.Kind == "audio" {
		formatOK := f.MIME == "audio/mp4" && strings.Contains(","+probe.Format.FormatName+",", ",m4a,") || f.MIME == "audio/mpeg" && probe.Format.FormatName == "mp3"
		if !formatOK {
			return fmt.Errorf("audio container differs from declared MIME")
		}
	}
	if f.Kind == "video" && !strings.Contains(probe.Format.FormatName, "mp4") {
		return fmt.Errorf("video is not MP4")
	}
	return nil
}

type topicPublishSource interface {
	SelfProfile(context.Context) (harvest.SelfProfile, error)
	interviewTopicPrepare(context.Context) error
	interviewTopicRead(context.Context, int) (*tg.Message, error)
	publishExisting(context.Context, InterviewTopicPost, int) (bool, error)
	publishSend(context.Context, InterviewTopicPost, int, string, int64) (int, error)
}

func verifyPublishedPost(m *tg.Message, p InterviewTopicPost, parent int) error {
	if m == nil {
		return fmt.Errorf("published message missing")
	}
	if err := validateTopicMessage(m, m.ID); err != nil {
		return err
	}
	reply, _ := replyInfo(m.ReplyTo)
	if reply != parent {
		return fmt.Errorf("reply parent mismatch")
	}
	entities, err := topicEntities(p.Text, p.Entities)
	if err != nil {
		return err
	}
	if m.Message != p.Text || !sameTopicEntities(m.Entities, entities) {
		return fmt.Errorf("text/entities mismatch")
	}
	if p.File == nil {
		if m.Media != nil {
			if _, empty := m.Media.(*tg.MessageMediaEmpty); empty {
				return nil
			}
			return fmt.Errorf("unexpected media")
		}
		return nil
	}
	media, ok := m.Media.(*tg.MessageMediaDocument)
	if !ok {
		return fmt.Errorf("document media missing")
	}
	d, ok := media.Document.(*tg.Document)
	if !ok {
		return fmt.Errorf("document missing")
	}
	f := p.File
	if d.MimeType != f.MIME || d.Size != f.Size || documentFileName(d) != f.Filename {
		return fmt.Errorf("filename/MIME/size mismatch")
	}
	audio, video := false, false
	for _, a := range d.Attributes {
		switch a := a.(type) {
		case *tg.DocumentAttributeAudio:
			audio = true
			if f.Kind != "audio" || a.Voice || float64(a.Duration) != f.Duration {
				return fmt.Errorf("audio attributes mismatch")
			}
		case *tg.DocumentAttributeVideo:
			video = true
			if f.Kind != "video" || a.RoundMessage || !a.SupportsStreaming || a.Nosound == f.HasAudio || math.Abs(a.Duration-f.Duration) > 0.1 || a.W != f.Width || a.H != f.Height {
				return fmt.Errorf("video attributes mismatch")
			}
		}
	}
	if audio != (f.Kind == "audio") || video != (f.Kind == "video") {
		return fmt.Errorf("playable media kind mismatch")
	}
	return nil
}
func checkPublishIdentity(ctx context.Context, s topicPublishSource) error {
	self, err := s.SelfProfile(ctx)
	if err != nil {
		return err
	}
	if self.ID != InterviewTopicAccountID || !strings.EqualFold(self.Username, SavedMessagesMainUsername) {
		return fmt.Errorf("active account differs from main/@Pheik13")
	}
	return s.interviewTopicPrepare(ctx)
}
func publishParent(ctx context.Context, s topicPublishSource, p InterviewTopicPost, ids map[string]int, posts map[string]InterviewTopicPost) (int, error) {
	if p.ParentKey != "" {
		id := ids[p.ParentKey]
		if id == 0 {
			return 0, nil
		}
		parent, err := s.interviewTopicRead(ctx, id)
		if err != nil {
			return 0, err
		}
		if err = verifyPublishedPost(parent, posts[p.ParentKey], InterviewTopicID); err != nil {
			return 0, err
		}
		return id, nil
	}
	if p.ParentID == 0 {
		return InterviewTopicID, nil
	}
	parent, err := s.interviewTopicRead(ctx, p.ParentID)
	if err != nil {
		return 0, err
	}
	if err = validateTopicMessage(parent, p.ParentID); err != nil {
		return 0, err
	}
	if topicTextHash(parent.Message) != p.ParentTextHash {
		return 0, fmt.Errorf("existing parent text changed")
	}
	return p.ParentID, nil
}
func publishInterviewTopic(ctx context.Context, s topicPublishSource, m InterviewTopicPublishManifest, j topicPublishJournal, save func() error, apply bool) (InterviewTopicPublishResult, error) {
	result := InterviewTopicPublishResult{DryRun: !apply, Rows: []InterviewTopicPublishRow{}}
	if err := ValidateInterviewTopicPublishManifest(m); err != nil {
		return result, err
	}
	if err := checkPublishIdentity(ctx, s); err != nil {
		return result, err
	}
	ids := map[string]int{}
	posts := map[string]InterviewTopicPost{}
	for _, p := range m.Posts {
		posts[p.Key] = p
	}
	// All local inputs and existing parents must pass before the first send.
	for _, p := range m.Posts {
		if err := verifyPublishFile(ctx, p.File); err != nil {
			return result, fmt.Errorf("%s: %w", p.Key, err)
		}
		parent, err := publishParent(ctx, s, p, ids, posts)
		if err != nil {
			return result, err
		}
		r, ok := j[p.Key]
		if ok {
			if r.Hash != publishPostHash(p) {
				return result, fmt.Errorf("post key payload changed: %s", p.Key)
			}
			if r.ID == 0 {
				return result, fmt.Errorf("outcome_unknown for %s; reconcile server manually; do not retry", p.Key)
			}
			msg, readErr := s.interviewTopicRead(ctx, r.ID)
			if readErr != nil {
				return result, readErr
			}
			if parent == 0 {
				return result, fmt.Errorf("receipt has no confirmed parent")
			}
			if readErr = verifyPublishedPost(msg, p, parent); readErr != nil {
				return result, readErr
			}
			ids[p.Key] = r.ID
		} else if parent != 0 {
			exists, searchErr := s.publishExisting(ctx, p, parent)
			if searchErr != nil {
				return result, searchErr
			}
			if exists {
				return result, fmt.Errorf("matching server post exists for %s without receipt; reconcile before sending", p.Key)
			}
		}
	}
	for _, p := range m.Posts {
		parent, err := publishParent(ctx, s, p, ids, posts)
		if err != nil {
			return result, err
		}
		r, known := j[p.Key]
		row := InterviewTopicPublishRow{Key: p.Key, ID: r.ID}
		if known {
			msg, err := s.interviewTopicRead(ctx, r.ID)
			if err != nil {
				return result, err
			}
			if err = verifyPublishedPost(msg, p, parent); err != nil {
				return result, err
			}
			if apply && r.Pending {
				r.Pending = false
				j[p.Key] = r
				if err = save(); err != nil {
					return result, err
				}
			}
			row.AlreadyApplied = true
			row.ServerVerified = true
			result.Rows = append(result.Rows, row)
			continue
		}
		if parent != 0 {
			exists, err := s.publishExisting(ctx, p, parent)
			if err != nil {
				return result, err
			}
			if exists {
				return result, fmt.Errorf("matching server post exists for %s without receipt; reconcile before sending", p.Key)
			}
		}
		if !apply {
			result.Rows = append(result.Rows, row)
			continue
		}
		if parent == 0 {
			return result, fmt.Errorf("new parent ID missing")
		}
		if err = checkPublishIdentity(ctx, s); err != nil {
			return result, err
		}
		if _, err = publishParent(ctx, s, p, ids, posts); err != nil {
			return result, err
		}
		if err = verifyPublishFile(ctx, p.File); err != nil {
			return result, err
		}
		j[p.Key] = topicPublishReceipt{Hash: publishPostHash(p), Pending: true}
		if err = save(); err != nil {
			return result, err
		}
		parentHash := p.ParentTextHash
		if p.ParentKey != "" {
			parentHash = topicTextHash(posts[p.ParentKey].Text)
		}
		id, sendErr := s.publishSend(ctx, p, parent, parentHash, publishRandomID(p))
		row.ID = id
		row.OutcomeUnknown = true
		if id > 0 {
			j[p.Key] = topicPublishReceipt{Hash: publishPostHash(p), ID: id, Pending: true}
			if err = save(); err != nil {
				result.Rows = append(result.Rows, row)
				return result, err
			}
			msg, readErr := s.interviewTopicRead(ctx, id)
			if readErr == nil {
				readErr = verifyPublishedPost(msg, p, parent)
			}
			if readErr == nil {
				row.OutcomeUnknown = false
				row.ServerVerified = true
				j[p.Key] = topicPublishReceipt{Hash: publishPostHash(p), ID: id}
				if err = save(); err != nil {
					result.Rows = append(result.Rows, row)
					return result, err
				}
				ids[p.Key] = id
			} else {
				sendErr = errors.Join(sendErr, readErr)
			}
		}
		result.Rows = append(result.Rows, row)
		if !row.ServerVerified {
			return result, fmt.Errorf("outcome_unknown for %s; no retry: %w", p.Key, sendErr)
		}
	}
	result.Ready = true
	result.Complete = apply
	return result, nil
}

// PublishInterviewTopic requires a private durable journal beside the reviewed manifest.
func (s *Session) PublishInterviewTopic(ctx context.Context, m InterviewTopicPublishManifest, journalPath string, apply bool) (InterviewTopicPublishResult, error) {
	j := topicPublishJournal{}
	f, err := os.Open(journalPath)
	if err == nil {
		defer f.Close()
		info, statErr := f.Stat()
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 1<<20 {
			return InterviewTopicPublishResult{}, fmt.Errorf("journal must be private regular file, at most 1 MiB")
		}
		b, readErr := io.ReadAll(io.LimitReader(f, (1<<20)+1))
		if readErr != nil {
			return InterviewTopicPublishResult{}, readErr
		}
		decoder := json.NewDecoder(bytes.NewReader(b))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&j); err != nil {
			return InterviewTopicPublishResult{}, err
		}
		var extra any
		if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) || j == nil {
			return InterviewTopicPublishResult{}, fmt.Errorf("journal must contain one non-null object")
		}
		for _, receipt := range j {
			if !validSHA256(receipt.Hash) || receipt.ID < 0 || receipt.ID > 0 && receipt.ID <= InterviewTopicID {
				return InterviewTopicPublishResult{}, fmt.Errorf("invalid receipt")
			}
		}
	} else if !os.IsNotExist(err) {
		return InterviewTopicPublishResult{}, err
	}

	save := func() error { return savePublishJournal(journalPath, j) }
	return publishInterviewTopic(ctx, &interviewTopicSession{Session: s}, m, j, save, apply)
}
func savePublishJournal(path string, j topicPublishJournal) error {
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".publish-journal-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(temp, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (s *interviewTopicSession) publishExisting(ctx context.Context, p InterviewTopicPost, parent int) (bool, error) {
	// Search exact text within this topic and own sender, paging until exhausted.
	offset := 0
	for {
		var result tg.MessagesMessagesClass
		req := &tg.MessagesSearchRequest{Peer: s.target.InputPeer, Q: p.Text, Filter: &tg.InputMessagesFilterEmpty{}, Limit: 100, OffsetID: offset}
		req.SetFromID(&tg.InputPeerSelf{})
		req.SetTopMsgID(InterviewTopicID)
		err := s.performRPC(ctx, "check_existing_interview_post", func(ctx context.Context) error { var e error; result, e = s.raw.MessagesSearch(ctx, req); return e })
		if err != nil {
			return false, err
		}
		messages := historyMessages(result)
		if len(messages) == 0 {
			return false, nil
		}
		next := 0
		for _, mc := range messages {
			if msg, ok := mc.(*tg.Message); ok {
				if next == 0 || msg.ID < next {
					next = msg.ID
				}
				if verifyPublishedPost(msg, p, parent) == nil {
					return true, nil
				}
			}
		}
		if next == 0 || offset != 0 && next >= offset {
			return false, fmt.Errorf("search pagination did not advance")
		}
		offset = next
	}
}
func (s *interviewTopicSession) publishSend(ctx context.Context, p InterviewTopicPost, parent int, parentHash string, randomID int64) (int, error) {
	reply := &tg.InputReplyToMessage{ReplyToMsgID: parent}
	reply.SetTopMsgID(InterviewTopicID)
	entities, err := topicEntities(p.Text, p.Entities)
	if err != nil {
		return 0, err
	}
	var media tg.InputMediaClass
	if p.File != nil {
		f := p.File
		uploaded, e := message.NewSender(s.raw).Self().Upload(message.FromPath(f.Path)).AsInputFile(ctx)
		if e != nil {
			return 0, e
		}
		if e = verifyPublishFile(ctx, f); e != nil {
			return 0, e
		}
		attrs := []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: f.Filename}}
		switch f.Kind {
		case "audio":
			attrs = append(attrs, &tg.DocumentAttributeAudio{Duration: int(f.Duration)})
		case "video":
			attrs = append(attrs, &tg.DocumentAttributeVideo{Duration: f.Duration, W: f.Width, H: f.Height, SupportsStreaming: true, Nosound: !f.HasAudio})
		}
		media = &tg.InputMediaUploadedDocument{File: uploaded, MimeType: f.MIME, Attributes: attrs, NosoundVideo: f.Kind == "video" && !f.HasAudio}
	}
	if err = checkPublishIdentity(ctx, s); err != nil {
		return 0, err
	}
	if parent != InterviewTopicID {
		parentMessage, readErr := s.interviewTopicRead(ctx, parent)
		if readErr != nil {
			return 0, readErr
		}
		if readErr = validateTopicMessage(parentMessage, parent); readErr != nil {
			return 0, readErr
		}
		if topicTextHash(parentMessage.Message) != parentHash {
			return 0, fmt.Errorf("parent changed during upload")
		}
	}
	// Pacing is shared, but this mutation is deliberately attempted exactly once.
	if err = s.beforeRPC(ctx, "publish_interview_post"); err != nil {
		return 0, err
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultRPCTimeout)
	defer cancel()
	var updates tg.UpdatesClass
	if media == nil {
		updates, err = s.raw.MessagesSendMessage(callCtx, &tg.MessagesSendMessageRequest{Peer: s.target.InputPeer, ReplyTo: reply, Message: p.Text, Entities: entities, RandomID: randomID, NoWebpage: true})
	} else {
		updates, err = s.raw.MessagesSendMedia(callCtx, &tg.MessagesSendMediaRequest{Peer: s.target.InputPeer, ReplyTo: reply, Message: p.Text, Entities: entities, RandomID: randomID, Media: media})
	}
	if err != nil {
		return 0, err
	}
	id, err := unpack.MessageID(updates, nil)
	return id, err
}
