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
	GroupStart         int                    `json:"group_start,omitempty"`
	GroupedID          int64                  `json:"grouped_id,string,omitempty"`
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
	printShopSendGroup(context.Context, []PrintShopPost, func() error) ([]int, error)
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
func verifyPrintShopMessage(m *tg.Message, p PrintShopPost, grouped bool) error {
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
	if m.ReplyTo != nil || (m.GroupedID != 0) != grouped {
		return fmt.Errorf("unexpected print-shop reply/topic or grouping")
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
	// Telegram may clear Nosound on a silent print-shop video. Readback also
	// downloads exact bytes and probes them for the declared absence of audio.
	return verifyPublishedContentWithSilentVideoNormalization(m, expected, true)
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

func verifyPrintShopMessageAndBytes(ctx context.Context, s printShopPublishSource, m *tg.Message, p PrintShopPost, grouped bool) (err error) {
	if err = verifyPrintShopMessage(m, p, grouped); err != nil {
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
	writer := &boundedPublicationWriter{writer: file, remaining: p.File.Size}
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
	batches := orderedAttachmentBatches(len(m.Posts), func(i int) string {
		p := m.Posts[i]
		if receipt, known := j[p.Key]; known {
			return receipt.Batch
		}
		if p.File == nil {
			return ""
		}
		return p.File.MIME
	})
	rows := make([]PrintShopPublishRow, len(m.Posts))
	for _, batch := range batches {
		posts := m.Posts[batch.start:batch.end]
		batchHash := printShopBatchHash(posts)
		var readErr error
		for i, p := range posts {
			row := &rows[batch.start+i]
			*row = PrintShopPublishRow{Post: p, EffectiveEntities: p.effectiveEntities(), Status: "would_send"}
			if batchHash != "" {
				row.GroupStart = batch.start + 1
			}
			if err = verifyPublishFile(ctx, p.publicationPost().File); err != nil {
				return r, fmt.Errorf("%s: %w", p.Key, err)
			}
			receipt, known := j[p.Key]
			if !known {
				if err = checkPrintShopNotSent(ctx, s, p); err != nil {
					return r, err
				}
				continue
			}
			if receipt.Hash != publishPostHash(p.publicationPost()) {
				return r, fmt.Errorf("post key payload changed: %s", p.Key)
			}
			if receipt.Batch != batchHash {
				return r, fmt.Errorf("print-shop group order or membership changed: %s", p.Key)
			}
			row.ID = receipt.ID
			readErr = errors.Join(readErr, readPrintShopRow(ctx, s, row))
			row.Status = "already_applied"
		}
		group := rows[batch.start:batch.end]
		if err = errors.Join(readErr, verifyPrintShopGroup(group)); err != nil {
			markPrintShopOutcomeUnknown(group)
			r.Rows = append(r.Rows, rows[:batch.end]...)
			return r, err
		}
	}
	for _, batch := range batches {
		group := rows[batch.start:batch.end]
		if apply && !group[0].ServerVerified {
			if err = checkIdentity(); err != nil {
				return r, err
			}
			err = sendPrintShopBatch(ctx, s, group, j, save)
		} else if apply {
			for _, row := range group {
				if receipt := j[row.Post.Key]; receipt.Pending {
					receipt.Pending = false
					j[row.Post.Key] = receipt
					err = save()
					if err != nil {
						return r, err
					}
				}
			}
		}
		r.Rows = append(r.Rows, group...)
		if err != nil {
			return r, err
		}
	}
	r.Ready = true
	r.Complete = apply
	return r, nil
}

func printShopBatchHash(posts []PrintShopPost) string {
	if len(posts) == 1 {
		return ""
	}
	keys := make([]string, len(posts))
	for i, post := range posts {
		keys[i] = post.Key
	}
	return topicTextHash(strings.Join(keys, "\n"))
}

func checkPrintShopNotSent(ctx context.Context, s printShopPublishSource, p PrintShopPost) error {
	exists, err := s.printShopExisting(ctx, p)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("matching server post exists for %s without receipt; reconcile before sending", p.Key)
	}
	return nil
}

func readPrintShopRow(ctx context.Context, s printShopPublishSource, row *PrintShopPublishRow) error {
	if row.ID == 0 {
		return fmt.Errorf("outcome_unknown for %s: server message ID is missing", row.Post.Key)
	}
	msg, err := s.printShopRead(ctx, row.ID)
	if err != nil {
		return err
	}
	if msg == nil || msg.ID != row.ID {
		return fmt.Errorf("print-shop message ID mismatch")
	}
	if err = verifyPrintShopMessageAndBytes(ctx, s, msg, row.Post, row.GroupStart != 0); err != nil {
		return err
	}
	row.GroupedID = msg.GroupedID
	row.ServerVerified = true
	row.OutcomeUnknown = false
	if row.Post.File != nil {
		row.ServerFilename = documentFileName(msg.Media.(*tg.MessageMediaDocument).Document.(*tg.Document))
		row.FilenameNormalized = row.ServerFilename != row.Post.File.Filename
	}
	return nil
}

func verifyPrintShopGroup(rows []PrintShopPublishRow) error {
	evidence := make([]MessageDeliveryRow, len(rows))
	for i, row := range rows {
		evidence[i] = MessageDeliveryRow{
			Index: row.GroupStart + i, GroupedID: row.GroupedID, MessageID: row.ID, ServerVerified: row.ServerVerified,
		}
	}
	return verifyMessageDeliveryBatch(evidence)
}

func markPrintShopOutcomeUnknown(rows []PrintShopPublishRow) {
	for i := range rows {
		rows[i].Status = "outcome_unknown"
		rows[i].ServerVerified = false
		rows[i].OutcomeUnknown = true
	}
}

func sendPrintShopBatch(ctx context.Context, s printShopPublishSource, rows []PrintShopPublishRow, j topicPublishJournal, save func() error) error {
	posts := make([]PrintShopPost, len(rows))
	for i, row := range rows {
		p := row.Post
		posts[i] = p
		if err := verifyPublishFile(ctx, p.publicationPost().File); err != nil {
			return err
		}
		if err := checkPrintShopNotSent(ctx, s, p); err != nil {
			return err
		}
	}
	batchHash := printShopBatchHash(posts)
	started := false
	recordIntent := func() error {
		for _, p := range posts {
			j[p.Key] = topicPublishReceipt{Hash: publishPostHash(p.publicationPost()), Pending: true, Batch: batchHash}
		}
		if err := save(); err != nil {
			return err
		}
		started = true
		for i := range rows {
			rows[i].Status = "outcome_unknown"
			rows[i].OutcomeUnknown = true
		}
		return nil
	}
	var ids []int
	var sendErr error
	if len(posts) == 1 {
		if err := recordIntent(); err != nil {
			return err
		}
		var id int
		id, sendErr = s.printShopSend(ctx, posts[0], printShopRandomID(posts[0].Key))
		ids = []int{id}
	} else {
		ids, sendErr = s.printShopSendGroup(ctx, posts, recordIntent)
		if !started {
			return sendErr
		}
	}
	for i, id := range ids {
		rows[i].ID = id
		receipt := j[posts[i].Key]
		receipt.ID = id
		j[posts[i].Key] = receipt
	}
	if err := save(); err != nil {
		return err
	}
	var readErr error
	for i := range rows {
		if err := readPrintShopRow(ctx, s, &rows[i]); err != nil {
			readErr = errors.Join(readErr, err)
		}
	}
	if err := errors.Join(readErr, verifyPrintShopGroup(rows)); err != nil {
		markPrintShopOutcomeUnknown(rows)
		return fmt.Errorf("outcome_unknown for %s; no retry: %w", posts[0].Key, errors.Join(sendErr, err))
	}
	for i, p := range posts {
		receipt := j[p.Key]
		receipt.Pending = false
		j[p.Key] = receipt
		rows[i].Status = "sent_verified"
	}
	return save()
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
				if (msg.GroupedID == 0 || p.File != nil) && verifyPrintShopMessage(msg, p, msg.GroupedID != 0) == nil {
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
		updates, err = s.raw.MessagesSendMessage(callCtx, &tg.MessagesSendMessageRequest{Peer: s.target.InputPeer, SendAs: &tg.InputPeerSelf{}, Message: p.Text, Entities: entities, RandomID: randomID, NoWebpage: true})
	} else {
		updates, err = s.raw.MessagesSendMedia(callCtx, &tg.MessagesSendMediaRequest{Peer: s.target.InputPeer, SendAs: &tg.InputPeerSelf{}, Message: p.Text, Entities: entities, RandomID: randomID, Media: media})
	}
	if err != nil {
		return 0, err
	}
	return unpack.MessageID(updates, nil)
}

func (s *printShopSession) printShopSendGroup(ctx context.Context, posts []PrintShopPost, recordIntent func() error) ([]int, error) {
	req := &tg.MessagesSendMultiMediaRequest{SendAs: &tg.InputPeerSelf{}, MultiMedia: make([]tg.InputSingleMedia, len(posts))}
	for i, p := range posts {
		entities, err := topicEntities(p.Text, p.effectiveEntities())
		if err != nil {
			return nil, err
		}
		if _, err = checkPrintShopIdentity(ctx, s); err != nil {
			return nil, err
		}
		upload, err := s.uploadPublishFile(ctx, p.publicationPost().File)
		if err != nil {
			return nil, err
		}
		upload.ForceFile = p.File.MIME == "application/pdf"
		media, err := s.uploadGroupedDocument(ctx, s.target, upload)
		if err != nil {
			return nil, err
		}
		req.MultiMedia[i] = tg.InputSingleMedia{
			Media: media, Message: p.Text, Entities: entities, RandomID: printShopRandomID(p.Key),
		}
	}
	if _, err := checkPrintShopIdentity(ctx, s); err != nil {
		return nil, err
	}
	for _, p := range posts {
		if err := verifyPublishFile(ctx, p.publicationPost().File); err != nil {
			return nil, err
		}
	}
	req.Peer = s.target.InputPeer
	if err := s.beforeRPC(ctx, "publish_print_shop_group"); err != nil {
		return nil, err
	}
	if err := recordIntent(); err != nil {
		return nil, err
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultRPCTimeout)
	defer cancel()
	updates, err := s.raw.MessagesSendMultiMedia(callCtx, req)
	if err != nil {
		return nil, err
	}
	return groupedDeliveryMessageIDs(updates, req.MultiMedia), nil
}

func (s *printShopSession) printShopDownloadFile(ctx context.Context, document *tg.Document, output io.Writer) error {
	return s.downloadPublishedDocument(ctx, document, output, "print_shop")
}
