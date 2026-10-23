package mtproto

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/gotd/td/tg"
)

// This corrective command is limited to the two published screening audio IDs.
type InterviewTopicReplaceVideoManifest struct {
	Profile      string                           `json:"profile"`
	AccountID    int64                            `json:"account_id"`
	ChannelID    int64                            `json:"channel_id"`
	TopicID      int                              `json:"topic_id"`
	Replacements []InterviewTopicVideoReplacement `json:"replacements"`
}
type InterviewTopicVideoReplacement struct {
	ID                  int                    `json:"id"`
	ParentID            int                    `json:"parent_id"`
	ParentTextHash      string                 `json:"parent_text_hash"`
	ExpectedCaptionHash string                 `json:"expected_caption_hash"`
	ExpectedMediaID     string                 `json:"expected_media_id"`
	OldAudio            InterviewTopicOldAudio `json:"old_audio"`
	NewCaption          string                 `json:"new_caption"`
	NewEntities         []InterviewTopicEntity `json:"new_entities,omitempty"`
	NewVideo            InterviewTopicFile     `json:"new_video"`
}
type InterviewTopicOldAudio struct {
	Filename string `json:"filename"`
	MIME     string `json:"mime"`
	Size     int64  `json:"size"`
	Duration int    `json:"duration"`
}

func (r InterviewTopicVideoReplacement) newPost() InterviewTopicPost {
	return InterviewTopicPost{Key: fmt.Sprintf("replace-audio-%d", r.ID), Text: r.NewCaption, Entities: r.NewEntities, ParentID: r.ParentID, ParentTextHash: r.ParentTextHash, File: &r.NewVideo}
}
func (r InterviewTopicVideoReplacement) hash() string {
	b, _ := json.Marshal(r)
	return topicTextHash(string(b))
}
func ValidateInterviewTopicReplaceVideoManifest(m InterviewTopicReplaceVideoManifest) error {
	if m.Profile != "main" || m.AccountID != InterviewTopicAccountID || m.ChannelID != InterviewTopicChannelID || m.TopicID != InterviewTopicID {
		return fmt.Errorf("replacement is bound to fixed main interview topic")
	}
	if len(m.Replacements) < 1 || len(m.Replacements) > 2 {
		return fmt.Errorf("provide one or two exact replacements")
	}
	seen := map[int]bool{}
	for _, r := range m.Replacements {
		parent := map[int]int{2362: 2361, 2365: 2364}[r.ID]
		if parent == 0 || r.ParentID != parent || seen[r.ID] || !validSHA256(r.ParentTextHash) || !validSHA256(r.ExpectedCaptionHash) || !strings.HasPrefix(r.ExpectedMediaID, "document:") {
			return fmt.Errorf("only audio IDs 2362/2365 with fixed parents and exact hashes/media ID may be replaced")
		}
		seen[r.ID] = true
		if r.OldAudio.Size <= 0 || r.OldAudio.Duration <= 0 || r.OldAudio.Filename == "" || !strings.HasPrefix(r.OldAudio.MIME, "audio/") || r.NewVideo.Kind != "video" {
			return fmt.Errorf("replacement must declare exact old audio and new video metadata")
		}
		p := r.newPost()
		if err := ValidateInterviewTopicPublishManifest(InterviewTopicPublishManifest{Profile: m.Profile, AccountID: m.AccountID, ChannelID: m.ChannelID, TopicID: m.TopicID, Posts: []InterviewTopicPost{p}}); err != nil {
			return err
		}
	}
	return nil
}

type topicReplaceVideoSource interface {
	SelfProfile(context.Context) (harvest.SelfProfile, error)
	interviewTopicPrepare(context.Context) error
	interviewTopicRead(context.Context, int) (*tg.Message, error)
	replaceVideoUpload(context.Context, InterviewTopicVideoReplacement) (tg.InputMediaClass, error)
	replaceVideoEdit(context.Context, InterviewTopicVideoReplacement, tg.InputMediaClass) error
}

func replaceVideoIdentity(ctx context.Context, s topicReplaceVideoSource) error {
	self, err := s.SelfProfile(ctx)
	if err != nil {
		return err
	}
	if self.ID != InterviewTopicAccountID || !strings.EqualFold(self.Username, SavedMessagesMainUsername) {
		return fmt.Errorf("replacement requires main/@Pheik13 numeric identity")
	}
	return s.interviewTopicPrepare(ctx)
}
func replacementCurrent(ctx context.Context, s topicReplaceVideoSource, r InterviewTopicVideoReplacement) (bool, error) {
	parent, err := s.interviewTopicRead(ctx, r.ParentID)
	if err != nil {
		return false, err
	}
	if err = validateTopicMessage(parent, r.ParentID); err != nil {
		return false, err
	}
	if topicTextHash(parent.Message) != r.ParentTextHash {
		return false, fmt.Errorf("parent text changed")
	}
	current, err := s.interviewTopicRead(ctx, r.ID)
	if err != nil {
		return false, err
	}
	if err = validateTopicMessage(current, r.ID); err != nil {
		return false, err
	}
	if current.GroupedID != 0 {
		return false, fmt.Errorf("album replacements are excluded")
	}
	reply, _ := replyInfo(current.ReplyTo)
	if reply != r.ParentID {
		return false, fmt.Errorf("replacement parent mismatch")
	}
	if verifyPublishedPost(current, r.newPost(), r.ParentID) == nil {
		return true, nil
	}
	if topicTextHash(current.Message) != r.ExpectedCaptionHash {
		return false, fmt.Errorf("old caption differs")
	}
	mediaID, err := topicMediaID(current)
	if err != nil || mediaID != r.ExpectedMediaID {
		return false, fmt.Errorf("old media ID differs")
	}
	oldPost := InterviewTopicPost{Text: current.Message, File: &InterviewTopicFile{Kind: "audio", Filename: r.OldAudio.Filename, MIME: r.OldAudio.MIME, Size: r.OldAudio.Size, Duration: float64(r.OldAudio.Duration), HasAudio: true}}
	// Verify original entities without interpreting or changing them.
	plainCopy := *current
	plainCopy.Entities = nil
	if err = verifyPublishedPost(&plainCopy, oldPost, r.ParentID); err != nil {
		return false, fmt.Errorf("old audio metadata: %w", err)
	}
	return false, nil
}
func replaceInterviewTopicVideo(ctx context.Context, s topicReplaceVideoSource, m InterviewTopicReplaceVideoManifest, j topicPublishJournal, save func() error, apply bool) (InterviewTopicPublishResult, error) {
	result := InterviewTopicPublishResult{DryRun: !apply, Rows: []InterviewTopicPublishRow{}}
	if err := ValidateInterviewTopicReplaceVideoManifest(m); err != nil {
		return result, err
	}
	if err := replaceVideoIdentity(ctx, s); err != nil {
		return result, err
	}
	// Every old message and new local file must pass before any upload/edit.
	for _, r := range m.Replacements {
		if err := verifyPublishFile(ctx, &r.NewVideo); err != nil {
			return result, err
		}
		already, err := replacementCurrent(ctx, s, r)
		if err != nil {
			return result, err
		}
		receipt, exists := j[r.newPost().Key]
		if exists && receipt.Hash != r.hash() {
			return result, fmt.Errorf("replacement receipt payload changed")
		}
		if exists && receipt.Pending && !already {
			return result, fmt.Errorf("outcome_unknown for %d; no retry before manual reconciliation", r.ID)
		}
	}
	for _, r := range m.Replacements {
		row := InterviewTopicPublishRow{Key: r.newPost().Key, ID: r.ID}
		already, err := replacementCurrent(ctx, s, r)
		if err != nil {
			return result, err
		}
		if already {
			row.AlreadyApplied = true
			row.ServerVerified = true
			if apply {
				j[row.Key] = topicPublishReceipt{Hash: r.hash(), ID: r.ID}
				if err = save(); err != nil {
					return result, err
				}
			}
			result.Rows = append(result.Rows, row)
			continue
		}
		if !apply {
			result.Rows = append(result.Rows, row)
			continue
		}
		if err = verifyPublishFile(ctx, &r.NewVideo); err != nil {
			return result, err
		}
		media, err := s.replaceVideoUpload(ctx, r)
		if err != nil {
			return result, err
		}
		if err = verifyPublishFile(ctx, &r.NewVideo); err != nil {
			return result, err
		}
		if err = replaceVideoIdentity(ctx, s); err != nil {
			return result, err
		}
		already, err = replacementCurrent(ctx, s, r)
		if err != nil {
			return result, err
		}
		if already {
			return result, fmt.Errorf("target changed during upload; rerun read-only verification")
		}
		j[row.Key] = topicPublishReceipt{Hash: r.hash(), ID: r.ID, Pending: true}
		if err = save(); err != nil {
			return result, err
		}
		writeErr := s.replaceVideoEdit(ctx, r, media)
		current, readErr := s.interviewTopicRead(ctx, r.ID)
		if readErr == nil {
			if current == nil || current.ID != r.ID || current.GroupedID != 0 {
				readErr = fmt.Errorf("replacement ID/album readback mismatch")
			} else {
				readErr = verifyPublishedPost(current, r.newPost(), r.ParentID)
			}
		}
		if readErr != nil {
			row.OutcomeUnknown = true
			result.Rows = append(result.Rows, row)
			return result, fmt.Errorf("outcome_unknown for %d; edit is not retried: %w", r.ID, errors.Join(writeErr, readErr))
		}
		row.ServerVerified = true
		j[row.Key] = topicPublishReceipt{Hash: r.hash(), ID: r.ID}
		if err = save(); err != nil {
			result.Rows = append(result.Rows, row)
			return result, err
		}
		result.Rows = append(result.Rows, row)
	}
	result.Ready = true
	result.Complete = apply
	return result, nil
}
func (s *Session) ReplaceInterviewTopicVideo(ctx context.Context, m InterviewTopicReplaceVideoManifest, journalPath string, apply bool) (InterviewTopicPublishResult, error) {
	j, err := loadPublishJournal(journalPath)
	if err != nil {
		return InterviewTopicPublishResult{}, err
	}
	return replaceInterviewTopicVideo(ctx, &interviewTopicSession{Session: s}, m, j, func() error { return savePublishJournal(journalPath, j) }, apply)
}
func (s *interviewTopicSession) replaceVideoUpload(ctx context.Context, r InterviewTopicVideoReplacement) (tg.InputMediaClass, error) {
	return s.uploadInterviewFile(ctx, &r.NewVideo)
}
func (s *interviewTopicSession) replaceVideoEdit(ctx context.Context, r InterviewTopicVideoReplacement, media tg.InputMediaClass) error {
	if r.ID != 2362 && r.ID != 2365 {
		return fmt.Errorf("replacement ID not allowed")
	}
	entities, err := topicEntities(r.NewCaption, r.NewEntities)
	if err != nil {
		return err
	}
	req := &tg.MessagesEditMessageRequest{Peer: s.target.InputPeer, ID: r.ID}
	req.SetMedia(media)
	req.SetMessage(r.NewCaption)
	req.SetEntities(entities)
	// Exactly one paced mutation; errors lead to readback, never automatic retry.
	if err = s.beforeRPC(ctx, "replace_interview_audio_video"); err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultRPCTimeout)
	defer cancel()
	_, err = s.raw.MessagesEditMessage(callCtx, req)
	return err
}
