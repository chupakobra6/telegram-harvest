package mtproto

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/config"
	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	tdcrypto "github.com/gotd/td/crypto"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/telegram/message/unpack"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
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
	Method             string `json:"method"`
	SourceSHA256       string `json:"source_sha256,omitempty"`
	SameServerDocument bool   `json:"same_server_document"`
	ExactFileUploaded  bool   `json:"exact_file_uploaded"`
	PreviewPresent     bool   `json:"preview_present"`
	PreviewPreserved   bool   `json:"preview_preserved"`
	Video              bool   `json:"video"`
	Audio              bool   `json:"audio"`
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

// SendSaved is the self-only sending primitive exposed by telegram-harvest.
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
	var copyMethod string
	var sourceMessageValue *tg.Message
	var sourceAudio *tg.DocumentAttributeAudio
	var sourcePreviewSHA256 string
	var copyTempDir string
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
		sourceRecord = &normalizedSource
		sourceValue, sourceDocument, documentErr := savedVideoDocumentFromMessage(sourceMessage)
		sourceMessageValue = sourceValue
		if documentErr != nil {
			return SendSavedResult{}, fmt.Errorf("prepare source message %d for copy: %w", opts.SourceMessageID, documentErr)
		}
		sourceAudio, err = savedNativeAudioAttribute(sourceDocument)
		if err != nil {
			return SendSavedResult{}, err
		}
		if sourceAudio != nil {
			if len(normalizedSource.Attachments) != 1 || normalizedSource.Attachments[0].Kind != "audio" {
				return SendSavedResult{}, fmt.Errorf("source is not an ordinary native audio document")
			}
			sourceEvidence.DocumentID = sourceDocument.ID
			randomID, randomErr := nonzeroRandomID()
			if randomErr != nil {
				return SendSavedResult{}, randomErr
			}
			updates, err = s.raw.MessagesSendMedia(ctx, savedDocumentReuseRequest(sourceMessageValue, sourceDocument, randomID))
			copyMethod = savedVideoServerReuse
		} else {
			sourceEvidence, err = savedVideoEvidenceFromMessage(sourceMessage)
			if err != nil {
				return SendSavedResult{}, fmt.Errorf("source message %d is not a supported Telegram video or native audio: %w", opts.SourceMessageID, err)
			}

			tempDir, tempErr := os.MkdirTemp("", "telegram-harvest-send-saved-")
			if tempErr != nil {
				return SendSavedResult{}, fmt.Errorf("prepare temporary video transfer: %w", tempErr)
			}
			defer os.RemoveAll(tempDir)
			copyTempDir = tempDir
			var thumbPath string
			if sourceEvidence.HasPreview {
				var previewErr error
				thumbPath, sourcePreviewSHA256, previewErr = s.downloadSavedVideoPreview(ctx, sourceMessageValue, tempDir, "source-preview.jpg")
				if previewErr != nil {
					return SendSavedResult{}, fmt.Errorf("verify source video preview before sending: %w", previewErr)
				}
			}
			randomID, randomErr := nonzeroRandomID()
			if randomErr != nil {
				return SendSavedResult{}, fmt.Errorf("generate copy random id: %w", randomErr)
			}
			request := savedDocumentReuseRequest(sourceMessageValue, sourceDocument, randomID)
			updates, copyMethod, sourceSHA256, err = sendSavedVideoCopy(ctx, request, !sourceMessageValue.Noforwards, s.raw.MessagesSendMedia, func() (tg.InputMediaClass, string, error) {
				if err := validateSavedVideoUploadPreview(sourceDocument); err != nil {
					return nil, "", err
				}
				uploadThumbPath := thumbPath
				if sourceMessageValue.Media.(*tg.MessageMediaDocument).VideoCover != nil {
					staticMessage := &tg.Message{Media: &tg.MessageMediaDocument{Document: sourceDocument}}
					var previewErr error
					uploadThumbPath, _, previewErr = s.downloadSavedVideoPreview(ctx, staticMessage, tempDir, "upload-preview.jpg")
					if previewErr != nil {
						return nil, "", previewErr
					}
				}
				videoPath, hashValue, downloadErr := s.downloadSavedVideoSource(ctx, sourceDocument, tempDir)
				if downloadErr != nil {
					return nil, "", fmt.Errorf("download source message %d for exact upload: %w", opts.SourceMessageID, downloadErr)
				}
				uploadedVideo, uploadErr := builder.Upload(message.FromPath(videoPath)).AsInputFile(ctx)
				if uploadErr != nil {
					return nil, "", fmt.Errorf("upload source video to Saved Messages: %w", uploadErr)
				}
				uploadedThumb, uploadErr := builder.Upload(message.FromPath(uploadThumbPath)).AsInputFile(ctx)
				if uploadErr != nil {
					return nil, "", fmt.Errorf("upload source video preview to Saved Messages: %w", uploadErr)
				}
				return &tg.InputMediaUploadedDocument{
					NosoundVideo: normalizedSource.Attachments[0].NoAudio,
					Spoiler:      sourceMessageValue.Media.(*tg.MessageMediaDocument).Spoiler,
					File:         uploadedVideo, Thumb: uploadedThumb,
					MimeType: sourceDocument.MimeType, Attributes: sourceDocument.Attributes,
				}, hashValue, nil
			})
		}
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
		return SendSavedResult{}, fmt.Errorf("send to Saved Messages failed; outcome may be unknown, do not retry blindly: %w", err)
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
		if sourceAudio != nil {
			if verifyErr := verifyCopiedSavedAudio(record, *sourceRecord, profile, sourceMessageValue, readbackMessage); verifyErr != nil {
				return result, fmt.Errorf("message %d audio copy verification failed; do not retry blindly: %w", messageID, verifyErr)
			}
			result.Copy = &SavedCopyVerification{
				SourceChatID: sourceTarget.Chat.ID, SourceMessageID: opts.SourceMessageID,
				SourceMediaID: sourceRecord.Attachments[0].MediaID, DestinationMediaID: record.Attachments[0].MediaID,
				Method: savedVideoServerReuse, SameServerDocument: true, Audio: true,
			}
		} else {

			destinationEvidence, evidenceErr := savedVideoEvidenceFromMessage(readbackMessage)
			if evidenceErr != nil {
				return result, fmt.Errorf("message %d was copied to Saved Messages but video readback failed; do not retry blindly: %w", messageID, evidenceErr)
			}
			if verifyErr := verifyCopiedSavedMessage(record, *sourceRecord, profile, sourceEvidence, destinationEvidence); verifyErr != nil {
				return result, fmt.Errorf("message %d was copied to Saved Messages but verification failed; do not retry blindly: %w", messageID, verifyErr)
			}
			if verifyErr := verifySavedVideoCopyMethod(sourceMessageValue, readbackMessage, copyMethod, sourceEvidence, destinationEvidence); verifyErr != nil {
				return result, fmt.Errorf("message %d copy verification failed; do not retry blindly: %w", messageID, verifyErr)
			}
			if sourceEvidence.HasPreview {
				_, destinationPreviewHash, previewErr := s.downloadSavedVideoPreview(ctx, readbackMessage, copyTempDir, "destination-preview.jpg")
				if previewErr != nil {
					return result, fmt.Errorf("message %d preview readback failed; do not retry blindly: %w", messageID, previewErr)
				}
				if copyMethod == savedVideoServerReuse && destinationPreviewHash != sourcePreviewSHA256 {
					return result, fmt.Errorf("message %d reused preview bytes changed; do not retry blindly", messageID)
				}
			}
			result.Copy = &SavedCopyVerification{
				SourceChatID:       sourceTarget.Chat.ID,
				SourceMessageID:    opts.SourceMessageID,
				SourceMediaID:      sourceRecord.Attachments[0].MediaID,
				DestinationMediaID: record.Attachments[0].MediaID,
				Method:             copyMethod,
				SourceSHA256:       sourceSHA256,
				SameServerDocument: sourceEvidence.DocumentID == destinationEvidence.DocumentID,
				ExactFileUploaded:  copyMethod == savedVideoExactUpload,
				PreviewPresent:     destinationEvidence.HasPreview,
				PreviewPreserved:   sourceEvidence.HasPreview && destinationEvidence.HasPreview,
				Video:              true,
				SupportsStreaming:  destinationEvidence.SupportsStreaming,
			}
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
	if sourceEvidence.HasPreview && !destinationEvidence.HasPreview {
		return fmt.Errorf("telegram video preview was not preserved")
	}
	if destinationEvidence.SupportsStreaming != sourceEvidence.SupportsStreaming {
		return fmt.Errorf("telegram streaming attribute changed during copy")
	}
	return nil
}

const (
	savedVideoServerReuse = "server_reuse"
	savedVideoExactUpload = "exact_upload"
)

func savedDocumentReuseRequest(source *tg.Message, document *tg.Document, randomID int64) *tg.MessagesSendMediaRequest {
	sourceMedia := source.Media.(*tg.MessageMediaDocument)
	media := &tg.InputMediaDocument{
		ID:      &tg.InputDocument{ID: document.ID, AccessHash: document.AccessHash, FileReference: document.FileReference},
		Spoiler: sourceMedia.Spoiler,
	}
	if cover, ok := sourceMedia.VideoCover.(*tg.Photo); ok {
		media.VideoCover = &tg.InputPhoto{ID: cover.ID, AccessHash: cover.AccessHash, FileReference: cover.FileReference}
	}
	if timestamp, ok := sourceMedia.GetVideoTimestamp(); ok {
		media.SetVideoTimestamp(timestamp)
	}
	return &tg.MessagesSendMediaRequest{
		Peer: &tg.InputPeerSelf{}, Media: media,
		Message: source.Message, Entities: source.Entities, RandomID: randomID,
	}
}

// Only a Telegram RPC rejection of the file reference proves that this request
// did not send. Privacy restrictions and uncertain transport errors never fall
// back to an upload. Keep the same random ID across the definitive rejection.
func sendSavedVideoCopy(ctx context.Context, request *tg.MessagesSendMediaRequest, reuse bool,
	send func(context.Context, *tg.MessagesSendMediaRequest) (tg.UpdatesClass, error),
	upload func() (tg.InputMediaClass, string, error),
) (tg.UpdatesClass, string, string, error) {
	if reuse {
		updates, err := send(ctx, request)
		if err == nil {
			return updates, savedVideoServerReuse, "", nil
		}
		rpcErr, ok := tgerr.As(err)
		if !ok || rpcErr.Code != 400 || (rpcErr.Message != "FILE_REFERENCE_EXPIRED" && rpcErr.Message != "FILE_REFERENCE_INVALID") {
			return nil, "", "", err
		}
	}
	media, hash, err := upload()
	if err != nil {
		return nil, "", "", err
	}
	request.Media = media
	updates, err := send(ctx, request)
	return updates, savedVideoExactUpload, hash, err
}

func verifySavedVideoCopyMethod(source *tg.Message, destination tg.MessageClass, method string, before, after savedVideoEvidence) error {
	message, ok := destination.(*tg.Message)
	if !ok || source.Message != message.Message || !reflect.DeepEqual(source.Entities, message.Entities) {
		return fmt.Errorf("exact caption or entities readback mismatch")
	}
	switch method {
	case savedVideoServerReuse:
		sourceMedia, sourceOK := source.Media.(*tg.MessageMediaDocument)
		destinationMedia, destinationOK := message.Media.(*tg.MessageMediaDocument)
		if sourceOK && destinationOK {
			if cover, ok := sourceMedia.VideoCover.(*tg.Photo); ok {
				copiedCover, ok := destinationMedia.VideoCover.(*tg.Photo)
				if !ok || copiedCover.ID != cover.ID {
					return fmt.Errorf("server reuse changed video cover")
				}
			}
		}
		if before.DocumentID != after.DocumentID {
			return fmt.Errorf("server reuse returned a different document ID")
		}
	case savedVideoExactUpload:
	default:
		return fmt.Errorf("unknown video copy method %q", method)
	}
	return nil
}

func validateSavedVideoUploadPreview(document *tg.Document) error {
	if _, _, ok := savedVideoThumbnailLocation(document); !ok {
		return fmt.Errorf("exact upload requires a downloadable static video thumbnail; no full transfer or send attempted")
	}
	return nil
}

func savedVideoPreviewLocation(message tg.MessageClass) (tg.InputFileLocationClass, int64, bool) {
	value, document, err := savedVideoDocumentFromMessage(message)
	if err != nil {
		return nil, 0, false
	}
	media := value.Media.(*tg.MessageMediaDocument)
	if cover, ok := media.VideoCover.(*tg.Photo); ok {
		thumbSize := bestPhotoThumbSize(cover)
		size := photoThumbSizeBytes(cover, thumbSize)
		if thumbSize == "" || size <= 0 {
			return nil, 0, false
		}
		return cover.AsInputPhotoFileLocation(thumbSize), size, true
	}
	return savedVideoThumbnailLocation(document)
}

func (s *Session) downloadSavedVideoPreview(ctx context.Context, message tg.MessageClass, tempDir, name string) (string, string, error) {
	location, size, ok := savedVideoPreviewLocation(message)
	if !ok {
		return "", "", fmt.Errorf("telegram preview has no downloadable static thumbnail")
	}
	path := filepath.Join(tempDir, name)
	downloadCtx, cancel := context.WithTimeout(ctx, defaultDownloadTimeout)
	defer cancel()
	if err := s.downloadFile(downloadCtx, location, path, size, nil); err != nil {
		return "", "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	config, _, decodeErr := image.DecodeConfig(file)
	closeErr := file.Close()
	if decodeErr != nil {
		return "", "", fmt.Errorf("decode preview: %w", decodeErr)
	}
	if closeErr != nil {
		return "", "", closeErr
	}
	if config.Width <= 0 || config.Height <= 0 {
		return "", "", fmt.Errorf("empty preview dimensions")
	}
	hash, err := fileSHA256(path)
	return path, hash, err
}

func (s *Session) downloadSavedVideoSource(ctx context.Context, document *tg.Document, tempDir string) (string, string, error) {
	fileName := safeFileName(documentFileName(document))
	if fileName == "" {
		return "", "", fmt.Errorf("source filename is missing")
	}
	videoPath := filepath.Join(tempDir, fileName)
	downloadCtx, cancel := context.WithTimeout(ctx, defaultDownloadTimeout)
	defer cancel()
	if err := s.downloadFile(downloadCtx, document.AsInputDocumentFileLocation(""), videoPath, document.Size, nil); err != nil {
		return "", "", fmt.Errorf("video: %w", err)
	}
	hashValue, err := fileSHA256(videoPath)
	if err != nil {
		return "", "", fmt.Errorf("hash downloaded source video: %w", err)
	}
	return videoPath, hashValue, nil
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

// Native audio is reused as a Telegram document, never downloaded or uploaded.
func savedNativeAudioAttribute(document *tg.Document) (*tg.DocumentAttributeAudio, error) {
	for _, attribute := range document.Attributes {
		if audio, ok := attribute.(*tg.DocumentAttributeAudio); ok {
			if audio.Voice {
				return nil, fmt.Errorf("voice messages are not supported by send-saved source copy")
			}
			return audio, nil
		}
	}
	return nil, nil
}

func verifyCopiedSavedAudio(record, source harvest.MessageRecord, profile harvest.SelfProfile, original, copied tg.MessageClass) error {
	sourceValue, sourceDocument, err := savedVideoDocumentFromMessage(original)
	if err != nil {
		return err
	}
	destinationValue, destinationDocument, err := savedVideoDocumentFromMessage(copied)
	if err != nil {
		return err
	}
	if sourceDocument.ID == 0 || sourceDocument.ID != destinationDocument.ID {
		return fmt.Errorf("native audio document ID changed")
	}
	sourceAudio, err := savedNativeAudioAttribute(sourceDocument)
	if err != nil {
		return err
	}
	destinationAudio, err := savedNativeAudioAttribute(destinationDocument)
	if err != nil {
		return err
	}
	if sourceAudio == nil || destinationAudio == nil || sourceAudio.Duration != destinationAudio.Duration || sourceAudio.Title != destinationAudio.Title || sourceAudio.Performer != destinationAudio.Performer {
		return fmt.Errorf("native audio attributes changed")
	}
	if sourceValue.Message != destinationValue.Message || !reflect.DeepEqual(sourceValue.Entities, destinationValue.Entities) {
		return fmt.Errorf("exact audio caption or entities changed")
	}
	if len(source.Attachments) != 1 || source.Attachments[0].Kind != "audio" || len(record.Attachments) != 1 || record.Attachments[0].Kind != "audio" {
		return fmt.Errorf("expected one native audio attachment")
	}
	attachment := source.Attachments[0]
	if attachment.MediaID == "" || record.Attachments[0].MediaID != attachment.MediaID {
		return fmt.Errorf("native audio media ID changed")
	}
	return verifySavedMessage(record, profile, SendSavedOptions{Caption: source.Text}, &savedFileMetadata{Name: attachment.FileName, MIMEType: attachment.MIMEType, Size: attachment.Size})
}
