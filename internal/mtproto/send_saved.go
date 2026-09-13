package mtproto

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/config"
	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	tdcrypto "github.com/gotd/td/crypto"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/telegram/message/unpack"
	"github.com/gotd/td/tg"
)

const SavedMessagesMainUsername = "Pheik13"

type SendSavedOptions struct {
	Text            string
	FilePath        string
	Caption         string
	SourceChat      string
	SourceMessageID int
}

type SendSavedResult struct {
	Destination string                 `json:"destination"`
	Profile     harvest.SelfProfile    `json:"profile"`
	Message     harvest.MessageRecord  `json:"message"`
	Copy        *SavedCopyVerification `json:"copy,omitempty"`
	Verified    bool                   `json:"verified"`
}

type SavedCopyVerification struct {
	SourceChatID       int64  `json:"source_chat_id"`
	SourceMessageID    int    `json:"source_message_id"`
	SourceMediaID      string `json:"source_media_id"`
	DestinationMediaID string `json:"destination_media_id"`
	SourceSHA256       string `json:"source_sha256"`
	ExactFileUploaded  bool   `json:"exact_file_uploaded"`
	PreviewPreserved   bool   `json:"preview_preserved"`
	Video              bool   `json:"video"`
	SupportsStreaming  bool   `json:"supports_streaming"`
}

type savedFileMetadata struct {
	Path     string
	Name     string
	MIMEType string
	Size     int64
}

type savedVideoEvidence struct {
	DocumentID        int64
	HasPreview        bool
	Video             bool
	SupportsStreaming bool
}

// SendSaved is the sole Telegram write primitive exposed by telegram-harvest.
// It is intentionally bound to the main profile and InputPeerSelf: callers
// cannot provide or resolve a recipient.
func (c *Client) SendSaved(ctx context.Context, opts SendSavedOptions) (SendSavedResult, error) {
	if c.cfg.Mode != config.ModeMain {
		return SendSavedResult{}, fmt.Errorf("send-saved is supported only for profile main")
	}

	normalized, fileMetadata, err := normalizeSendSavedOptions(opts)
	if err != nil {
		return SendSavedResult{}, err
	}

	var result SendSavedResult
	err = c.RunAuthorized(ctx, func(runCtx context.Context, session *Session) error {
		var sendErr error
		result, sendErr = session.sendSaved(runCtx, normalized, fileMetadata)
		return sendErr
	})
	if err != nil {
		return result, err
	}
	return result, nil
}

func normalizeSendSavedOptions(opts SendSavedOptions) (SendSavedOptions, *savedFileMetadata, error) {
	hasText := strings.TrimSpace(opts.Text) != ""
	hasFile := strings.TrimSpace(opts.FilePath) != ""
	hasSource := strings.TrimSpace(opts.SourceChat) != "" || opts.SourceMessageID != 0
	payloads := 0
	for _, present := range []bool{hasText, hasFile, hasSource} {
		if present {
			payloads++
		}
	}
	if payloads != 1 {
		return SendSavedOptions{}, nil, fmt.Errorf("provide exactly one of --text, --file, or --from-chat with --message-id")
	}
	if hasSource {
		if strings.TrimSpace(opts.SourceChat) == "" {
			return SendSavedOptions{}, nil, fmt.Errorf("--from-chat is required with --message-id")
		}
		if opts.SourceMessageID <= 0 {
			return SendSavedOptions{}, nil, fmt.Errorf("--message-id must be > 0 with --from-chat")
		}
		if strings.TrimSpace(opts.Caption) != "" {
			return SendSavedOptions{}, nil, fmt.Errorf("--caption is not supported when copying an existing Telegram video")
		}
		normalized := opts
		normalized.SourceChat = strings.TrimSpace(opts.SourceChat)
		return normalized, nil, nil
	}
	if !hasFile {
		if strings.TrimSpace(opts.Caption) != "" {
			return SendSavedOptions{}, nil, fmt.Errorf("--caption requires --file")
		}
		return opts, nil, nil
	}

	absolutePath, err := filepath.Abs(strings.TrimSpace(opts.FilePath))
	if err != nil {
		return SendSavedOptions{}, nil, fmt.Errorf("resolve --file: %w", err)
	}
	info, err := os.Stat(absolutePath)
	if err != nil {
		return SendSavedOptions{}, nil, fmt.Errorf("inspect --file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return SendSavedOptions{}, nil, fmt.Errorf("--file must be a regular file")
	}

	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(info.Name())))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	normalized := opts
	normalized.FilePath = absolutePath
	return normalized, &savedFileMetadata{
		Path:     absolutePath,
		Name:     info.Name(),
		MIMEType: mimeType,
		Size:     info.Size(),
	}, nil
}

func (s *Session) sendSaved(ctx context.Context, opts SendSavedOptions, fileMetadata *savedFileMetadata) (SendSavedResult, error) {
	profile, err := s.SelfProfile(ctx)
	if err != nil {
		return SendSavedResult{}, fmt.Errorf("verify sending account: %w", err)
	}
	if err := validateSavedMessagesProfile(profile); err != nil {
		return SendSavedResult{}, err
	}

	builder := message.NewSender(s.raw).Self()
	var sourceRecord *harvest.MessageRecord
	var sourceEvidence savedVideoEvidence
	var sourceTarget resolvedTarget
	var sourceSHA256 string
	var updates tg.UpdatesClass
	if strings.TrimSpace(opts.SourceChat) != "" {
		sourceTarget, err = s.resolveTarget(ctx, opts.SourceChat)
		if err != nil {
			return SendSavedResult{}, fmt.Errorf("resolve source chat: %w", err)
		}
		sourceMessage, entities, fetchErr := s.fetchMessageByID(ctx, sourceTarget, opts.SourceMessageID)
		if fetchErr != nil {
			return SendSavedResult{}, fmt.Errorf("read source message: %w", fetchErr)
		}
		normalizedSource, ok := normalizeRecord(sourceMessage, sourceTarget.Chat, entities)
		if !ok {
			return SendSavedResult{}, fmt.Errorf("source message %d has an unsupported type", opts.SourceMessageID)
		}
		sourceEvidence, err = savedVideoEvidenceFromMessage(sourceMessage)
		if err != nil {
			return SendSavedResult{}, fmt.Errorf("source message %d is not a copyable Telegram video: %w", opts.SourceMessageID, err)
		}
		if !sourceEvidence.HasPreview {
			return SendSavedResult{}, fmt.Errorf("source message %d has no Telegram video preview; refusing a transfer that cannot prove preview preservation", opts.SourceMessageID)
		}
		sourceRecord = &normalizedSource
		sourceMessageValue, sourceDocument, documentErr := savedVideoDocumentFromMessage(sourceMessage)
		if documentErr != nil {
			return SendSavedResult{}, fmt.Errorf("prepare source message %d for copy: %w", opts.SourceMessageID, documentErr)
		}
		tempDir, tempErr := os.MkdirTemp("", "telegram-harvest-send-saved-")
		if tempErr != nil {
			return SendSavedResult{}, fmt.Errorf("prepare temporary video transfer: %w", tempErr)
		}
		defer os.RemoveAll(tempDir)
		videoPath, thumbPath, hashValue, downloadErr := s.downloadSavedVideoSource(ctx, sourceDocument, tempDir)
		if downloadErr != nil {
			return SendSavedResult{}, fmt.Errorf("download source message %d for exact upload: %w", opts.SourceMessageID, downloadErr)
		}
		sourceSHA256 = hashValue
		uploadedVideo, uploadErr := builder.Upload(message.FromPath(videoPath)).AsInputFile(ctx)
		if uploadErr != nil {
			return SendSavedResult{}, fmt.Errorf("upload source video to Saved Messages: %w", uploadErr)
		}
		uploadedThumb, uploadErr := builder.Upload(message.FromPath(thumbPath)).AsInputFile(ctx)
		if uploadErr != nil {
			return SendSavedResult{}, fmt.Errorf("upload source video preview to Saved Messages: %w", uploadErr)
		}
		randomID, randomErr := nonzeroRandomID()
		if randomErr != nil {
			return SendSavedResult{}, fmt.Errorf("generate copy random id: %w", randomErr)
		}
		sourceMedia := sourceMessageValue.Media.(*tg.MessageMediaDocument)
		updates, err = s.raw.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{
			Peer: &tg.InputPeerSelf{},
			Media: &tg.InputMediaUploadedDocument{
				NosoundVideo: normalizedSource.Attachments[0].NoAudio,
				Spoiler:      sourceMedia.Spoiler,
				File:         uploadedVideo,
				Thumb:        uploadedThumb,
				MimeType:     sourceDocument.MimeType,
				Attributes:   sourceDocument.Attributes,
			},
			Message:  sourceMessageValue.Message,
			RandomID: randomID,
			Entities: sourceMessageValue.Entities,
		})
	} else if fileMetadata == nil {
		updates, err = builder.Text(ctx, opts.Text)
	} else {
		uploaded, uploadErr := builder.Upload(message.FromPath(fileMetadata.Path)).AsInputFile(ctx)
		if uploadErr != nil {
			return SendSavedResult{}, fmt.Errorf("upload file to Saved Messages: %w", uploadErr)
		}
		document := message.File(uploaded).
			Filename(fileMetadata.Name).
			MIME(fileMetadata.MIMEType)
		if strings.TrimSpace(opts.Caption) != "" {
			document = message.File(uploaded, styling.Plain(opts.Caption)).
				Filename(fileMetadata.Name).
				MIME(fileMetadata.MIMEType)
		}
		updates, err = builder.Media(ctx, document)
	}
	if err != nil {
		return SendSavedResult{}, fmt.Errorf("send to Saved Messages: %w", err)
	}

	messageID, err := unpack.MessageID(updates, nil)
	if err != nil {
		return SendSavedResult{}, fmt.Errorf("message was sent to Saved Messages but its id could not be read: %w", err)
	}
	publicProfile := profile
	publicProfile.Phone = ""
	result := SendSavedResult{
		Destination: "saved_messages",
		Profile:     publicProfile,
	}

	target := resolvedTarget{
		Raw: "me",
		Chat: harvest.Chat{
			ID:       profile.ID,
			Type:     "user",
			Title:    profile.Display,
			Username: profile.Username,
			Display:  "Saved Messages",
		},
		InputPeer: &tg.InputPeerSelf{},
	}
	record, readbackMessage, err := s.readBackSavedMessage(ctx, target, messageID)
	if err != nil {
		return result, fmt.Errorf("message %d was sent to Saved Messages but readback failed; do not retry blindly: %w", messageID, err)
	}
	result.Message = record
	if sourceRecord != nil {
		destinationEvidence, evidenceErr := savedVideoEvidenceFromMessage(readbackMessage)
		if evidenceErr != nil {
			return result, fmt.Errorf("message %d was copied to Saved Messages but video readback failed; do not retry blindly: %w", messageID, evidenceErr)
		}
		if verifyErr := verifyCopiedSavedMessage(record, *sourceRecord, profile, sourceEvidence, destinationEvidence); verifyErr != nil {
			return result, fmt.Errorf("message %d was copied to Saved Messages but verification failed; do not retry blindly: %w", messageID, verifyErr)
		}
		result.Copy = &SavedCopyVerification{
			SourceChatID:       sourceTarget.Chat.ID,
			SourceMessageID:    opts.SourceMessageID,
			SourceMediaID:      sourceRecord.Attachments[0].MediaID,
			DestinationMediaID: record.Attachments[0].MediaID,
			SourceSHA256:       sourceSHA256,
			ExactFileUploaded:  true,
			PreviewPreserved:   true,
			Video:              true,
			SupportsStreaming:  destinationEvidence.SupportsStreaming,
		}
	} else if err := verifySavedMessage(record, profile, opts, fileMetadata); err != nil {
		return result, fmt.Errorf("message %d was sent to Saved Messages but verification failed; do not retry blindly: %w", messageID, err)
	}
	result.Verified = true
	return result, nil
}

func validateSavedMessagesProfile(profile harvest.SelfProfile) error {
	if strings.EqualFold(strings.TrimSpace(profile.Username), SavedMessagesMainUsername) {
		return nil
	}
	actual := strings.TrimSpace(profile.Username)
	if actual == "" {
		actual = "<no username>"
	} else {
		actual = "@" + actual
	}
	return fmt.Errorf("profile main is authorized as %s; refusing send because @%s is required", actual, SavedMessagesMainUsername)
}

func (s *Session) readBackSavedMessage(ctx context.Context, target resolvedTarget, messageID int) (harvest.MessageRecord, tg.MessageClass, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		msgClass, entities, err := s.fetchMessageByID(ctx, target, messageID)
		if err == nil {
			record, ok := normalizeRecord(msgClass, target.Chat, entities)
			if !ok {
				return harvest.MessageRecord{}, nil, fmt.Errorf("unsupported message type %T", msgClass)
			}
			return record, msgClass, nil
		}
		lastErr = err
		if attempt == 2 {
			break
		}
		select {
		case <-ctx.Done():
			return harvest.MessageRecord{}, nil, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return harvest.MessageRecord{}, nil, lastErr
}

func verifySavedMessage(record harvest.MessageRecord, profile harvest.SelfProfile, opts SendSavedOptions, fileMetadata *savedFileMetadata) error {
	// Telegram represents a message in the user's own Saved Messages without
	// the ordinary outgoing flag. The strong destination proof is that this
	// exact send result was fetched back through InputPeerSelf under the
	// already verified @Pheik13 session.
	if record.Chat.ID != profile.ID || !strings.EqualFold(record.Chat.Username, profile.Username) {
		return fmt.Errorf("readback is not from the authorized account's self chat")
	}
	if fileMetadata == nil {
		if record.Text != strings.TrimSpace(opts.Text) {
			return fmt.Errorf("text readback mismatch")
		}
		return nil
	}
	if record.Text != strings.TrimSpace(opts.Caption) {
		return fmt.Errorf("caption readback mismatch")
	}
	if len(record.Attachments) != 1 {
		return fmt.Errorf("expected one attachment, got %d", len(record.Attachments))
	}
	attachment := record.Attachments[0]
	if attachment.FileName != fileMetadata.Name {
		return fmt.Errorf("filename readback mismatch: got %q, want %q", attachment.FileName, fileMetadata.Name)
	}
	if attachment.MIMEType != fileMetadata.MIMEType {
		return fmt.Errorf("MIME type readback mismatch: got %q, want %q", attachment.MIMEType, fileMetadata.MIMEType)
	}
	if attachment.Size != fileMetadata.Size {
		return fmt.Errorf("file size readback mismatch: got %d, want %d", attachment.Size, fileMetadata.Size)
	}
	return nil
}

func verifyCopiedSavedMessage(record, source harvest.MessageRecord, profile harvest.SelfProfile, sourceEvidence, destinationEvidence savedVideoEvidence) error {
	if record.Chat.ID != profile.ID || !strings.EqualFold(record.Chat.Username, profile.Username) {
		return fmt.Errorf("readback is not from the authorized account's self chat")
	}
	if record.Text != source.Text {
		return fmt.Errorf("copied caption readback mismatch")
	}
	if len(source.Attachments) != 1 || len(record.Attachments) != 1 {
		return fmt.Errorf("expected one source and destination attachment, got %d and %d", len(source.Attachments), len(record.Attachments))
	}
	sourceAttachment := source.Attachments[0]
	destinationAttachment := record.Attachments[0]
	if sourceAttachment.Kind != "video" || destinationAttachment.Kind != "video" {
		return fmt.Errorf("expected Telegram video attachments, got %q and %q", sourceAttachment.Kind, destinationAttachment.Kind)
	}
	if sourceAttachment.MediaID == "" || destinationAttachment.MediaID == "" {
		return fmt.Errorf("telegram media id is missing from source or destination readback")
	}
	if destinationAttachment.FileName != sourceAttachment.FileName {
		return fmt.Errorf("filename readback mismatch: got %q, want %q", destinationAttachment.FileName, sourceAttachment.FileName)
	}
	if destinationAttachment.MIMEType != sourceAttachment.MIMEType {
		return fmt.Errorf("MIME type readback mismatch: got %q, want %q", destinationAttachment.MIMEType, sourceAttachment.MIMEType)
	}
	if destinationAttachment.Size != sourceAttachment.Size {
		return fmt.Errorf("file size readback mismatch: got %d, want %d", destinationAttachment.Size, sourceAttachment.Size)
	}
	if destinationAttachment.DurationSeconds != sourceAttachment.DurationSeconds ||
		destinationAttachment.Width != sourceAttachment.Width || destinationAttachment.Height != sourceAttachment.Height ||
		destinationAttachment.NoAudio != sourceAttachment.NoAudio {
		return fmt.Errorf("video attributes changed during copy")
	}
	if !sourceEvidence.Video || !destinationEvidence.Video {
		return fmt.Errorf("telegram video attribute is missing")
	}
	if sourceEvidence.DocumentID == 0 || destinationEvidence.DocumentID == 0 {
		return fmt.Errorf("telegram document id is missing from source or destination readback")
	}
	if !sourceEvidence.HasPreview || !destinationEvidence.HasPreview {
		return fmt.Errorf("telegram video preview was not preserved")
	}
	if destinationEvidence.SupportsStreaming != sourceEvidence.SupportsStreaming {
		return fmt.Errorf("telegram streaming attribute changed during copy")
	}
	return nil
}

func (s *Session) downloadSavedVideoSource(ctx context.Context, document *tg.Document, tempDir string) (string, string, string, error) {
	fileName := safeFileName(documentFileName(document))
	if fileName == "" {
		return "", "", "", fmt.Errorf("source filename is missing")
	}
	videoPath := filepath.Join(tempDir, fileName)
	downloadCtx, cancel := context.WithTimeout(ctx, defaultDownloadTimeout)
	err := s.downloadFile(downloadCtx, document.AsInputDocumentFileLocation(), videoPath, document.Size, nil)
	cancel()
	if err != nil {
		return "", "", "", fmt.Errorf("video: %w", err)
	}

	thumbLocation, thumbSize, ok := savedVideoThumbnailLocation(document)
	if !ok {
		return "", "", "", fmt.Errorf("source Telegram preview has no downloadable static thumbnail")
	}
	thumbPath := filepath.Join(tempDir, "preview.jpg")
	thumbCtx, thumbCancel := context.WithTimeout(ctx, defaultDownloadTimeout)
	err = s.downloadFile(thumbCtx, thumbLocation, thumbPath, thumbSize, nil)
	thumbCancel()
	if err != nil {
		return "", "", "", fmt.Errorf("preview: %w", err)
	}

	hashValue, err := fileSHA256(videoPath)
	if err != nil {
		return "", "", "", fmt.Errorf("hash downloaded source video: %w", err)
	}
	return videoPath, thumbPath, hashValue, nil
}

func savedVideoThumbnailLocation(document *tg.Document) (tg.InputFileLocationClass, int64, bool) {
	if document == nil {
		return nil, 0, false
	}
	bestType := ""
	bestArea := -1
	var bestSize int64
	for _, size := range document.Thumbs {
		width, height, ok := photoSizeDimensions(size)
		if !ok {
			continue
		}
		byteSize := photoSizeBytes(size)
		if byteSize <= 0 {
			continue
		}
		area := width * height
		if area <= bestArea {
			continue
		}
		bestType = size.GetType()
		bestArea = area
		bestSize = byteSize
	}
	if bestType == "" {
		return nil, 0, false
	}
	return &tg.InputDocumentFileLocation{
		ID:            document.ID,
		AccessHash:    document.AccessHash,
		FileReference: document.FileReference,
		ThumbSize:     bestType,
	}, bestSize, true
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func savedVideoDocumentFromMessage(messageClass tg.MessageClass) (*tg.Message, *tg.Document, error) {
	messageValue, ok := messageClass.(*tg.Message)
	if !ok {
		return nil, nil, fmt.Errorf("unsupported message type %T", messageClass)
	}
	media, ok := messageValue.Media.(*tg.MessageMediaDocument)
	if !ok {
		return nil, nil, fmt.Errorf("media is %T, not a document", messageValue.Media)
	}
	document, ok := media.Document.(*tg.Document)
	if !ok {
		return nil, nil, fmt.Errorf("document is %T", media.Document)
	}
	return messageValue, document, nil
}

func nonzeroRandomID() (int64, error) {
	for {
		value, err := tdcrypto.RandInt64(cryptorand.Reader)
		if err != nil {
			return 0, err
		}
		if value != 0 {
			return value, nil
		}
	}
}

func savedVideoEvidenceFromMessage(messageClass tg.MessageClass) (savedVideoEvidence, error) {
	messageValue, document, err := savedVideoDocumentFromMessage(messageClass)
	if err != nil {
		return savedVideoEvidence{}, err
	}
	media := messageValue.Media.(*tg.MessageMediaDocument)
	evidence := savedVideoEvidence{
		DocumentID: document.ID,
		HasPreview: len(document.Thumbs) > 0 || len(document.VideoThumbs) > 0 || media.VideoCover != nil,
		Video:      media.Video,
	}
	for _, attribute := range document.Attributes {
		video, ok := attribute.(*tg.DocumentAttributeVideo)
		if !ok {
			continue
		}
		evidence.Video = true
		evidence.SupportsStreaming = video.SupportsStreaming
		return evidence, nil
	}
	return savedVideoEvidence{}, fmt.Errorf("telegram video attribute is missing")
}
