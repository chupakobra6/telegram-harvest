package mtproto

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/message/unpack"
	"github.com/gotd/td/tg"
)

const (
	PrintShopChannelID = int64(4415343946)
	PrintShopArinaID   = int64(1221157785)
	PrintShopTitle     = "Игорь × Арина · Печатный цех"
)

// The manifest cannot select a recipient, reply, topic or a different media kind.
type PrintShopPublishManifest struct {
	Profile   string          `json:"profile"`
	AccountID int64           `json:"account_id"`
	ChannelID int64           `json:"channel_id"`
	Posts     []PrintShopPost `json:"posts"`
}
type PrintShopPost struct {
	Key      string                 `json:"key"`
	Text     string                 `json:"text"`
	Entities []InterviewTopicEntity `json:"entities,omitempty"`
	File     *PrintShopPDF          `json:"file,omitempty"`
}
type PrintShopPDF struct {
	Duration float64 `json:"duration,omitempty"`
	Width    int     `json:"width,omitempty"`
	Height   int     `json:"height,omitempty"`
	HasAudio bool    `json:"has_audio,omitempty"`
	Path     string  `json:"path"`
	SHA256   string  `json:"sha256"`
	Filename string  `json:"filename"`
	MIME     string  `json:"mime"`
	Size     int64   `json:"size"`
}
type PrintShopPublishRow struct {
	Post               PrintShopPost          `json:"post"`
	EffectiveEntities  []InterviewTopicEntity `json:"effective_entities"`
	ID                 int                    `json:"id,omitempty"`
	Status             string                 `json:"status"`
	ServerVerified     bool                   `json:"server_verified"`
	OutcomeUnknown     bool                   `json:"outcome_unknown"`
	ServerFilename     string                 `json:"server_filename,omitempty"`
	FilenameNormalized bool                   `json:"filename_normalized"`
}
type PrintShopPublishResult struct {
	AccountID      int64                 `json:"account_id"`
	Username       string                `json:"username"`
	ChannelID      int64                 `json:"channel_id"`
	Title          string                `json:"title"`
	ParticipantIDs []int64               `json:"participant_ids,omitempty"`
	DryRun         bool                  `json:"dry_run"`
	Ready          bool                  `json:"ready"`
	Complete       bool                  `json:"complete"`
	Rows           []PrintShopPublishRow `json:"rows"`
}

func (p PrintShopPost) publicationPost() InterviewTopicPost {
	post := InterviewTopicPost{Key: p.Key, Text: p.Text, Entities: p.Entities}
	if p.File != nil {
		f := p.File
		kind := "document"
		if f.MIME == "video/mp4" {
			kind = "video"
		}
		post.File = &InterviewTopicFile{Path: f.Path, SHA256: f.SHA256, Filename: f.Filename, MIME: f.MIME, Size: f.Size, Kind: kind, Duration: f.Duration, Width: f.Width, Height: f.Height, HasAudio: f.HasAudio}
	}
	return post
}

// Only unformatted plain-text posts get literal whitespace-delimited HTTP(S)
// tokens. The authored post is never mutated: receipts hash the original payload.
func (p PrintShopPost) effectiveEntities() []InterviewTopicEntity {
	result := append([]InterviewTopicEntity{}, p.Entities...)
	if p.File != nil || len(p.Entities) != 0 {
		return result
	}
	start := -1
	for i, r := range p.Text {
		if unicode.IsSpace(r) {
			if start >= 0 {
				result = appendPrintShopURL(result, p.Text, start, i)
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		result = appendPrintShopURL(result, p.Text, start, len(p.Text))
	}
	return result
}

func appendPrintShopURL(entities []InterviewTopicEntity, text string, start, end int) []InterviewTopicEntity {
	token := text[start:end]
	parsed, err := url.ParseRequestURI(token)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return entities
	}
	return append(entities, InterviewTopicEntity{Type: "url", Offset: len(utf16.Encode([]rune(text[:start]))), Length: len(utf16.Encode([]rune(token)))})
}

func printShopRandomID(key string) int64 {
	h := sha256.Sum256([]byte("print-shop-publish/main/4415343946/" + key))
	n := int64(binary.BigEndian.Uint64(h[:8]) & math.MaxInt64)
	if n == 0 {
		return 1
	}
	return n
}
func ValidatePrintShopPublishManifest(m PrintShopPublishManifest) error {
	if m.Profile != "main" || m.AccountID != InterviewTopicAccountID || m.ChannelID != PrintShopChannelID {
		return fmt.Errorf("print-shop-publish is bound to main/@Pheik13 and channel %d", PrintShopChannelID)
	}
	if len(m.Posts) < 1 || len(m.Posts) > 20 {
		return fmt.Errorf("provide 1..20 ordered print-shop posts")
	}
	seen := map[string]bool{}
	for _, p := range m.Posts {
		if p.Key == "" || len(p.Key) > 100 || strings.ContainsAny(p.Key, "/\\ \t\r\n") || seen[p.Key] {
			return fmt.Errorf("invalid or duplicate post key")
		}
		seen[p.Key] = true
		limit := 4096
		if p.File != nil {
			limit = 1024
		}
		if !utf8.ValidString(p.Text) || strings.TrimSpace(p.Text) == "" || len(utf16.Encode([]rune(p.Text))) > limit {
			return fmt.Errorf("invalid print-shop text/caption")
		}
		if _, err := topicEntities(p.Text, p.Entities); err != nil {
			return err
		}
		if f := p.File; f != nil {
			if !filepath.IsAbs(f.Path) || !validSHA256(f.SHA256) || f.Size <= 0 || f.Filename != filepath.Base(f.Path) || f.Filename != filepath.Base(f.Filename) {
				return fmt.Errorf("print-shop file requires absolute path, exact filename, size and SHA-256")
			}
			ext := strings.ToLower(filepath.Ext(f.Filename))
			switch {
			case f.MIME == "application/pdf" && ext == ".pdf" && f.Duration == 0 && f.Width == 0 && f.Height == 0 && !f.HasAudio:
			case f.MIME == "video/mp4" && ext == ".mp4" && f.Duration > 0 && !math.IsInf(f.Duration, 0) && f.Width > 0 && f.Height > 0:
			default:
				return fmt.Errorf("print-shop file must be PDF or MP4 with exact video metadata")
			}
		}
	}
	return nil
}

type printShopPublishSource interface {
	SelfProfile(context.Context) (harvest.SelfProfile, error)
	printShopPrepare(context.Context) ([]int64, error)
	printShopRead(context.Context, int) (*tg.Message, error)
	printShopExisting(context.Context, PrintShopPost) (bool, error)
	printShopSend(context.Context, PrintShopPost, int64) (int, error)
	printShopDownloadFile(context.Context, *tg.Document, io.Writer) error
}

func checkPrintShopIdentity(ctx context.Context, s printShopPublishSource) ([]int64, error) {
	self, err := s.SelfProfile(ctx)
	if err != nil {
		return nil, err
	}
	if self.ID != InterviewTopicAccountID || !strings.EqualFold(self.Username, SavedMessagesMainUsername) {
		return nil, fmt.Errorf("active account differs from main/@Pheik13")
	}
	ids, err := s.printShopPrepare(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	ids = append([]int64(nil), ids...)
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return nil, fmt.Errorf("invalid or duplicate print-shop participant")
		}
		seen[id] = true
	}
	if !seen[InterviewTopicAccountID] || !seen[PrintShopArinaID] {
		return nil, fmt.Errorf("print-shop must contain Igor and Arina")
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}
func verifyPrintShopPost(m *tg.Message, p PrintShopPost) error {
	if m == nil || !m.Out {
		return fmt.Errorf("own print-shop message missing")
	}
	from, ok := m.FromID.(*tg.PeerUser)
	if !ok || from.UserID != InterviewTopicAccountID {
		return fmt.Errorf("print-shop sender mismatch")
	}
	peer, ok := m.PeerID.(*tg.PeerChannel)
	if !ok || peer.ChannelID != PrintShopChannelID {
		return fmt.Errorf("print-shop destination mismatch")
	}
	if m.ReplyTo != nil || m.GroupedID != 0 {
		return fmt.Errorf("unexpected print-shop reply/topic")
	}
	expected := p.publicationPost()
	expected.Entities = p.effectiveEntities()
	if p.File != nil {
		if media, ok := m.Media.(*tg.MessageMediaDocument); ok {
			if document, ok := media.Document.(*tg.Document); ok {
				name := documentFileName(document)
				if name == strings.ReplaceAll(p.File.Filename, " ", "_") || name == strings.NewReplacer(" ", "_", "-", "_").Replace(p.File.Filename) {
					expected.File.Filename = name
				}
			}
		}
	}
	entities, err := topicEntities(expected.Text, expected.Entities)
	if err != nil {
		return err
	}
	if m.Message != expected.Text {
		return fmt.Errorf("print-shop text mismatch")
	}
	if !sameTopicEntities(m.Entities, entities) {
		return fmt.Errorf("print-shop entities mismatch: server=%s expected=%s", printShopEntityShapes(m.Entities), printShopEntityShapes(entities))
	}
	return verifyPublishedContent(m, expected)
}

// Entity diagnostics deliberately exclude URL values and any media access fields.
func printShopEntityShapes(entities []tg.MessageEntityClass) string {
	shapes := make([]string, 0, len(entities))
	for _, entity := range entities {
		if bounds, ok := entity.(interface {
			GetOffset() int
			GetLength() int
		}); ok {
			shapes = append(shapes, fmt.Sprintf("%T(%d,%d)", entity, bounds.GetOffset(), bounds.GetLength()))
		} else {
			shapes = append(shapes, fmt.Sprintf("%T", entity))
		}
	}
	return "[" + strings.Join(shapes, ", ") + "]"
}

// Stream uses one worker; never permit server bytes beyond the reviewed file size.
type boundedPrintShopWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *boundedPrintShopWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, fmt.Errorf("print-shop file exceeds reviewed size")
	}
	n, err := w.writer.Write(p)
	w.remaining -= int64(n)
	return n, err
}
func verifyPrintShopReadback(ctx context.Context, s printShopPublishSource, m *tg.Message, p PrintShopPost) (err error) {
	if err = verifyPrintShopPost(m, p); err != nil {
		return err
	}
	if p.File == nil {
		return nil
	}
	// Use exactly the document returned and validated above, not a second message lookup.
	document := m.Media.(*tg.MessageMediaDocument).Document.(*tg.Document)
	dir, err := os.MkdirTemp("", "print-shop-readback-*")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	path := filepath.Join(dir, p.File.Filename)
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	writer := &boundedPrintShopWriter{writer: file, remaining: p.File.Size}
	downloadErr := s.printShopDownloadFile(ctx, document, writer)
	closeErr := file.Close()
	if err = errors.Join(downloadErr, closeErr); err != nil {
		return fmt.Errorf("verify print-shop file download: %w", err)
	}
	expected := p.publicationPost().File
	expected.Path = path
	if err = verifyPublishFile(ctx, expected); err != nil {
		return fmt.Errorf("server file bytes differ from reviewed local file: %w", err)
	}
	return nil
}

func publishPrintShop(ctx context.Context, s printShopPublishSource, m PrintShopPublishManifest, j topicPublishJournal, save func() error, apply bool) (PrintShopPublishResult, error) {
	r := PrintShopPublishResult{AccountID: InterviewTopicAccountID, Username: SavedMessagesMainUsername, ChannelID: PrintShopChannelID, Title: PrintShopTitle, DryRun: !apply, Rows: []PrintShopPublishRow{}}
	if err := ValidatePrintShopPublishManifest(m); err != nil {
		return r, err
	}
	audience, err := checkPrintShopIdentity(ctx, s)
	if err != nil {
		return r, err
	}
	r.ParticipantIDs = audience
	checkIdentity := func() error {
		ids, err := checkPrintShopIdentity(ctx, s)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(ids, audience) {
			return fmt.Errorf("print-shop audience changed during publication")
		}
		return nil
	}
	// Keep readback metadata only after all checks, including the downloaded hash.
	verifiedNames := map[string]string{}
	// Reject every bad local input/receipt and pre-existing match before any upload.
	for _, p := range m.Posts {
		if err = verifyPublishFile(ctx, p.publicationPost().File); err != nil {
			return r, fmt.Errorf("%s: %w", p.Key, err)
		}
		receipt, known := j[p.Key]
		if known {
			if receipt.Hash != publishPostHash(p.publicationPost()) {
				return r, fmt.Errorf("post key payload changed: %s", p.Key)
			}
			if receipt.ID == 0 {
				r.Rows = append(r.Rows, PrintShopPublishRow{Post: p, EffectiveEntities: p.effectiveEntities(), Status: "outcome_unknown", OutcomeUnknown: true})
				return r, fmt.Errorf("outcome_unknown for %s; reconcile server manually; do not retry", p.Key)
			}
			msg, e := s.printShopRead(ctx, receipt.ID)
			if e != nil {
				return r, e
			}
			if msg == nil || msg.ID != receipt.ID {
				return r, fmt.Errorf("print-shop message ID mismatch")
			}
			if e = verifyPrintShopReadback(ctx, s, msg, p); e != nil {
				return r, e
			}
			if p.File != nil {
				verifiedNames[p.Key] = documentFileName(msg.Media.(*tg.MessageMediaDocument).Document.(*tg.Document))
			}
		} else {
			exists, e := s.printShopExisting(ctx, p)
			if e != nil {
				return r, e
			}
			if exists {
				return r, fmt.Errorf("matching server post exists for %s without receipt; reconcile before sending", p.Key)
			}
		}
	}
	for _, p := range m.Posts {
		receipt, known := j[p.Key]
		row := PrintShopPublishRow{Post: p, EffectiveEntities: p.effectiveEntities(), ID: receipt.ID, Status: "would_send"}
		if known {
			// Already read and byte-verified in the all-input preflight; do not download twice.
			if apply && receipt.Pending {
				receipt.Pending = false
				j[p.Key] = receipt
				if err = save(); err != nil {
					return r, err
				}
			}
			row.Status = "already_applied"
			row.ServerVerified = true
			if p.File != nil {
				row.ServerFilename = verifiedNames[p.Key]
				row.FilenameNormalized = row.ServerFilename != p.File.Filename
			}
			r.Rows = append(r.Rows, row)
			continue
		}
		if !apply {
			r.Rows = append(r.Rows, row)
			continue
		}
		if err = checkIdentity(); err != nil {
			return r, err
		}
		if err = verifyPublishFile(ctx, p.publicationPost().File); err != nil {
			return r, err
		}
		exists, e := s.printShopExisting(ctx, p)
		if e != nil {
			return r, e
		}
		if exists {
			return r, fmt.Errorf("matching server post exists without receipt; reconcile before sending")
		}
		j[p.Key] = topicPublishReceipt{Hash: publishPostHash(p.publicationPost()), Pending: true}
		if err = save(); err != nil {
			return r, err
		}
		id, sendErr := s.printShopSend(ctx, p, printShopRandomID(p.Key))
		row.ID = id
		row.Status = "outcome_unknown"
		row.OutcomeUnknown = true
		if id > 0 {
			j[p.Key] = topicPublishReceipt{Hash: publishPostHash(p.publicationPost()), ID: id, Pending: true}
			if err = save(); err != nil {
				r.Rows = append(r.Rows, row)
				return r, err
			}
			msg, readErr := s.printShopRead(ctx, id)
			if readErr == nil {
				if msg == nil || msg.ID != id {
					readErr = fmt.Errorf("print-shop message ID mismatch")
				} else {
					readErr = verifyPrintShopReadback(ctx, s, msg, p)
				}
			}
			if readErr == nil {
				row.Status = "sent_verified"
				row.ServerVerified = true
				row.OutcomeUnknown = false
				if p.File != nil {
					row.ServerFilename = documentFileName(msg.Media.(*tg.MessageMediaDocument).Document.(*tg.Document))
					row.FilenameNormalized = row.ServerFilename != p.File.Filename
				}
				j[p.Key] = topicPublishReceipt{Hash: publishPostHash(p.publicationPost()), ID: id}
				if err = save(); err != nil {
					r.Rows = append(r.Rows, row)
					return r, err
				}
			} else {
				sendErr = errors.Join(sendErr, readErr)
			}
		}
		r.Rows = append(r.Rows, row)
		if !row.ServerVerified {
			return r, fmt.Errorf("outcome_unknown for %s; no retry: %w", p.Key, sendErr)
		}
	}
	r.Ready = true
	r.Complete = apply
	return r, nil
}

type printShopSession struct {
	*Session
	target   resolvedTarget
	audience []int64
}

func (s *Session) PublishPrintShop(ctx context.Context, m PrintShopPublishManifest, journalPath string, apply bool) (PrintShopPublishResult, error) {
	j, err := loadPublicationJournal(journalPath, 0)
	if err != nil {
		return PrintShopPublishResult{}, err
	}
	return publishPrintShop(ctx, &printShopSession{Session: s}, m, j, func() error { return savePublishJournal(journalPath, j) }, apply)
}
func (s *printShopSession) printShopPrepare(ctx context.Context) ([]int64, error) {
	target, err := s.resolveTarget(ctx, "supergroup:4415343946")
	if err != nil {
		return nil, err
	}
	peer, ok := target.InputPeer.(*tg.InputPeerChannel)
	if !ok || peer.ChannelID != PrintShopChannelID || target.Chat.ID != PrintShopChannelID || target.Chat.Type != "supergroup" || target.Chat.Title != PrintShopTitle {
		return nil, fmt.Errorf("fixed print-shop group ID/type/title not proven")
	}
	var full *tg.MessagesChatFull
	err = s.performRPC(ctx, "check_print_shop_group", func(ctx context.Context) error {
		var e error
		full, e = s.raw.ChannelsGetFullChannel(ctx, &tg.InputChannel{ChannelID: peer.ChannelID, AccessHash: peer.AccessHash})
		return e
	})
	if err != nil {
		return nil, err
	}
	validGroup := false
	if full != nil {
		for _, chat := range full.Chats {
			if c, ok := chat.(*tg.Channel); ok && c.ID == PrintShopChannelID && c.Title == PrintShopTitle && c.Megagroup && !c.Broadcast && !c.Left && !c.Forum {
				validGroup = true
			}
		}
	}
	if !validGroup {
		return nil, fmt.Errorf("fresh print-shop group ID/title/non-forum membership not proven")
	}
	var result tg.ChannelsChannelParticipantsClass
	err = s.performRPC(ctx, "check_print_shop_participants", func(ctx context.Context) error {
		var e error
		result, e = s.raw.ChannelsGetParticipants(ctx, &tg.ChannelsGetParticipantsRequest{Channel: &tg.InputChannel{ChannelID: peer.ChannelID, AccessHash: peer.AccessHash}, Filter: &tg.ChannelParticipantsRecent{}, Limit: 100})
		return e
	})
	if err != nil {
		return nil, err
	}
	participants, ok := result.(*tg.ChannelsChannelParticipants)
	if !ok || participants.Count != len(participants.Participants) {
		return nil, fmt.Errorf("complete print-shop participant list not returned")
	}
	ids := []int64{}
	for _, p := range participants.Participants {
		switch p := p.(type) {
		case *tg.ChannelParticipant:
			ids = append(ids, p.UserID)
		case *tg.ChannelParticipantSelf:
			ids = append(ids, p.UserID)
		case *tg.ChannelParticipantCreator:
			ids = append(ids, p.UserID)
		case *tg.ChannelParticipantAdmin:
			ids = append(ids, p.UserID)
		default:
			return nil, fmt.Errorf("unexpected print-shop participant type")
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if s.audience != nil && !reflect.DeepEqual(ids, s.audience) {
		return nil, fmt.Errorf("print-shop audience changed during publication")
	}
	s.target = target
	s.audience = append([]int64(nil), ids...)
	return ids, nil
}
func (s *printShopSession) printShopRead(ctx context.Context, id int) (*tg.Message, error) {
	return s.Session.readPublicationMessage(ctx, s.target, id)
}
func (s *printShopSession) printShopExisting(ctx context.Context, p PrintShopPost) (bool, error) {
	offset := 0
	for {
		var result tg.MessagesMessagesClass
		req := &tg.MessagesSearchRequest{Peer: s.target.InputPeer, Q: p.Text, Filter: &tg.InputMessagesFilterEmpty{}, Limit: 100, OffsetID: offset}
		req.SetFromID(&tg.InputPeerSelf{})
		err := s.performRPC(ctx, "check_existing_print_shop_post", func(ctx context.Context) error { var e error; result, e = s.raw.MessagesSearch(ctx, req); return e })
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
				if verifyPrintShopPost(msg, p) == nil {
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
func (s *printShopSession) printShopSend(ctx context.Context, p PrintShopPost, randomID int64) (int, error) {
	entities, err := topicEntities(p.Text, p.effectiveEntities())
	if err != nil {
		return 0, err
	}
	// Check account, audience and local bytes immediately before any upload.
	if _, err = checkPrintShopIdentity(ctx, s); err != nil {
		return 0, err
	}
	if err = verifyPublishFile(ctx, p.publicationPost().File); err != nil {
		return 0, err
	}
	var media tg.InputMediaClass
	if p.File != nil {
		media, err = s.uploadPublishFile(ctx, p.publicationPost().File)
		if err != nil {
			return 0, err
		}
	}
	if _, err = checkPrintShopIdentity(ctx, s); err != nil {
		return 0, err
	}
	if err = verifyPublishFile(ctx, p.publicationPost().File); err != nil {
		return 0, err
	}
	if err = s.beforeRPC(ctx, "publish_print_shop_post"); err != nil {
		return 0, err
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultRPCTimeout)
	defer cancel()
	var updates tg.UpdatesClass
	if media == nil {
		updates, err = s.raw.MessagesSendMessage(callCtx, &tg.MessagesSendMessageRequest{Peer: s.target.InputPeer, Message: p.Text, Entities: entities, RandomID: randomID, NoWebpage: true})
	} else {
		updates, err = s.raw.MessagesSendMedia(callCtx, &tg.MessagesSendMediaRequest{Peer: s.target.InputPeer, Message: p.Text, Entities: entities, RandomID: randomID, Media: media})
	}
	if err != nil {
		return 0, err
	}
	return unpack.MessageID(updates, nil)
}

func (s *printShopSession) printShopDownloadFile(ctx context.Context, document *tg.Document, output io.Writer) error {
	if s.client == nil {
		return fmt.Errorf("print-shop file downloader is unavailable")
	}
	// The CLI owns the session/runtime lock and runs no concurrent history. Reuse the
	// normal media coordinator reservation and client downloader (including DC handling).
	coordinator := newDownloadCoordinator(nil, func(ctx context.Context) error { return s.beforeRPC(ctx, "download_print_shop_file_readback") })
	defer coordinator.finish()
	var downloadErr error
	task := &mediaDownloadTask{Slots: 1,
		Transfer: func(ctx context.Context) {
			downloadCtx, cancel := context.WithTimeout(ctx, defaultDownloadTimeout)
			defer cancel()
			_, downloadErr = s.client.Download(document.AsInputDocumentFileLocation("")).WithThreads(1).
				WithRetryHandler(func(event downloader.RetryEvent) {
					s.observeDownloadError("download_print_shop_file_retry", event.Err, nil, nil)
				}).
				Stream(downloadCtx, output)
		},
		Fail: func(err error) { downloadErr = err }, Cancel: func(err error) { downloadErr = err },
	}
	runErr := coordinator.runBatch(ctx, []*mediaDownloadTask{task}, nil)
	return errors.Join(runErr, downloadErr)
}
