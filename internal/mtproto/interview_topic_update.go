package mtproto

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

const (
	InterviewTopicAccountID = int64(1266978055)
	InterviewTopicChannelID = int64(2621478426)
	InterviewTopicID        = 115
)

type InterviewTopicEntity struct {
	Type     string `json:"type"`
	Offset   int    `json:"offset"`
	Length   int    `json:"length"`
	URL      string `json:"url,omitempty"`
	Language string `json:"language,omitempty"`
}

type InterviewTopicEdit struct {
	ID               int                     `json:"id"`
	ExpectedTextHash string                  `json:"expected_text_hash"`
	NewText          string                  `json:"new_text"`
	ExpectedMediaID  string                  `json:"expected_media_id,omitempty"`
	NewEntities      *[]InterviewTopicEntity `json:"new_entities,omitempty"`
}

type InterviewTopicDeleteZIP struct {
	ID               int    `json:"id"`
	ExpectedTextHash string `json:"expected_text_hash"`
	ExpectedMediaID  string `json:"expected_media_id"`
	Filename         string `json:"filename"`
	LocalBackupPath  string `json:"local_backup_path"`
	SHA256           string `json:"sha256"`
}

type InterviewTopicUpdateManifest struct {
	Profile   string                    `json:"profile"`
	AccountID int64                     `json:"account_id"`
	ChannelID int64                     `json:"channel_id"`
	TopicID   int                       `json:"topic_id"`
	Edits     []InterviewTopicEdit      `json:"edits"`
	DeleteZIP []InterviewTopicDeleteZIP `json:"delete_zip"`
}

type InterviewTopicUpdateRow struct {
	ID                    int      `json:"id"`
	Action                string   `json:"action"`
	AlreadyApplied        bool     `json:"already_applied"`
	ServerVerified        bool     `json:"server_verified"`
	OutcomeUnknown        bool     `json:"outcome_unknown"`
	Error                 string   `json:"error,omitempty"`
	ProtectedChangedPaths []string `json:"protected_changed_paths,omitempty"`
	SourceEntityTypes     []string `json:"source_entity_types,omitempty"`
}

type InterviewTopicUpdateResult struct {
	AccountID int64                     `json:"account_id"`
	ChannelID int64                     `json:"channel_id"`
	TopicID   int                       `json:"topic_id"`
	DryRun    bool                      `json:"dry_run"`
	Ready     bool                      `json:"ready"`
	Complete  bool                      `json:"complete"`
	Rows      []InterviewTopicUpdateRow `json:"rows"`
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func topicTextHash(text string) string {
	hash := sha256.Sum256([]byte(text))
	return hex.EncodeToString(hash[:])
}

func topicEntities(text string, specs []InterviewTopicEntity) ([]tg.MessageEntityClass, error) {
	boundaries := map[int]bool{0: true}
	position := 0
	for _, r := range text {
		position += len(utf16.Encode([]rune{r}))
		boundaries[position] = true
	}
	entities := []tg.MessageEntityClass{}
	for _, e := range specs {
		if e.Length <= 0 || !boundaries[e.Offset] || !boundaries[e.Offset+e.Length] {
			return nil, fmt.Errorf("entity offset/length must use valid UTF-16 boundaries")
		}
		if e.URL != "" && e.Type != "text_url" || e.Language != "" && e.Type != "pre" {
			return nil, fmt.Errorf("entity URL/language does not match its type")
		}
		switch e.Type {
		case "bold":
			entities = append(entities, &tg.MessageEntityBold{Offset: e.Offset, Length: e.Length})
		case "italic":
			entities = append(entities, &tg.MessageEntityItalic{Offset: e.Offset, Length: e.Length})
		case "underline":
			entities = append(entities, &tg.MessageEntityUnderline{Offset: e.Offset, Length: e.Length})
		case "strike":
			entities = append(entities, &tg.MessageEntityStrike{Offset: e.Offset, Length: e.Length})
		case "code":
			entities = append(entities, &tg.MessageEntityCode{Offset: e.Offset, Length: e.Length})
		case "pre":
			entities = append(entities, &tg.MessageEntityPre{Offset: e.Offset, Length: e.Length, Language: e.Language})
		case "url":
			entities = append(entities, &tg.MessageEntityURL{Offset: e.Offset, Length: e.Length})
		case "hashtag":
			entities = append(entities, &tg.MessageEntityHashtag{Offset: e.Offset, Length: e.Length})
		case "text_url":
			parsed, err := url.Parse(e.URL)
			if err != nil || parsed.Host == "" || parsed.Scheme != "https" && parsed.Scheme != "http" {
				return nil, fmt.Errorf("text_url must contain an explicit HTTP(S) URL")
			}
			entities = append(entities, &tg.MessageEntityTextURL{Offset: e.Offset, Length: e.Length, URL: e.URL})
		default:
			return nil, fmt.Errorf("unsupported interview topic entity type %q", e.Type)
		}
	}
	return entities, nil
}

func ValidateInterviewTopicUpdateManifest(m InterviewTopicUpdateManifest) error {
	if m.Profile != "main" || m.AccountID != InterviewTopicAccountID || m.ChannelID != InterviewTopicChannelID || m.TopicID != InterviewTopicID {
		return fmt.Errorf("interview-topic-update is bound to main/@Pheik13, channel %d, topic %d", InterviewTopicChannelID, InterviewTopicID)
	}
	if len(m.Edits)+len(m.DeleteZIP) == 0 || len(m.Edits)+len(m.DeleteZIP) > 100 {
		return fmt.Errorf("provide 1..100 exact edit/delete_zip operations")
	}
	seen := map[int]bool{}
	for _, e := range m.Edits {
		if e.ID <= InterviewTopicID || seen[e.ID] || !validSHA256(e.ExpectedTextHash) || !utf8.ValidString(e.NewText) || strings.TrimSpace(e.NewText) == "" || len(utf16.Encode([]rune(e.NewText))) > 4096 {
			return fmt.Errorf("invalid edit ID, duplicate, text hash or new text")
		}
		if e.NewEntities != nil {
			if _, err := topicEntities(e.NewText, *e.NewEntities); err != nil {
				return err
			}
		}
		seen[e.ID] = true
	}
	for _, d := range m.DeleteZIP {
		if d.ID != 2348 && d.ID != 2352 || seen[d.ID] || !validSHA256(d.ExpectedTextHash) || !validSHA256(d.SHA256) || !strings.HasPrefix(d.ExpectedMediaID, "document:") || !strings.HasSuffix(strings.ToLower(d.Filename), ".zip") || filepath.Base(d.Filename) != d.Filename || !filepath.IsAbs(d.LocalBackupPath) {
			return fmt.Errorf("delete_zip permits only backed-up ZIP posts 2348 and 2352, with exact hashes/media/filename")
		}
		seen[d.ID] = true
	}
	return nil
}

func verifyTopicZIPBackup(d InterviewTopicDeleteZIP) (int64, error) {
	f, err := os.Open(d.LocalBackupPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return 0, fmt.Errorf("ZIP backup must be a nonempty regular local file")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return 0, err
	}
	if hex.EncodeToString(hash.Sum(nil)) != d.SHA256 {
		return 0, fmt.Errorf("ZIP backup SHA-256 differs from manifest")
	}
	if _, err := zip.NewReader(f, info.Size()); err != nil {
		return 0, fmt.Errorf("backup is not a readable ZIP: %w", err)
	}
	return info.Size(), nil
}

type interviewTopicUpdateSource interface {
	SelfProfile(context.Context) (harvest.SelfProfile, error)
	interviewTopicPrepare(context.Context) error
	interviewTopicRead(context.Context, int) (*tg.Message, error)
	interviewTopicEdit(context.Context, int, string, []tg.MessageEntityClass) error
	interviewTopicDeleteZIP(context.Context, int) error
}

func validateTopicMessage(m *tg.Message, id int) error {
	if m == nil || m.ID != id || !m.Out || !m.RichMessage.Zero() {
		return fmt.Errorf("message %d must be an existing own ordinary message", id)
	}
	from, own := m.FromID.(*tg.PeerUser)
	peer, dest := m.PeerID.(*tg.PeerChannel)
	reply, topic := m.ReplyTo.(*tg.MessageReplyHeader)
	replyID, topID := replyInfo(m.ReplyTo)
	if !own || from.UserID != InterviewTopicAccountID || !dest || peer.ChannelID != InterviewTopicChannelID || !topic || !reply.ForumTopic || topID != InterviewTopicID && !(topID == 0 && replyID == InterviewTopicID) {
		return fmt.Errorf("message %d author/destination/topic differs from the authorized target", id)
	}
	return nil
}

func topicMediaID(m *tg.Message) (string, error) {
	if _, ok := m.Media.(*tg.MessageMediaWebPage); ok {
		return "", fmt.Errorf("existing webpage previews are outside the text-edit contract")
	}
	if m.Media == nil {
		return "", nil
	}
	if _, ok := m.Media.(*tg.MessageMediaEmpty); ok {
		return "", nil
	}
	attachments := extractAttachments(m.Media)
	if len(attachments) != 1 || attachments[0].MediaID == "" {
		return "", fmt.Errorf("unsupported media; only existing exact document/photo identities can be preserved")
	}
	return attachments[0].MediaID, nil
}

func topicProtectedSnapshot(m *tg.Message) ([]byte, error) {
	if m == nil {
		return nil, fmt.Errorf("message disappeared during verification")
	}
	copy := *m
	if media, ok := m.Media.(*tg.MessageMediaDocument); ok {
		if document, ok := media.Document.(*tg.Document); ok {
			mediaCopy, documentCopy := *media, *document
			// Two read-only native lookups of the same interview post varied
			// only this temporary access reference. Preserve every other field,
			// including ID, access hash, thumbnails and all codec/audio attrs.
			documentCopy.FileReference = nil
			mediaCopy.Document = &documentCopy
			copy.Media = &mediaCopy
		}
	}
	return topicRawProtectedSnapshot(&copy)
}

func topicRawProtectedSnapshot(m *tg.Message) ([]byte, error) {
	if m == nil {
		return nil, fmt.Errorf("message disappeared during verification")
	}
	return json.Marshal([]any{m.ID, m.Out, m.FromID, m.PeerID, m.Media, m.ReplyTo, m.GroupedID})
}

// Report field paths only. Values contain Telegram access references and must
// never be included in logs, errors or the public result.
func topicProtectedChangedPaths(before, after []byte) []string {
	var left, right []any
	leftDecoder, rightDecoder := json.NewDecoder(bytes.NewReader(before)), json.NewDecoder(bytes.NewReader(after))
	leftDecoder.UseNumber()
	rightDecoder.UseNumber()
	if leftDecoder.Decode(&left) != nil || rightDecoder.Decode(&right) != nil || len(left) != 7 || len(right) != 7 {
		return []string{"protected_snapshot"}
	}
	paths := []string{}
	var compare func(string, any, any)
	compare = func(path string, a, b any) {
		if reflect.DeepEqual(a, b) {
			return
		}
		am, aok := a.(map[string]any)
		bm, bok := b.(map[string]any)
		if aok && bok {
			keys := map[string]bool{}
			for key := range am {
				keys[key] = true
			}
			for key := range bm {
				keys[key] = true
			}
			for key := range keys {
				compare(path+"."+key, am[key], bm[key])
			}
			return
		}
		as, aok := a.([]any)
		bs, bok := b.([]any)
		if aok && bok && len(as) == len(bs) {
			for i := range as {
				compare(fmt.Sprintf("%s[%d]", path, i), as[i], bs[i])
			}
			return
		}
		paths = append(paths, path)
	}
	for i, name := range []string{"id", "out", "from", "peer", "media", "reply", "grouped_id"} {
		compare(name, left[i], right[i])
	}
	sort.Strings(paths)
	return paths
}

func sameTopicEntities(a, b []tg.MessageEntityClass) bool {
	var left, right bin.Buffer
	for _, e := range a {
		if err := e.Encode(&left); err != nil {
			return false
		}
	}
	for _, e := range b {
		if err := e.Encode(&right); err != nil {
			return false
		}
	}
	return bytes.Equal(left.Buf, right.Buf)
}

func desiredTopicEntities(e InterviewTopicEdit, current *tg.Message) ([]tg.MessageEntityClass, error) {
	if e.NewEntities != nil {
		return topicEntities(e.NewText, *e.NewEntities)
	}
	if current.Message != e.NewText && len(current.Entities) > 0 {
		return nil, fmt.Errorf("message %d has entities; provide explicit new_entities for the changed text", e.ID)
	}
	return current.Entities, nil
}

func checkTopicEdit(current *tg.Message, e InterviewTopicEdit) ([]tg.MessageEntityClass, bool, error) {
	if err := validateTopicMessage(current, e.ID); err != nil {
		return nil, false, err
	}
	media, err := topicMediaID(current)
	if err != nil {
		return nil, false, err
	}
	if media != e.ExpectedMediaID {
		return nil, false, fmt.Errorf("message %d exact media identity differs or is not specified", e.ID)
	}
	if media != "" && len(utf16.Encode([]rune(e.NewText))) > 1024 {
		return nil, false, fmt.Errorf("message %d caption exceeds 1024 UTF-16 units", e.ID)
	}
	entities, err := desiredTopicEntities(e, current)
	if err != nil {
		return nil, false, err
	}
	already := current.Message == e.NewText && sameTopicEntities(current.Entities, entities)
	if !already && topicTextHash(current.Message) != e.ExpectedTextHash {
		return nil, false, fmt.Errorf("message %d complete old text hash differs", e.ID)
	}
	return entities, already, nil
}

func checkTopicZIP(current *tg.Message, d InterviewTopicDeleteZIP) (bool, error) {
	size, err := verifyTopicZIPBackup(d)
	if err != nil {
		return false, err
	}
	if current == nil {
		return true, nil
	}
	if err := validateTopicMessage(current, d.ID); err != nil {
		return false, err
	}
	attachments := extractAttachments(current.Media)
	if len(attachments) != 1 || attachments[0].MediaID != d.ExpectedMediaID || attachments[0].FileName != d.Filename || attachments[0].Size != size || attachments[0].MIMEType != "application/zip" && attachments[0].MIMEType != "application/x-zip-compressed" || topicTextHash(current.Message) != d.ExpectedTextHash {
		return false, fmt.Errorf("ZIP post %d media/filename/size/MIME/full text differs", d.ID)
	}
	return false, nil
}

func updateInterviewTopic(ctx context.Context, source interviewTopicUpdateSource, m InterviewTopicUpdateManifest, apply bool) (InterviewTopicUpdateResult, error) {
	result := InterviewTopicUpdateResult{AccountID: m.AccountID, ChannelID: m.ChannelID, TopicID: m.TopicID, DryRun: !apply, Rows: []InterviewTopicUpdateRow{}}
	if err := ValidateInterviewTopicUpdateManifest(m); err != nil {
		return result, err
	}
	self, err := source.SelfProfile(ctx)
	if err != nil {
		return result, err
	}
	if self.ID != InterviewTopicAccountID || !strings.EqualFold(self.Username, SavedMessagesMainUsername) {
		return result, fmt.Errorf("active session must be @Pheik13 ID %d", InterviewTopicAccountID)
	}
	if err := source.interviewTopicPrepare(ctx); err != nil {
		return result, err
	}
	// Capture protected fields for every edit before any mutation. Do not
	// serialize these in output: media contains access hashes/file references.
	before := map[int][]byte{}
	rawBefore := map[int][]byte{}
	entities := map[int][]tg.MessageEntityClass{}
	for _, e := range m.Edits {
		current, err := source.interviewTopicRead(ctx, e.ID)
		if err != nil {
			return result, err
		}
		wanted, already, err := checkTopicEdit(current, e)
		if err != nil {
			return result, err
		}
		before[e.ID], err = topicProtectedSnapshot(current)
		if err != nil {
			return result, err
		}
		rawBefore[e.ID], err = topicRawProtectedSnapshot(current)
		if err != nil {
			return result, err
		}
		entities[e.ID] = wanted
		row := InterviewTopicUpdateRow{ID: e.ID, Action: "edit", AlreadyApplied: already}
		if len(result.Rows) == 0 {
			for _, entity := range current.Entities {
				row.SourceEntityTypes = append(row.SourceEntityTypes, entity.TypeName())
			}
		}
		result.Rows = append(result.Rows, row)
	}
	for _, d := range m.DeleteZIP {
		current, err := source.interviewTopicRead(ctx, d.ID)
		if err != nil {
			return result, err
		}
		already, err := checkTopicZIP(current, d)
		if err != nil {
			return result, err
		}
		result.Rows = append(result.Rows, InterviewTopicUpdateRow{ID: d.ID, Action: "delete_zip", AlreadyApplied: already})
	}
	result.Ready = true
	if !apply {
		result.Ready = false
		for i, e := range m.Edits {
			current, err := source.interviewTopicRead(ctx, e.ID)
			if err != nil {
				result.Rows[i].Error = err.Error()
				return result, err
			}
			if _, _, err := checkTopicEdit(current, e); err != nil {
				result.Rows[i].Error = err.Error()
				return result, err
			}
			raw, err := topicRawProtectedSnapshot(current)
			if err != nil {
				return result, err
			}
			result.Rows[i].ProtectedChangedPaths = topicProtectedChangedPaths(rawBefore[e.ID], raw)
			currentProtected, err := topicProtectedSnapshot(current)
			if err != nil || !bytes.Equal(before[e.ID], currentProtected) {
				result.Ready = false
				result.Rows[i].Error = "protected fields changed during read-only preflight"
				return result, errors.New(result.Rows[i].Error)
			}
		}
		result.Ready = true
		return result, nil
	}
	for i, e := range m.Edits {
		row := &result.Rows[i]
		current, err := source.interviewTopicRead(ctx, e.ID)
		if err != nil {
			row.Error = err.Error()
			return result, err
		}
		_, already, err := checkTopicEdit(current, e)
		protected, snapErr := topicProtectedSnapshot(current)
		if err != nil || snapErr != nil || !bytes.Equal(before[e.ID], protected) {
			row.Error = "message changed during preflight; review before editing"
			return result, errors.New(row.Error)
		}
		var writeErr error
		if !already {
			writeErr = source.interviewTopicEdit(ctx, e.ID, e.NewText, entities[e.ID])
		} else {
			row.AlreadyApplied = true
		}
		current, err = source.interviewTopicRead(ctx, e.ID)
		if err == nil {
			err = validateTopicMessage(current, e.ID)
		}
		if err == nil {
			protected, err = topicProtectedSnapshot(current)
		}
		if err != nil || current == nil || current.Message != e.NewText || !sameTopicEntities(current.Entities, entities[e.ID]) || !bytes.Equal(before[e.ID], protected) {
			row.OutcomeUnknown, row.Error = true, "edited text/entities/media/reply/topic not confirmed by server; inspect, do not repeat blindly"
			return result, errors.Join(errors.New(row.Error), writeErr, err)
		}
		row.ServerVerified = true
		if writeErr != nil {
			row.Error = writeErr.Error()
			return result, writeErr
		}
	}
	for i, d := range m.DeleteZIP {
		row := &result.Rows[len(m.Edits)+i]
		current, err := source.interviewTopicRead(ctx, d.ID)
		if err != nil {
			row.Error = err.Error()
			return result, err
		}
		already, err := checkTopicZIP(current, d)
		if err != nil {
			row.Error = err.Error()
			return result, err
		}
		var writeErr error
		if !already {
			writeErr = source.interviewTopicDeleteZIP(ctx, d.ID)
		} else {
			row.AlreadyApplied = true
		}
		current, err = source.interviewTopicRead(ctx, d.ID)
		if err != nil || current != nil {
			row.OutcomeUnknown, row.Error = true, "ZIP deletion not confirmed by exact server absence; inspect before retry"
			return result, errors.Join(errors.New(row.Error), writeErr, err)
		}
		row.ServerVerified = true
		if writeErr != nil {
			row.Error = writeErr.Error()
			return result, writeErr
		}
	}
	result.Complete = true
	return result, nil
}

type interviewTopicSession struct {
	*Session
	target resolvedTarget
}

func (s *Session) UpdateInterviewTopic(ctx context.Context, m InterviewTopicUpdateManifest, apply bool) (InterviewTopicUpdateResult, error) {
	return updateInterviewTopic(ctx, &interviewTopicSession{Session: s}, m, apply)
}

func (s *interviewTopicSession) interviewTopicPrepare(ctx context.Context) error {
	target, err := s.resolveTarget(ctx, "supergroup:2621478426")
	if err != nil {
		return err
	}
	peer, ok := target.InputPeer.(*tg.InputPeerChannel)
	if !ok || peer.ChannelID != InterviewTopicChannelID || target.Chat.ID != InterviewTopicChannelID || target.Chat.Type != "supergroup" {
		return fmt.Errorf("authorized interview supergroup identity not proven")
	}
	s.target = target
	var topics *tg.MessagesForumTopics
	err = s.performRPC(ctx, "get_interview_topic", func(ctx context.Context) error {
		var err error
		topics, err = s.raw.MessagesGetForumTopicsByID(ctx, &tg.MessagesGetForumTopicsByIDRequest{Peer: peer, Topics: []int{InterviewTopicID}})
		return err
	})
	if err != nil {
		return err
	}
	for _, topic := range topics.Topics {
		if t, ok := topic.(*tg.ForumTopic); ok && t.ID == InterviewTopicID && t.Title == "Собеседования" {
			return nil
		}
	}
	return fmt.Errorf("authorized interview topic ID/title not returned by server")
}

func (s *interviewTopicSession) interviewTopicRead(ctx context.Context, id int) (*tg.Message, error) {
	peer := s.target.InputPeer.(*tg.InputPeerChannel)
	var result tg.MessagesMessagesClass
	err := s.performRPC(ctx, "read_interview_topic_message", func(ctx context.Context) error {
		var err error
		result, err = s.raw.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{Channel: &tg.InputChannel{ChannelID: peer.ChannelID, AccessHash: peer.AccessHash}, ID: []tg.InputMessageClass{&tg.InputMessageID{ID: id}}})
		return err
	})
	if err != nil {
		return nil, err
	}
	for _, message := range historyMessages(result) {
		switch m := message.(type) {
		case *tg.Message:
			if m.ID == id {
				return m, nil
			}
		case *tg.MessageEmpty:
			if m.ID == id {
				return nil, nil
			}
		}
	}
	return nil, fmt.Errorf("server did not return requested message or exact MessageEmpty %d", id)
}

func (s *interviewTopicSession) interviewTopicEdit(ctx context.Context, id int, text string, entities []tg.MessageEntityClass) error {
	return s.performRPC(ctx, "edit_own_interview_topic_message", func(ctx context.Context) error {
		request := &tg.MessagesEditMessageRequest{Peer: s.target.InputPeer, ID: id, NoWebpage: true}
		request.SetMessage(text)
		request.SetEntities(entities)
		_, err := s.raw.MessagesEditMessage(ctx, request)
		return err
	})
}

func (s *interviewTopicSession) interviewTopicDeleteZIP(ctx context.Context, id int) error {
	if id != 2348 && id != 2352 {
		return fmt.Errorf("only authorized ZIP post IDs 2348 and 2352 can be deleted")
	}
	peer := s.target.InputPeer.(*tg.InputPeerChannel)
	return s.performRPC(ctx, "delete_own_backed_up_interview_zip", func(ctx context.Context) error {
		_, err := s.raw.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{Channel: &tg.InputChannel{ChannelID: peer.ChannelID, AccessHash: peer.AccessHash}, ID: []int{id}})
		return err
	})
}
