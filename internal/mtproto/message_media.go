package mtproto

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"github.com/gotd/td/tg"
)

var messageFileMIMEs = map[string]string{
	".pdf": "application/pdf", ".docx": docxMIME, ".xlsx": xlsxMIME, ".pptx": pptxMIME, ".zip": zipMIME,
	".txt": "text/plain", ".md": "text/markdown", ".csv": "text/csv", ".tsv": "text/tab-separated-values", ".json": "application/json",
	".m4a": "audio/mp4", ".mp3": "audio/mpeg", ".wav": "audio/wav", ".aac": "audio/aac", ".flac": "audio/flac", ".ogg": "audio/ogg", ".opus": "audio/ogg",
	".mp4": "video/mp4", ".mov": "video/quicktime", ".mkv": "video/x-matroska", ".webm": "video/webm",
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".gif": "image/gif", ".webp": "image/webp", ".heic": "image/heic", ".heif": "image/heif",
}

func validateMessageFile(f InterviewTopicFile) error {
	ext := strings.ToLower(filepath.Ext(f.Filename))
	if !filepath.IsAbs(f.Path) || f.Filename != filepath.Base(f.Path) || strings.ContainsAny(f.Filename, "\x00\r\n") ||
		f.Size <= 0 || !validSHA256(f.SHA256) || f.MIME == "" || messageFileMIMEs[ext] != f.MIME {
		return fmt.Errorf("file requires an absolute path, exact filename, size, SHA-256 and supported MIME")
	}
	if math.IsNaN(f.Duration) || math.IsInf(f.Duration, 0) {
		return fmt.Errorf("invalid media duration")
	}
	switch f.Kind {
	case "document":
		if f.Duration != 0 || f.Width != 0 || f.Height != 0 || f.HasAudio {
			return fmt.Errorf("a document has no native playback attributes")
		}
	case "audio":
		if (ext != ".m4a" && ext != ".mp3") || !f.HasAudio || f.Duration < 1 || f.Duration != math.Trunc(f.Duration) || f.Width != 0 || f.Height != 0 {
			return fmt.Errorf("native audio requires M4A or MP3 with an audio stream")
		}
	case "video":
		if ext != ".mp4" || f.Duration <= 0 || f.Width <= 0 || f.Height <= 0 {
			return fmt.Errorf("native video requires MP4 with valid dimensions and duration")
		}
	default:
		return fmt.Errorf("file kind must be document, audio or video")
	}
	return nil
}

func prepareMessageFile(ctx context.Context, input MessageFileInput) (InterviewTopicFile, error) {
	path, err := filepath.Abs(input.Path)
	if err != nil {
		return InterviewTopicFile{}, err
	}
	f := InterviewTopicFile{Path: path, Filename: filepath.Base(path), Kind: input.Kind}
	if f.Kind == "" {
		f.Kind = "document"
	}
	f.MIME = messageFileMIMEs[strings.ToLower(filepath.Ext(f.Filename))]
	if f.MIME == "" {
		return f, fmt.Errorf("unsupported file extension: %s", filepath.Ext(f.Filename))
	}
	info, err := os.Stat(path)
	if err != nil {
		return f, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return f, fmt.Errorf("file must be regular and nonempty")
	}
	source, err := os.Open(path)
	if err != nil {
		return f, err
	}
	defer source.Close()
	sourceInfo, err := source.Stat()
	if err != nil {
		return f, err
	}
	if !os.SameFile(info, sourceInfo) || sourceInfo.Size() != info.Size() {
		return f, fmt.Errorf("source file changed during preparation")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(source, info.Size()+1))
	if err != nil {
		return f, err
	}
	if n != info.Size() {
		return f, fmt.Errorf("source file size changed during preparation")
	}
	f.SHA256, f.Size = hex.EncodeToString(h.Sum(nil)), n
	var probe localPublishMedia
	if f.Kind == "audio" || f.Kind == "video" {
		probe, err = probeLocalPublishMedia(ctx, path)
		if err != nil {
			return f, err
		}
		f.Duration, f.Width, f.Height, f.HasAudio = probe.Duration, probe.Width, probe.Height, probe.HasAudio
		if f.Kind == "audio" {
			f.Duration = math.Ceil(f.Duration)
		}
	}
	if err := validateMessageFile(f); err != nil {
		return f, err
	}
	if f.Kind == "document" {
		err = verifyLocalPublicationDocument(source, f.Size, f.MIME)
	} else {
		err = verifyPublicationMediaProbe(&f, probe)
	}
	return f, err
}

func verifyDeliveryDocument(media tg.MessageMediaClass, file InterviewTopicFile) (*tg.Document, error) {
	if message, ok := media.(*tg.MessageMediaDocument); ok {
		if doc, ok := message.Document.(*tg.Document); ok {
			name := documentFileName(doc)
			// Reuse the observed Telegram filename transforms, with a mandatory byte check.
			if name == strings.ReplaceAll(file.Filename, " ", "_") ||
				name == strings.NewReplacer(" ", "_", "-", "_").Replace(file.Filename) {
				file.Filename = name
			}
		}
	}
	return verifyPublishedDocument(media, &file, true)
}

func (s *Session) verifyDeliveryFileBytes(ctx context.Context, doc *tg.Document, file InterviewTopicFile) error {
	hash := sha256.New()
	writer := &boundedPublicationWriter{writer: hash, remaining: file.Size}
	if err := s.downloadPublishedDocument(ctx, doc, writer, "message_delivery"); err != nil {
		return fmt.Errorf("verify server document download: %w", err)
	}
	if writer.remaining != 0 || hex.EncodeToString(hash.Sum(nil)) != file.SHA256 {
		return fmt.Errorf("server document bytes differ from reviewed size or SHA-256")
	}
	return nil
}

// Telegram may recognize text as links or hashtags on its own. These entities
// only decorate existing text, and may not replace explicit styling or URLs.
func matchesDeliveryEntities(actual, expected []tg.MessageEntityClass, text string) bool {
	units := utf16.Encode([]rune(text))
	for _, entity := range actual {
		if len(expected) > 0 && sameTopicEntities([]tg.MessageEntityClass{entity}, expected[:1]) {
			expected = expected[1:]
			continue
		}
		switch entity.(type) {
		case *tg.MessageEntityURL, *tg.MessageEntityEmail, *tg.MessageEntityPhone, *tg.MessageEntityHashtag,
			*tg.MessageEntityMention, *tg.MessageEntityCashtag, *tg.MessageEntityBotCommand, *tg.MessageEntityBankCard:
			bounds := entity.(interface {
				GetOffset() int
				GetLength() int
			})
			start, length := bounds.GetOffset(), bounds.GetLength()
			if start < 0 || start > len(units) || length <= 0 || length > len(units)-start {
				return false
			}
			end := start + length
			if (start < len(units) && units[start] >= 0xdc00 && units[start] <= 0xdfff) ||
				(end < len(units) && units[end] >= 0xdc00 && units[end] <= 0xdfff) {
				return false
			}
		default:
			return false
		}
	}
	return len(expected) == 0
}
