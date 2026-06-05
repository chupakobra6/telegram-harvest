package mtproto

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/config"
	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/chupakobra6/telegram-harvest/internal/transcribe"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

const (
	defaultRPCTimeout        = 30 * time.Second
	defaultDialogTimeout     = 45 * time.Second
	defaultHistoryTimeout    = 45 * time.Second
	defaultDownloadTimeout   = 2 * time.Minute
	defaultTranscribeTimeout = 10 * time.Minute
	maxFloodWaitRetries      = 3
	defaultDialogBatchSize   = 100
	defaultDailyDialogLimit  = 500
	defaultMaxPhotoBytes     = harvest.DefaultMaxPhotoBytes
	defaultMaxDocumentBytes  = harvest.DefaultMaxDocumentBytes
	defaultMaxAudioBytes     = harvest.DefaultMaxAudioBytes
	defaultMaxVideoBytes     = harvest.DefaultMaxVideoBytes
)

var linkPattern = regexp.MustCompile(`(?i)\b(?:https?://|t\.me/|telegram\.me/)[^\s<>()"'` + "`" + `]+`)

type Client struct {
	cfg config.Config
}

type AuthStatus struct {
	Authorized bool
}

type Session struct {
	client     *telegram.Client
	raw        *tg.Client
	rpcSpacing time.Duration

	mu          sync.Mutex
	nextRPCAt   time.Time
	floodWaits  int
	dialogCache map[string]resolvedTarget
}

type DownloadMediaOptions struct {
	MediaDir  string
	Index     int
	Overwrite bool
}

type DownloadMediaResult struct {
	Record     harvest.MessageRecord `json:"record"`
	Attachment harvest.Attachment    `json:"attachment"`
}

type resolvedTarget struct {
	Raw       string
	Chat      harvest.Chat
	InputPeer tg.InputPeerClass
}

func New(cfg config.Config) *Client {
	return &Client{cfg: cfg}
}

func (c *Client) Login(ctx context.Context, in *os.File, out *os.File) error {
	if err := c.cfg.ValidateLogin(); err != nil {
		return err
	}
	if err := ensureSessionDir(c.cfg.SessionPath); err != nil {
		return err
	}
	client := c.newTelegramClient()
	reader := bufio.NewReader(in)
	_, _ = fmt.Fprintf(out, "starting read-only MTProto login for %s\n", maskPhone(c.cfg.Phone))
	_, _ = fmt.Fprintln(out, "connecting to Telegram...")
	return client.Run(ctx, func(runCtx context.Context) error {
		status, err := client.Auth().Status(runCtx)
		if err != nil {
			return fmt.Errorf("auth status: %w", err)
		}
		if status.Authorized {
			_, _ = fmt.Fprintln(out, "session already authorized")
			return nil
		}

		_, _ = fmt.Fprintf(out, "connected, authenticating as %s\n", maskPhone(c.cfg.Phone))
		_, _ = fmt.Fprintln(out, "requesting login code...")
		sentCodeClass, err := client.Auth().SendCode(runCtx, c.cfg.Phone, auth.SendCodeOptions{})
		if err != nil {
			return fmt.Errorf("send code: %w", err)
		}

		sentCode, ok := sentCodeClass.(*tg.AuthSentCode)
		if !ok {
			if _, ok := sentCodeClass.(*tg.AuthSentCodeSuccess); ok {
				_, _ = fmt.Fprintln(out, "login successful")
				return nil
			}
			return fmt.Errorf("unexpected sent code type %T", sentCodeClass)
		}

		_, _ = fmt.Fprintf(out, "code requested via %s\n", sentCodeTypeSummary(sentCode))
		code, err := promptLine(out, reader, "code: ")
		if err != nil {
			return fmt.Errorf("read code: %w", err)
		}

		if _, err := client.Auth().SignIn(runCtx, c.cfg.Phone, code, sentCode.PhoneCodeHash); err != nil {
			if !errors.Is(err, auth.ErrPasswordAuthNeeded) {
				return fmt.Errorf("sign in: %w", err)
			}
			password := strings.TrimSpace(c.cfg.Password)
			if password == "" {
				_, _ = fmt.Fprintln(out, "two-factor authentication is enabled")
				password, err = promptLine(out, reader, "password: ")
				if err != nil {
					return fmt.Errorf("read password: %w", err)
				}
			}
			if _, err := client.Auth().Password(runCtx, password); err != nil {
				return fmt.Errorf("sign in with password: %w", err)
			}
		}

		_, _ = fmt.Fprintln(out, "login successful")
		return nil
	})
}

func (c *Client) AuthStatus(ctx context.Context) (AuthStatus, error) {
	if err := c.cfg.ValidateRuntime(); err != nil {
		return AuthStatus{}, err
	}
	if err := ensureSessionDir(c.cfg.SessionPath); err != nil {
		return AuthStatus{}, err
	}
	client := c.newTelegramClient()
	var status AuthStatus
	err := client.Run(ctx, func(runCtx context.Context) error {
		authStatus, err := client.Auth().Status(runCtx)
		if err != nil {
			return fmt.Errorf("auth status: %w", err)
		}
		status.Authorized = authStatus.Authorized
		return nil
	})
	if err != nil {
		return AuthStatus{}, err
	}
	return status, nil
}

func (c *Client) RunAuthorized(ctx context.Context, fn func(context.Context, *Session) error) error {
	if err := c.cfg.ValidateRuntime(); err != nil {
		return err
	}
	if err := ensureSessionDir(c.cfg.SessionPath); err != nil {
		return err
	}
	client := c.newTelegramClient()
	return client.Run(ctx, func(runCtx context.Context) error {
		status, err := client.Auth().Status(runCtx)
		if err != nil {
			return fmt.Errorf("auth status: %w", err)
		}
		if !status.Authorized {
			return unauthorizedRuntimeError(c.cfg.SessionPath)
		}
		session := &Session{
			client:      client,
			raw:         tg.NewClient(client),
			rpcSpacing:  c.cfg.RPCSpacing,
			dialogCache: map[string]resolvedTarget{},
		}
		return fn(runCtx, session)
	})
}

func unauthorizedRuntimeError(sessionPath string) error {
	if sessionFileExists(sessionPath) {
		return fmt.Errorf("telegram session is not authorized: session file exists at %s, but Telegram requires re-login; run the matching login command again", sessionPath)
	}
	return fmt.Errorf("telegram session is not authorized: no valid Telegram session is available; run the matching login command")
}

func (s *Session) ListDialogs(ctx context.Context, limit int, query string) ([]harvest.Chat, error) {
	if limit <= 0 {
		limit = defaultDialogBatchSize
	}
	query = strings.ToLower(strings.TrimSpace(query))
	dialogs, err := s.loadDialogs(ctx, limit)
	if err != nil {
		return nil, err
	}
	if query == "" {
		return dialogs, nil
	}
	filtered := make([]harvest.Chat, 0, len(dialogs))
	for _, chat := range dialogs {
		haystack := strings.ToLower(strings.Join([]string{chat.Title, chat.Username, chat.Display, strconv.FormatInt(chat.ID, 10)}, " "))
		if strings.Contains(haystack, query) {
			filtered = append(filtered, chat)
		}
	}
	return filtered, nil
}

func (s *Session) ListTopics(ctx context.Context, chat string, limit int, query string) ([]harvest.Topic, error) {
	target, err := s.resolveTarget(ctx, chat)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	query = strings.TrimSpace(query)
	topics := make([]harvest.Topic, 0, limit)
	offsetTopic := 0
	offsetID := 0
	offsetDate := 0
	for len(topics) < limit {
		batchLimit := min(100, limit-len(topics))
		var result *tg.MessagesForumTopics
		err := s.performRPC(ctx, "get_forum_topics", func(callCtx context.Context) error {
			var callErr error
			req := &tg.MessagesGetForumTopicsRequest{
				Peer:        target.InputPeer,
				OffsetDate:  offsetDate,
				OffsetID:    offsetID,
				OffsetTopic: offsetTopic,
				Limit:       batchLimit,
			}
			if query != "" {
				req.SetQ(query)
			}
			result, callErr = s.raw.MessagesGetForumTopics(callCtx, req)
			return callErr
		})
		if err != nil {
			return nil, fmt.Errorf("load forum topics: %w", err)
		}
		if len(result.Topics) == 0 {
			break
		}
		topMessages := map[int]tg.MessageClass{}
		for _, msg := range result.Messages {
			topMessages[messageID(msg)] = msg
		}
		for _, topicClass := range result.Topics {
			topic, ok := topicFromClass(topicClass, topMessages)
			if !ok {
				continue
			}
			topics = append(topics, topic)
		}
		last, ok := lastTopic(result.Topics, topMessages)
		if !ok || len(result.Topics) < batchLimit {
			break
		}
		offsetTopic = last.ID
		offsetID = last.TopMessageID
		if !last.LastMessageAt.IsZero() {
			offsetDate = int(last.LastMessageAt.Unix())
		}
	}
	return topics, nil
}

func (s *Session) SelfProfile(ctx context.Context) (harvest.SelfProfile, error) {
	var result *tg.UsersUserFull
	err := s.performRPC(ctx, "get_self", func(callCtx context.Context) error {
		var callErr error
		result, callErr = s.raw.UsersGetFullUser(callCtx, &tg.InputUserSelf{})
		return callErr
	})
	if err != nil {
		return harvest.SelfProfile{}, err
	}
	full := result.GetFullUser()
	profile := harvest.SelfProfile{ID: full.ID}
	for _, userClass := range result.GetUsers() {
		user, ok := userClass.(*tg.User)
		if !ok || user.ID != full.ID {
			continue
		}
		username, _ := user.GetUsername()
		phone, _ := user.GetPhone()
		display := strings.TrimSpace(strings.Join([]string{user.FirstName, user.LastName}, " "))
		if display == "" {
			display = usernameOrID(username, user.ID)
		}
		profile.Username = username
		profile.FirstName = user.FirstName
		profile.LastName = user.LastName
		profile.Phone = phone
		profile.Display = display
		break
	}
	return profile, nil
}

func (s *Session) DumpHistory(ctx context.Context, chat string, opts harvest.HistoryOptions, emit func(harvest.MessageRecord) error) (harvest.Chat, harvest.HistoryStats, error) {
	target, err := s.resolveTarget(ctx, chat)
	if err != nil {
		return harvest.Chat{}, harvest.HistoryStats{}, err
	}
	opts = normalizeHistoryOptions(opts)
	topicByID := map[int]harvest.Topic{}
	if opts.TopicID > 0 {
		topicByID[opts.TopicID] = harvest.Topic{ID: opts.TopicID, Title: opts.TopicTitle, TopMessageID: opts.TopicID}
	} else if target.Chat.Forum {
		if topics, err := s.ListTopics(ctx, chat, 500, ""); err == nil {
			for _, topic := range topics {
				storeTopic(topicByID, topic.ID, topic)
				if topic.TopMessageID > 0 {
					storeTopic(topicByID, topic.TopMessageID, topic)
				}
			}
		}
	}
	if opts.All {
		return s.dumpHistoryStreaming(ctx, target, opts, emit, topicByID)
	}

	records := make([]harvest.MessageRecord, 0, initialHistoryCapacity(opts))
	offsetID := 0
	batches := 0
	for shouldContinueHistory(opts, len(records), batches) {
		batches++
		batchLimit := nextBatchLimit(opts, len(records))
		var result tg.MessagesMessagesClass
		if opts.TopicID > 0 {
			err = s.performRPC(ctx, "get_replies", func(callCtx context.Context) error {
				var callErr error
				result, callErr = s.raw.MessagesGetReplies(callCtx, &tg.MessagesGetRepliesRequest{
					Peer:     target.InputPeer,
					MsgID:    opts.TopicID,
					OffsetID: offsetID,
					Limit:    batchLimit,
					MinID:    opts.MinID,
					Hash:     0,
				})
				return callErr
			})
		} else {
			err = s.performRPC(ctx, "get_history", func(callCtx context.Context) error {
				var callErr error
				result, callErr = s.raw.MessagesGetHistory(callCtx, &tg.MessagesGetHistoryRequest{
					Peer:     target.InputPeer,
					OffsetID: offsetID,
					Limit:    batchLimit,
					MinID:    opts.MinID,
					Hash:     0,
				})
				return callErr
			})
		}
		if err != nil {
			return harvest.Chat{}, harvest.HistoryStats{}, err
		}
		entities := historyEntities(result)
		messages := historyMessages(result)
		if len(messages) == 0 {
			break
		}
		mergeTopicMap(topicByID, historyTopics(result), messages)

		minSeenID := 0
		for _, msgClass := range messages {
			record, ok := normalizeRecord(msgClass, target.Chat, entities)
			if !ok {
				continue
			}
			annotateRecordTopic(&record, opts, topicByID)
			if opts.MinID > 0 && record.MessageID <= opts.MinID {
				continue
			}
			s.downloadRecordMedia(ctx, msgClass, &record, opts)
			records = append(records, record)
			if minSeenID == 0 || record.MessageID < minSeenID {
				minSeenID = record.MessageID
			}
		}
		if minSeenID == 0 || len(messages) < batchLimit {
			break
		}
		offsetID = minSeenID
	}

	sort.Slice(records, func(i, j int) bool { return records[i].MessageID < records[j].MessageID })
	stats := harvest.HistoryStats{Records: len(records), Batches: batches, FloodWaits: s.FloodWaits()}
	for _, record := range records {
		if stats.FirstID == 0 || record.MessageID < stats.FirstID {
			stats.FirstID = record.MessageID
		}
		if record.MessageID > stats.LastID {
			stats.LastID = record.MessageID
		}
		if emit != nil {
			if err := emit(record); err != nil {
				return harvest.Chat{}, harvest.HistoryStats{}, err
			}
		}
	}
	stats.Complete = true
	return target.Chat, stats, nil
}

func (s *Session) DownloadMessageMedia(ctx context.Context, chat string, messageID int, opts DownloadMediaOptions) (DownloadMediaResult, error) {
	if messageID <= 0 {
		return DownloadMediaResult{}, fmt.Errorf("message id must be > 0")
	}
	target, err := s.resolveTarget(ctx, chat)
	if err != nil {
		return DownloadMediaResult{}, err
	}
	msgClass, entities, err := s.fetchMessageByID(ctx, target, messageID)
	if err != nil {
		return DownloadMediaResult{}, err
	}
	msg, ok := msgClass.(*tg.Message)
	if !ok {
		return DownloadMediaResult{}, fmt.Errorf("message %d is not downloadable", messageID)
	}
	record, ok := normalizeRecord(msg, target.Chat, entities)
	if !ok {
		record = harvest.MessageRecord{
			Source:    "telegram",
			SourceURL: messageURL(target.Chat, msg.ID),
			Chat:      target.Chat,
			MessageID: msg.ID,
			Date:      time.Unix(int64(msg.Date), 0).UTC(),
			Outgoing:  msg.Out,
			Kind:      messageKind(msg.Media),
		}
	}
	attachment, location, fileName, ok := downloadableMedia(msg.Media, record.MessageID)
	if !ok {
		return DownloadMediaResult{}, fmt.Errorf("message %d has no downloadable media", messageID)
	}
	record.Attachments = []harvest.Attachment{attachment}
	index := opts.Index
	if index <= 0 {
		index = 1
	}
	if index != 1 {
		return DownloadMediaResult{}, fmt.Errorf("attachment index %d is unavailable; message has 1 downloadable attachment", index)
	}
	mediaDir := opts.MediaDir
	if strings.TrimSpace(mediaDir) == "" {
		mediaDir = "media-manual"
	}
	s.saveAttachmentFile(ctx, &record, 0, location, fileName, mediaDir, opts.Overwrite)
	result := DownloadMediaResult{Record: record, Attachment: record.Attachments[0]}
	if result.Attachment.DownloadError != "" {
		return result, errors.New(result.Attachment.DownloadError)
	}
	return result, nil
}

func (s *Session) fetchMessageByID(ctx context.Context, target resolvedTarget, messageID int) (tg.MessageClass, peer.Entities, error) {
	var result tg.MessagesMessagesClass
	err := s.performRPC(ctx, "get_history", func(callCtx context.Context) error {
		var callErr error
		result, callErr = s.raw.MessagesGetHistory(callCtx, &tg.MessagesGetHistoryRequest{
			Peer:  target.InputPeer,
			Limit: 1,
			MinID: messageID - 1,
			MaxID: messageID + 1,
			Hash:  0,
		})
		return callErr
	})
	if err != nil {
		return nil, peer.Entities{}, err
	}
	entities := historyEntities(result)
	for _, msg := range historyMessages(result) {
		if typed, ok := msg.(*tg.Message); ok && typed.ID == messageID {
			return msg, entities, nil
		}
	}
	return nil, entities, fmt.Errorf("message %d not found", messageID)
}

func (s *Session) DumpOutgoingDay(ctx context.Context, opts harvest.OutgoingDayOptions, emit func(harvest.MessageRecord) error) (harvest.OutgoingDayStats, error) {
	opts = normalizeOutgoingDayOptions(opts)
	if opts.Start.IsZero() {
		return harvest.OutgoingDayStats{}, fmt.Errorf("start time is required")
	}
	if opts.End.IsZero() || !opts.End.After(opts.Start) {
		return harvest.OutgoingDayStats{}, fmt.Errorf("end time must be after start time")
	}
	dialogs, err := s.loadDialogs(ctx, opts.DialogLimit)
	if err != nil {
		return harvest.OutgoingDayStats{}, err
	}

	stats := harvest.OutgoingDayStats{DialogsScanned: len(dialogs)}
	records := make([]harvest.MessageRecord, 0)
	for _, chat := range dialogs {
		if !chat.LastMessageAt.IsZero() && chat.LastMessageAt.Before(opts.Start) {
			stats.DialogsSkipped++
			if opts.Progress != nil {
				if err := opts.Progress(harvest.OutgoingDayProgress{
					Chat:       chat,
					Skipped:    true,
					Total:      len(records),
					FloodWaits: s.FloodWaits(),
				}); err != nil {
					return harvest.OutgoingDayStats{}, err
				}
			}
			continue
		}

		target, err := s.resolveTarget(ctx, strconv.FormatInt(chat.ID, 10))
		if err != nil {
			stats.DialogErrors = append(stats.DialogErrors, dailyDialogError(chat, err))
			if opts.Progress != nil {
				if err := opts.Progress(harvest.OutgoingDayProgress{
					Chat:       chat,
					Error:      oneLine(err.Error()),
					Total:      len(records),
					FloodWaits: s.FloodWaits(),
				}); err != nil {
					return harvest.OutgoingDayStats{}, err
				}
			}
			continue
		}

		dialogRecords, dialogStats, err := s.searchOutgoingDayInDialog(ctx, target, opts)
		stats.Batches += dialogStats.Batches
		if err != nil {
			stats.DialogErrors = append(stats.DialogErrors, dailyDialogError(chat, err))
			if opts.Progress != nil {
				if err := opts.Progress(harvest.OutgoingDayProgress{
					Chat:       chat,
					Error:      oneLine(err.Error()),
					Total:      len(records),
					Batches:    dialogStats.Batches,
					FloodWaits: s.FloodWaits(),
				}); err != nil {
					return harvest.OutgoingDayStats{}, err
				}
			}
			continue
		}
		if len(dialogRecords) > 0 {
			stats.DialogsWithRecords++
			records = append(records, dialogRecords...)
		}
		if !dialogStats.Complete {
			stats.DialogErrors = append(stats.DialogErrors, dailyDialogIncomplete(chat, opts.History.MaxBatches))
		}
		if opts.Progress != nil {
			if err := opts.Progress(harvest.OutgoingDayProgress{
				Chat:       chat,
				Records:    len(dialogRecords),
				Total:      len(records),
				Batches:    dialogStats.Batches,
				FloodWaits: s.FloodWaits(),
			}); err != nil {
				return harvest.OutgoingDayStats{}, err
			}
		}
	}

	sortDailyRecords(records)
	if opts.History.Limit > 0 && len(records) > opts.History.Limit {
		records = records[len(records)-opts.History.Limit:]
	}
	for _, record := range records {
		stats.Records++
		if stats.FirstAt.IsZero() || record.Date.Before(stats.FirstAt) {
			stats.FirstAt = record.Date
		}
		if record.Date.After(stats.LastAt) {
			stats.LastAt = record.Date
		}
		for _, attachment := range record.Attachments {
			stats.Attachments++
			if strings.TrimSpace(attachment.Transcript) != "" {
				stats.Transcripts++
			}
		}
		if emit != nil {
			if err := emit(record); err != nil {
				return harvest.OutgoingDayStats{}, err
			}
		}
	}
	stats.FloodWaits = s.FloodWaits()
	stats.Complete = len(stats.DialogErrors) == 0
	return stats, nil
}

func (s *Session) dumpHistoryStreaming(ctx context.Context, target resolvedTarget, opts harvest.HistoryOptions, emit func(harvest.MessageRecord) error, topicByID map[int]harvest.Topic) (harvest.Chat, harvest.HistoryStats, error) {
	offsetID := opts.StartOffsetID
	stats := harvest.HistoryStats{}
	for shouldContinueHistory(opts, stats.Records, stats.Batches) {
		stats.Batches++
		batchLimit := opts.BatchSize
		var result tg.MessagesMessagesClass
		var err error
		if opts.TopicID > 0 {
			err = s.performRPC(ctx, "get_replies", func(callCtx context.Context) error {
				var callErr error
				result, callErr = s.raw.MessagesGetReplies(callCtx, &tg.MessagesGetRepliesRequest{
					Peer:     target.InputPeer,
					MsgID:    opts.TopicID,
					OffsetID: offsetID,
					Limit:    batchLimit,
					MinID:    opts.MinID,
					Hash:     0,
				})
				return callErr
			})
		} else {
			err = s.performRPC(ctx, "get_history", func(callCtx context.Context) error {
				var callErr error
				result, callErr = s.raw.MessagesGetHistory(callCtx, &tg.MessagesGetHistoryRequest{
					Peer:     target.InputPeer,
					OffsetID: offsetID,
					Limit:    batchLimit,
					MinID:    opts.MinID,
					Hash:     0,
				})
				return callErr
			})
		}
		if err != nil {
			stats.FloodWaits = s.FloodWaits()
			return harvest.Chat{}, stats, err
		}
		entities := historyEntities(result)
		messages := historyMessages(result)
		if len(messages) == 0 {
			stats.Complete = true
			stats.FloodWaits = s.FloodWaits()
			if opts.Progress != nil {
				if err := opts.Progress(historyProgress(stats, 0, offsetID, true)); err != nil {
					return harvest.Chat{}, stats, err
				}
			}
			break
		}
		mergeTopicMap(topicByID, historyTopics(result), messages)

		batchRecords := make([]harvest.MessageRecord, 0, len(messages))
		minMessageID := 0
		for _, msgClass := range messages {
			if id := messageID(msgClass); id > 0 && (minMessageID == 0 || id < minMessageID) {
				minMessageID = id
			}
			record, ok := normalizeRecord(msgClass, target.Chat, entities)
			if !ok {
				continue
			}
			annotateRecordTopic(&record, opts, topicByID)
			if opts.MinID > 0 && record.MessageID <= opts.MinID {
				continue
			}
			s.downloadRecordMedia(ctx, msgClass, &record, opts)
			batchRecords = append(batchRecords, record)
		}
		sort.Slice(batchRecords, func(i, j int) bool { return batchRecords[i].MessageID < batchRecords[j].MessageID })
		for _, record := range batchRecords {
			if emit != nil {
				if err := emit(record); err != nil {
					stats.FloodWaits = s.FloodWaits()
					return harvest.Chat{}, stats, err
				}
			}
			stats.Records++
			if stats.FirstID == 0 || record.MessageID < stats.FirstID {
				stats.FirstID = record.MessageID
			}
			if record.MessageID > stats.LastID {
				stats.LastID = record.MessageID
			}
		}
		done := minMessageID == 0 || len(messages) < batchLimit
		if done {
			stats.Complete = true
		}
		stats.FloodWaits = s.FloodWaits()
		if opts.Progress != nil {
			if err := opts.Progress(historyProgress(stats, len(batchRecords), minMessageID, done)); err != nil {
				return harvest.Chat{}, stats, err
			}
		}
		if done {
			break
		}
		offsetID = minMessageID
	}
	stats.FloodWaits = s.FloodWaits()
	return target.Chat, stats, nil
}

func historyProgress(stats harvest.HistoryStats, batchRecords int, nextOffsetID int, done bool) harvest.HistoryProgress {
	return harvest.HistoryProgress{
		BatchRecords: batchRecords,
		Records:      stats.Records,
		FirstID:      stats.FirstID,
		LastID:       stats.LastID,
		Batches:      stats.Batches,
		NextOffsetID: nextOffsetID,
		Done:         done,
		FloodWaits:   stats.FloodWaits,
	}
}

func normalizeHistoryOptions(opts harvest.HistoryOptions) harvest.HistoryOptions {
	if opts.Limit <= 0 && !opts.All {
		opts.Limit = config.DefaultHistoryLimit
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = config.DefaultBatchSize
	}
	if opts.BatchSize > 100 {
		opts.BatchSize = 100
	}
	if opts.MaxBatches <= 0 && !opts.All {
		opts.MaxBatches = config.DefaultMaxBatches
	}
	return opts
}

func normalizeOutgoingDayOptions(opts harvest.OutgoingDayOptions) harvest.OutgoingDayOptions {
	if opts.DialogLimit <= 0 {
		opts.DialogLimit = defaultDailyDialogLimit
	}
	if opts.History.BatchSize <= 0 {
		opts.History.BatchSize = config.DefaultBatchSize
	}
	if opts.History.BatchSize > 100 {
		opts.History.BatchSize = 100
	}
	if opts.History.MaxBatches < 0 {
		opts.History.MaxBatches = config.DefaultMaxBatches
	}
	return opts
}

func (s *Session) searchOutgoingDayInDialog(ctx context.Context, target resolvedTarget, opts harvest.OutgoingDayOptions) ([]harvest.MessageRecord, harvest.HistoryStats, error) {
	records, stats, err := s.searchOutgoingDayWithSearch(ctx, target, opts)
	if err != nil && isSearchQueryEmpty(err) {
		return s.scanOutgoingDayWithHistory(ctx, target, opts)
	}
	return records, stats, err
}

type outgoingDayBatchLoader func(context.Context, int, int) (tg.MessagesMessagesClass, error)

func (s *Session) searchOutgoingDayWithSearch(ctx context.Context, target resolvedTarget, opts harvest.OutgoingDayOptions) ([]harvest.MessageRecord, harvest.HistoryStats, error) {
	return s.collectOutgoingDay(ctx, target, opts, false, func(callCtx context.Context, offsetID int, batchLimit int) (tg.MessagesMessagesClass, error) {
		var result tg.MessagesMessagesClass
		err := s.performRPC(callCtx, "search_messages", func(rpcCtx context.Context) error {
			var callErr error
			req := &tg.MessagesSearchRequest{
				Peer:     target.InputPeer,
				Q:        "",
				Filter:   &tg.InputMessagesFilterEmpty{},
				MinDate:  int(opts.Start.Unix()) - 1,
				MaxDate:  int(opts.End.Unix()),
				OffsetID: offsetID,
				Limit:    batchLimit,
				Hash:     0,
			}
			req.SetFromID(&tg.InputPeerSelf{})
			result, callErr = s.raw.MessagesSearch(rpcCtx, req)
			return callErr
		})
		return result, err
	})
}

func (s *Session) scanOutgoingDayWithHistory(ctx context.Context, target resolvedTarget, opts harvest.OutgoingDayOptions) ([]harvest.MessageRecord, harvest.HistoryStats, error) {
	return s.collectOutgoingDay(ctx, target, opts, true, func(callCtx context.Context, offsetID int, batchLimit int) (tg.MessagesMessagesClass, error) {
		var result tg.MessagesMessagesClass
		err := s.performRPC(callCtx, "get_history", func(rpcCtx context.Context) error {
			var callErr error
			result, callErr = s.raw.MessagesGetHistory(rpcCtx, &tg.MessagesGetHistoryRequest{
				Peer:     target.InputPeer,
				OffsetID: offsetID,
				Limit:    batchLimit,
				Hash:     0,
			})
			return callErr
		})
		return result, err
	})
}

func (s *Session) collectOutgoingDay(
	ctx context.Context,
	target resolvedTarget,
	opts harvest.OutgoingDayOptions,
	stopAtStart bool,
	load outgoingDayBatchLoader,
) ([]harvest.MessageRecord, harvest.HistoryStats, error) {
	topicByID := map[int]harvest.Topic{}
	records := make([]harvest.MessageRecord, 0)
	stats := harvest.HistoryStats{}
	offsetID := 0
	for shouldContinueOutgoingDay(opts, stats.Batches) {
		stats.Batches++
		batchLimit := opts.History.BatchSize
		result, err := load(ctx, offsetID, batchLimit)
		if err != nil {
			stats.FloodWaits = s.FloodWaits()
			return nil, stats, err
		}
		entities := historyEntities(result)
		messages := historyMessages(result)
		if len(messages) == 0 {
			stats.Complete = true
			break
		}
		mergeTopicMap(topicByID, historyTopics(result), messages)

		minMessageID := 0
		reachedStart := false
		for _, msgClass := range messages {
			if id := messageID(msgClass); id > 0 && (minMessageID == 0 || id < minMessageID) {
				minMessageID = id
			}
			if stopAtStart && messageAtOrBefore(msgClass, opts.Start) {
				reachedStart = true
			}
			record, ok := s.normalizeOutgoingDayRecord(ctx, msgClass, target, entities, topicByID, opts)
			if !ok {
				continue
			}
			records = append(records, record)
		}
		if reachedStart || minMessageID == 0 || len(messages) < batchLimit {
			stats.Complete = true
			break
		}
		offsetID = minMessageID
	}
	sortDailyRecords(records)
	finalizeHistoryStats(&stats, records, s.FloodWaits())
	return records, stats, nil
}

func messageAtOrBefore(msg tg.MessageClass, boundary time.Time) bool {
	date := messageDate(msg)
	return date > 0 && !time.Unix(int64(date), 0).UTC().After(boundary)
}

func finalizeHistoryStats(stats *harvest.HistoryStats, records []harvest.MessageRecord, floodWaits int) {
	stats.Records = len(records)
	stats.FloodWaits = floodWaits
	for _, record := range records {
		if stats.FirstID == 0 || record.MessageID < stats.FirstID {
			stats.FirstID = record.MessageID
		}
		if record.MessageID > stats.LastID {
			stats.LastID = record.MessageID
		}
	}
}

func (s *Session) normalizeOutgoingDayRecord(
	ctx context.Context,
	msgClass tg.MessageClass,
	target resolvedTarget,
	entities peer.Entities,
	topicByID map[int]harvest.Topic,
	opts harvest.OutgoingDayOptions,
) (harvest.MessageRecord, bool) {
	record, ok := normalizeRecord(msgClass, target.Chat, entities)
	if !ok {
		return harvest.MessageRecord{}, false
	}
	annotateRecordTopic(&record, opts.History, topicByID)
	if !opts.IncludeService && record.Kind == "service" {
		return harvest.MessageRecord{}, false
	}
	if record.Date.Before(opts.Start) || !record.Date.Before(opts.End) {
		return harvest.MessageRecord{}, false
	}
	if !record.Outgoing && !record.Sender.Self {
		return harvest.MessageRecord{}, false
	}
	if record.Sender.Display == "" && record.Outgoing {
		record.Sender = harvest.Sender{Type: "self", Display: "self", Self: true}
	}
	ensureDailyAttachments(msgClass, &record)
	s.downloadRecordMedia(ctx, msgClass, &record, opts.History)
	return record, true
}

func shouldContinueOutgoingDay(opts harvest.OutgoingDayOptions, batches int) bool {
	if opts.History.MaxBatches > 0 && batches >= opts.History.MaxBatches {
		return false
	}
	return true
}

func sortDailyRecords(records []harvest.MessageRecord) {
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Date.Equal(records[j].Date) {
			if records[i].Chat.ID == records[j].Chat.ID {
				return records[i].MessageID < records[j].MessageID
			}
			return records[i].Chat.ID < records[j].Chat.ID
		}
		return records[i].Date.Before(records[j].Date)
	})
}

func dailyDialogError(chat harvest.Chat, err error) string {
	return fmt.Sprintf("%s (%d): %s", displayChannel(chat.Title, chat.Username, chat.ID), chat.ID, oneLine(err.Error()))
}

func dailyDialogIncomplete(chat harvest.Chat, maxBatches int) string {
	if maxBatches > 0 {
		return fmt.Sprintf("%s (%d): stopped after max_batches=%d before confirming the day boundary", displayChannel(chat.Title, chat.Username, chat.ID), chat.ID, maxBatches)
	}
	return fmt.Sprintf("%s (%d): stopped before confirming the day boundary", displayChannel(chat.Title, chat.Username, chat.ID), chat.ID)
}

func isSearchQueryEmpty(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToUpper(err.Error()), "SEARCH_QUERY_EMPTY")
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func initialHistoryCapacity(opts harvest.HistoryOptions) int {
	if opts.All {
		return 0
	}
	return min(opts.Limit, opts.BatchSize*max(opts.MaxBatches, 1))
}

func shouldContinueHistory(opts harvest.HistoryOptions, records int, batches int) bool {
	if !opts.All && records >= opts.Limit {
		return false
	}
	if opts.MaxBatches > 0 && batches >= opts.MaxBatches {
		return false
	}
	return true
}

func nextBatchLimit(opts harvest.HistoryOptions, records int) int {
	if opts.All {
		return opts.BatchSize
	}
	return min(opts.BatchSize, opts.Limit-records)
}

func (s *Session) loadDialogs(ctx context.Context, limit int) ([]harvest.Chat, error) {
	all := make([]harvest.Chat, 0, limit)
	offsetPeer := tg.InputPeerClass(&tg.InputPeerEmpty{})
	offsetID := 0
	offsetDate := 0

	for len(all) < limit {
		batchLimit := min(defaultDialogBatchSize, limit-len(all))
		var result tg.MessagesDialogsClass
		err := s.performRPC(ctx, "get_dialogs", func(callCtx context.Context) error {
			var callErr error
			result, callErr = s.raw.MessagesGetDialogs(callCtx, &tg.MessagesGetDialogsRequest{
				ExcludePinned: false,
				OffsetDate:    offsetDate,
				OffsetID:      offsetID,
				OffsetPeer:    offsetPeer,
				Limit:         batchLimit,
				Hash:          0,
			})
			return callErr
		})
		if err != nil {
			return nil, fmt.Errorf("load dialogs: %w", err)
		}
		modified, ok := result.AsModified()
		if !ok {
			break
		}
		entities := dialogEntities(result)
		for _, dialog := range modified.GetDialogs() {
			chat, inputPeer, ok := chatFromPeer(dialog.GetPeer(), entities)
			if !ok {
				continue
			}
			chat.Pinned = dialog.GetPinned()
			chat.UnreadCount = dialogUnreadCount(dialog)
			chat.TopMessageID = dialog.GetTopMessage()
			if top := topMessageByID(modified.GetMessages(), chat.TopMessageID); top != nil {
				chat.LastMessageAt = time.Unix(int64(messageDate(top)), 0).UTC()
			}
			all = append(all, chat)
			s.cacheTarget(chat, inputPeer)
		}
		messages := modified.GetMessages()
		if len(messages) == 0 || len(messages) < batchLimit {
			break
		}
		last := messages[len(messages)-1]
		lastID, lastDate, lastPeer, ok := messageOffset(last)
		if !ok {
			break
		}
		offsetID = lastID
		offsetDate = lastDate
		offsetPeer = inputPeerFromMessagePeer(lastPeer, entities)
		if offsetPeer == nil {
			break
		}
	}
	return all, nil
}

func (s *Session) resolveTarget(ctx context.Context, raw string) (resolvedTarget, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return resolvedTarget{}, fmt.Errorf("target chat is empty")
	}
	if cached, ok := s.dialogCache[raw]; ok {
		return cached, nil
	}
	if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if _, err := s.loadDialogs(ctx, 1000); err != nil {
			return resolvedTarget{}, err
		}
		for _, candidate := range numericPeerCandidates(id) {
			if cached, ok := s.dialogCache[strconv.FormatInt(candidate, 10)]; ok {
				return cached, nil
			}
		}
		return resolvedTarget{}, fmt.Errorf("numeric peer id %d was not found in dialogs; run `chats --query ...` and use an exact listed id or @username", id)
	}
	username := strings.TrimPrefix(raw, "@")
	var resolved *tg.ContactsResolvedPeer
	err := s.performRPC(ctx, "resolve_username", func(callCtx context.Context) error {
		var callErr error
		resolved, callErr = s.raw.ContactsResolveUsername(callCtx, &tg.ContactsResolveUsernameRequest{
			Username: username,
		})
		return callErr
	})
	if err != nil {
		return resolvedTarget{}, fmt.Errorf("resolve username %q: %w", raw, err)
	}
	entities := peer.EntitiesFromResult(resolved)
	inputPeer, err := entities.ExtractPeer(resolved.Peer)
	if err != nil {
		return resolvedTarget{}, err
	}
	chat, _, ok := chatFromPeer(resolved.Peer, entities)
	if !ok {
		chat = harvest.Chat{ID: peerClassID(resolved.Peer), Type: peerType(resolved.Peer), Display: "@" + username, Username: username}
	}
	target := resolvedTarget{Raw: raw, Chat: chat, InputPeer: inputPeer}
	s.dialogCache[raw] = target
	s.dialogCache["@"+username] = target
	s.dialogCache[strconv.FormatInt(chat.ID, 10)] = target
	return target, nil
}

func (s *Session) cacheTarget(chat harvest.Chat, inputPeer tg.InputPeerClass) {
	if inputPeer == nil || chat.ID == 0 {
		return
	}
	target := resolvedTarget{Raw: strconv.FormatInt(chat.ID, 10), Chat: chat, InputPeer: inputPeer}
	s.dialogCache[strconv.FormatInt(chat.ID, 10)] = target
	if chat.Username != "" {
		s.dialogCache["@"+chat.Username] = target
		s.dialogCache[chat.Username] = target
	}
	if chat.Title != "" {
		s.dialogCache[chat.Title] = target
	}
}

func (s *Session) performRPC(ctx context.Context, operation string, fn func(context.Context) error) error {
	attempt := func() error {
		if err := s.beforeRPC(ctx); err != nil {
			return err
		}
		callCtx, cancel := context.WithTimeout(ctx, rpcTimeoutForOperation(operation))
		defer cancel()
		return fn(callCtx)
	}
	var lastErr error
	for attemptNo := 0; attemptNo < maxFloodWaitRetries; attemptNo++ {
		if attemptNo > 0 {
			delay, ok := floodWaitDelay(lastErr)
			if !ok {
				break
			}
			s.noteFloodWait()
			if err := sleepContext(ctx, delay+s.rpcSpacing); err != nil {
				return err
			}
		}
		if err := attempt(); err != nil {
			lastErr = err
			if _, ok := floodWaitDelay(err); ok && attemptNo < maxFloodWaitRetries-1 {
				continue
			}
			return err
		}
		return nil
	}
	return lastErr
}

func rpcTimeoutForOperation(operation string) time.Duration {
	switch operation {
	case "get_dialogs":
		return defaultDialogTimeout
	case "get_history", "get_replies", "get_forum_topics":
		return defaultHistoryTimeout
	default:
		return defaultRPCTimeout
	}
}

func (s *Session) beforeRPC(ctx context.Context) error {
	delay := s.reserveRPCSlot(time.Now())
	if delay <= 0 {
		return nil
	}
	return sleepContext(ctx, delay)
}

func (s *Session) reserveRPCSlot(now time.Time) time.Duration {
	if s.rpcSpacing <= 0 {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nextRPCAt.IsZero() || s.nextRPCAt.Before(now) {
		s.nextRPCAt = now
	}
	delay := s.nextRPCAt.Sub(now)
	s.nextRPCAt = s.nextRPCAt.Add(s.rpcSpacing)
	return delay
}

func (s *Session) noteFloodWait() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.floodWaits++
}

func (s *Session) FloodWaits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.floodWaits
}

func floodWaitDelay(err error) (time.Duration, bool) {
	delay, ok := tgerr.AsFloodWait(err)
	if !ok {
		return 0, false
	}
	if delay <= 0 {
		return time.Second, true
	}
	return delay, true
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func historyEntities(result tg.MessagesMessagesClass) peer.Entities {
	modified, ok := result.AsModified()
	if !ok {
		return peer.Entities{}
	}
	chats := tg.ChatClassArray(modified.GetChats())
	return peer.NewEntities(
		tg.UserClassArray(modified.GetUsers()).UserToMap(),
		chats.ChatToMap(),
		chats.ChannelToMap(),
	)
}

func historyMessages(result tg.MessagesMessagesClass) []tg.MessageClass {
	modified, ok := result.AsModified()
	if !ok {
		return nil
	}
	return modified.GetMessages()
}

func historyTopics(result tg.MessagesMessagesClass) []tg.ForumTopicClass {
	modified, ok := result.AsModified()
	if !ok {
		return nil
	}
	return modified.GetTopics()
}

func dialogEntities(result tg.MessagesDialogsClass) peer.Entities {
	modified, ok := result.AsModified()
	if !ok {
		return peer.Entities{}
	}
	chats := tg.ChatClassArray(modified.GetChats())
	return peer.NewEntities(
		tg.UserClassArray(modified.GetUsers()).UserToMap(),
		chats.ChatToMap(),
		chats.ChannelToMap(),
	)
}

func dialogUnreadCount(dialog tg.DialogClass) int {
	typed, ok := dialog.(*tg.Dialog)
	if !ok {
		return 0
	}
	return typed.UnreadCount
}

func chatFromPeer(peerClass tg.PeerClass, entities peer.Entities) (harvest.Chat, tg.InputPeerClass, bool) {
	switch typed := peerClass.(type) {
	case *tg.PeerUser:
		user, ok := entities.User(typed.UserID)
		if !ok {
			return harvest.Chat{}, nil, false
		}
		username, _ := user.GetUsername()
		display := strings.TrimSpace(strings.Join([]string{user.FirstName, user.LastName}, " "))
		if display == "" {
			display = usernameOrID(username, user.ID)
		}
		return harvest.Chat{
			ID:       user.ID,
			Type:     "user",
			Title:    display,
			Username: username,
			Display:  display,
		}, user.AsInputPeer(), true
	case *tg.PeerChat:
		chat, ok := entities.Chat(typed.ChatID)
		if !ok {
			return harvest.Chat{}, nil, false
		}
		return harvest.Chat{
			ID:                   chat.ID,
			Type:                 "basic_group",
			Title:                chat.Title,
			Display:              chat.Title,
			ParticipantsEstimate: chat.ParticipantsCount,
		}, chat.AsInputPeer(), true
	case *tg.PeerChannel:
		channel, ok := entities.Channel(typed.ChannelID)
		if !ok {
			return harvest.Chat{}, nil, false
		}
		username, _ := channel.GetUsername()
		chatType := "channel"
		if channel.Megagroup {
			chatType = "supergroup"
		}
		return harvest.Chat{
			ID:                   channel.ID,
			Type:                 chatType,
			Title:                channel.Title,
			Username:             username,
			Display:              displayChannel(channel.Title, username, channel.ID),
			Forum:                channel.Forum,
			ParticipantsEstimate: channel.ParticipantsCount,
		}, channel.AsInputPeer(), true
	default:
		return harvest.Chat{}, nil, false
	}
}

func topicFromClass(topicClass tg.ForumTopicClass, topMessages map[int]tg.MessageClass) (harvest.Topic, bool) {
	switch typed := topicClass.(type) {
	case *tg.ForumTopic:
		topic := harvest.Topic{
			ID:            typed.ID,
			Title:         typed.Title,
			TopMessageID:  typed.TopMessage,
			LastMessageAt: time.Unix(int64(typed.Date), 0).UTC(),
			Pinned:        typed.Pinned,
			Closed:        typed.Closed,
			Hidden:        typed.Hidden,
			UnreadCount:   typed.UnreadCount,
		}
		if typed.TopMessage > 0 {
			if msg := topMessages[typed.TopMessage]; msg != nil {
				if date := messageDate(msg); date > 0 {
					topic.LastMessageAt = time.Unix(int64(date), 0).UTC()
				}
			}
		}
		return topic, true
	case *tg.ForumTopicDeleted:
		return harvest.Topic{ID: typed.ID, Title: "[deleted topic]"}, typed.ID > 0
	default:
		return harvest.Topic{}, false
	}
}

func lastTopic(topicClasses []tg.ForumTopicClass, topMessages map[int]tg.MessageClass) (harvest.Topic, bool) {
	for i := len(topicClasses) - 1; i >= 0; i-- {
		topic, ok := topicFromClass(topicClasses[i], topMessages)
		if ok {
			return topic, true
		}
	}
	return harvest.Topic{}, false
}

func mergeTopicMap(topicByID map[int]harvest.Topic, topicClasses []tg.ForumTopicClass, messages []tg.MessageClass) {
	if len(topicClasses) == 0 {
		return
	}
	topMessages := make(map[int]tg.MessageClass, len(messages))
	for _, msg := range messages {
		topMessages[messageID(msg)] = msg
	}
	for _, topicClass := range topicClasses {
		topic, ok := topicFromClass(topicClass, topMessages)
		if !ok || topic.ID == 0 {
			continue
		}
		storeTopic(topicByID, topic.ID, topic)
		if topic.TopMessageID > 0 {
			storeTopic(topicByID, topic.TopMessageID, topic)
		}
	}
}

func storeTopic(topicByID map[int]harvest.Topic, key int, topic harvest.Topic) {
	if key == 0 {
		return
	}
	if existing, ok := topicByID[key]; ok {
		topicByID[key] = mergeTopic(existing, topic)
		return
	}
	topicByID[key] = topic
}

func mergeTopic(existing harvest.Topic, incoming harvest.Topic) harvest.Topic {
	merged := existing
	if merged.ID == 0 {
		merged.ID = incoming.ID
	}
	if merged.Title == "" {
		merged.Title = incoming.Title
	}
	if incoming.TopMessageID > 0 {
		merged.TopMessageID = incoming.TopMessageID
	}
	if merged.LastMessageAt.IsZero() || incoming.TopMessageID > 0 {
		if !incoming.LastMessageAt.IsZero() {
			merged.LastMessageAt = incoming.LastMessageAt
		}
	}
	merged.Pinned = merged.Pinned || incoming.Pinned
	merged.Closed = merged.Closed || incoming.Closed
	merged.Hidden = merged.Hidden || incoming.Hidden
	if incoming.UnreadCount > merged.UnreadCount {
		merged.UnreadCount = incoming.UnreadCount
	}
	return merged
}

func annotateRecordTopic(record *harvest.MessageRecord, opts harvest.HistoryOptions, topicByID map[int]harvest.Topic) {
	if opts.TopicID > 0 {
		topic, ok := topicByID[opts.TopicID]
		if !ok {
			topic = harvest.Topic{ID: opts.TopicID, Title: opts.TopicTitle}
		}
		if record.ThreadTopMessageID == 0 {
			record.ThreadTopMessageID = opts.TopicID
		}
		record.Topic = &topic
		return
	}
	if record.ThreadTopMessageID > 0 {
		if topic, ok := topicByID[record.ThreadTopMessageID]; ok {
			record.Topic = &topic
			return
		}
	}
	if record.ReplyToMessageID > 0 {
		if topic, ok := topicByID[record.ReplyToMessageID]; ok {
			if record.ThreadTopMessageID == 0 {
				record.ThreadTopMessageID = topic.ID
			}
			record.Topic = &topic
			return
		}
	}
	if topic, ok := topicByID[record.MessageID]; ok {
		if record.ThreadTopMessageID == 0 {
			record.ThreadTopMessageID = topic.ID
		}
		record.Topic = &topic
		return
	}
	if record.Chat.Forum && record.Kind != "service" {
		if topic, ok := topicByID[1]; ok {
			record.ThreadTopMessageID = topic.ID
			record.Topic = &topic
		}
	}
}

func normalizeRecord(msgClass tg.MessageClass, chat harvest.Chat, entities peer.Entities) (harvest.MessageRecord, bool) {
	switch msg := msgClass.(type) {
	case *tg.Message:
		record := harvest.MessageRecord{
			Source:      "telegram",
			SourceURL:   messageURL(chat, msg.ID),
			Chat:        chat,
			MessageID:   msg.ID,
			Date:        time.Unix(int64(msg.Date), 0).UTC(),
			Sender:      senderFromPeer(msg.FromID, msg.Out, entities),
			Outgoing:    msg.Out,
			Kind:        messageKind(msg.Media),
			Text:        strings.TrimSpace(msg.Message),
			Pinned:      msg.Pinned,
			Views:       msg.Views,
			Links:       mergeLinks(extractLinks(msg.Message, msg.Entities), extractMediaLinks(msg.Media)),
			Attachments: extractAttachments(msg.Media),
		}
		if replyID, topID := replyInfo(msg.ReplyTo); replyID > 0 || topID > 0 {
			record.ReplyToMessageID = replyID
			record.ThreadTopMessageID = topID
		}
		return record, true
	case *tg.MessageService:
		record := harvest.MessageRecord{
			Source:    "telegram",
			SourceURL: messageURL(chat, msg.ID),
			Chat:      chat,
			MessageID: msg.ID,
			Date:      time.Unix(int64(msg.Date), 0).UTC(),
			Sender:    senderFromPeer(msg.FromID, msg.Out, entities),
			Outgoing:  msg.Out,
			Kind:      "service",
			Text:      serviceActionText(msg.Action),
			RawAction: rawActionName(msg.Action),
		}
		if replyID, topID := replyInfo(msg.ReplyTo); replyID > 0 || topID > 0 {
			record.ReplyToMessageID = replyID
			record.ThreadTopMessageID = topID
		}
		return record, true
	default:
		return harvest.MessageRecord{}, false
	}
}

func senderFromPeer(from tg.PeerClass, outgoing bool, entities peer.Entities) harvest.Sender {
	if from == nil {
		if outgoing {
			return harvest.Sender{Type: "self", Display: "self", Self: true}
		}
		return harvest.Sender{}
	}
	switch typed := from.(type) {
	case *tg.PeerUser:
		user, ok := entities.User(typed.UserID)
		if !ok {
			return harvest.Sender{ID: typed.UserID, Type: "user", Display: strconv.FormatInt(typed.UserID, 10), Self: outgoing}
		}
		username, _ := user.GetUsername()
		display := strings.TrimSpace(strings.Join([]string{user.FirstName, user.LastName}, " "))
		if display == "" {
			display = usernameOrID(username, user.ID)
		}
		return harvest.Sender{ID: user.ID, Type: "user", Username: username, Display: display, Self: user.Self || outgoing, Bot: user.Bot}
	case *tg.PeerChat:
		chat, ok := entities.Chat(typed.ChatID)
		display := strconv.FormatInt(typed.ChatID, 10)
		if ok {
			display = chat.Title
		}
		return harvest.Sender{ID: typed.ChatID, Type: "basic_group", Display: display}
	case *tg.PeerChannel:
		channel, ok := entities.Channel(typed.ChannelID)
		display := strconv.FormatInt(typed.ChannelID, 10)
		username := ""
		if ok {
			display = channel.Title
			username, _ = channel.GetUsername()
		}
		return harvest.Sender{ID: typed.ChannelID, Type: "channel", Username: username, Display: display}
	default:
		return harvest.Sender{ID: peerClassID(from), Type: peerType(from), Display: fmt.Sprintf("%T", from)}
	}
}

func messageKind(media tg.MessageMediaClass) string {
	switch typed := media.(type) {
	case nil:
		return "text"
	case *tg.MessageMediaPhoto:
		return "photo"
	case *tg.MessageMediaDocument:
		return documentKind(typed)
	case *tg.MessageMediaWebPage:
		return "webpage"
	case *tg.MessageMediaGeo:
		return "geo"
	case *tg.MessageMediaGeoLive:
		return "geo_live"
	case *tg.MessageMediaVenue:
		return "venue"
	case *tg.MessageMediaContact:
		return "contact"
	case *tg.MessageMediaPoll:
		return "poll"
	case *tg.MessageMediaGame:
		return "game"
	case *tg.MessageMediaInvoice:
		return "invoice"
	case *tg.MessageMediaDice:
		return "dice"
	case *tg.MessageMediaStory:
		return "story"
	case *tg.MessageMediaGiveaway:
		return "giveaway"
	case *tg.MessageMediaGiveawayResults:
		return "giveaway_results"
	case *tg.MessageMediaPaidMedia:
		return "paid_media"
	case *tg.MessageMediaToDo:
		return "todo"
	case *tg.MessageMediaVideoStream:
		return "video_stream"
	case *tg.MessageMediaUnsupported:
		return "unsupported_media"
	default:
		return "media"
	}
}

func extractAttachments(media tg.MessageMediaClass) []harvest.Attachment {
	switch typed := media.(type) {
	case *tg.MessageMediaPhoto:
		return []harvest.Attachment{{Kind: "photo"}}
	case *tg.MessageMediaDocument:
		kind := documentKind(typed)
		if kind != "document" && kind != "image" {
			return nil
		}
		attachment := harvest.Attachment{Kind: kind}
		if document, ok := typed.GetDocument(); ok {
			if doc, ok := document.(*tg.Document); ok {
				attachment.MediaID = documentMediaID(doc)
				attachment.MIMEType = doc.MimeType
				attachment.Size = doc.Size
				attachment.FileName = documentFileName(doc)
			}
		}
		return []harvest.Attachment{attachment}
	case *tg.MessageMediaWebPage:
		link, title := webPageMetadata(typed.Webpage)
		if link == "" && title == "" {
			return nil
		}
		return []harvest.Attachment{{Kind: "webpage", Title: title, URL: link}}
	case *tg.MessageMediaPoll:
		if attached, ok := typed.GetAttachedMedia(); ok {
			return extractAttachments(attached)
		}
		return nil
	default:
		return nil
	}
}

func ensureDailyAttachments(msgClass tg.MessageClass, record *harvest.MessageRecord) {
	if record == nil || len(record.Attachments) > 0 {
		return
	}
	msg, ok := msgClass.(*tg.Message)
	if !ok {
		return
	}
	switch media := msg.Media.(type) {
	case *tg.MessageMediaDocument:
		attachment, ok := dailyDocumentAttachment(media, record.MessageID)
		if ok {
			record.Attachments = []harvest.Attachment{attachment}
		}
	case *tg.MessageMediaPoll:
		if attached, ok := media.GetAttachedMedia(); ok {
			copyMsg := *msg
			copyMsg.Media = attached
			ensureDailyAttachments(&copyMsg, record)
		}
	}
}

func dailyDocumentAttachment(media *tg.MessageMediaDocument, messageID int) (harvest.Attachment, bool) {
	kind := documentKind(media)
	switch kind {
	case "voice", "round_video", "audio", "video":
	default:
		return harvest.Attachment{}, false
	}
	attachment := harvest.Attachment{Kind: kind}
	if document, ok := media.GetDocument(); ok {
		if doc, ok := document.(*tg.Document); ok {
			attachment.MediaID = documentMediaID(doc)
			attachment.MIMEType = doc.MimeType
			attachment.Size = doc.Size
			attachment.FileName = documentFileName(doc)
			if strings.TrimSpace(attachment.FileName) == "" {
				attachment.FileName = fallbackFileName(kind, messageID, doc.MimeType)
			}
		}
	}
	if strings.TrimSpace(attachment.FileName) == "" {
		attachment.FileName = fallbackFileName(kind, messageID, "")
	}
	return attachment, true
}

func downloadableMedia(media tg.MessageMediaClass, messageID int) (harvest.Attachment, tg.InputFileLocationClass, string, bool) {
	switch typed := media.(type) {
	case *tg.MessageMediaPhoto:
		location, fileName, size, ok := photoDownload(typed)
		if !ok {
			return harvest.Attachment{}, nil, "", false
		}
		return harvest.Attachment{
			Kind:     "photo",
			MIMEType: "image/jpeg",
			FileName: fileName,
			Size:     size,
		}, location, fileName, true
	case *tg.MessageMediaDocument:
		document, ok := typed.GetDocument()
		if !ok {
			return harvest.Attachment{}, nil, "", false
		}
		doc, ok := document.(*tg.Document)
		if !ok {
			return harvest.Attachment{}, nil, "", false
		}
		kind := documentKind(typed)
		fileName := documentFileName(doc)
		if strings.TrimSpace(fileName) == "" {
			fileName = fallbackFileName(kind, messageID, doc.MimeType)
		}
		return harvest.Attachment{
			Kind:     kind,
			MediaID:  documentMediaID(doc),
			MIMEType: doc.MimeType,
			Size:     doc.Size,
			FileName: fileName,
		}, doc.AsInputDocumentFileLocation(), fileName, true
	case *tg.MessageMediaPoll:
		if attached, ok := typed.GetAttachedMedia(); ok {
			return downloadableMedia(attached, messageID)
		}
		return harvest.Attachment{}, nil, "", false
	default:
		return harvest.Attachment{}, nil, "", false
	}
}

func (s *Session) downloadRecordMedia(ctx context.Context, msgClass tg.MessageClass, record *harvest.MessageRecord, opts harvest.HistoryOptions) {
	if !opts.DownloadMedia || record == nil || len(record.Attachments) == 0 {
		return
	}
	msg, ok := msgClass.(*tg.Message)
	if !ok {
		return
	}
	switch typed := msg.Media.(type) {
	case *tg.MessageMediaPhoto:
		if len(record.Attachments) == 0 {
			return
		}
		location, fileName, size, ok := photoDownload(typed)
		if !ok {
			record.Attachments[0].DownloadError = "photo location is unavailable"
			return
		}
		record.Attachments[0].MIMEType = "image/jpeg"
		record.Attachments[0].FileName = fileName
		record.Attachments[0].Size = size
		s.downloadAttachment(ctx, record, 0, location, fileName, opts)
	case *tg.MessageMediaDocument:
		if len(record.Attachments) == 0 {
			return
		}
		document, ok := typed.GetDocument()
		if !ok {
			record.Attachments[0].DownloadError = "document location is unavailable"
			return
		}
		doc, ok := document.(*tg.Document)
		if !ok {
			record.Attachments[0].DownloadError = "document location is unavailable"
			return
		}
		fileName := documentFileName(doc)
		if strings.TrimSpace(fileName) == "" {
			fileName = fallbackFileName(record.Kind, record.MessageID, doc.MimeType)
		}
		record.Attachments[0].MediaID = documentMediaID(doc)
		record.Attachments[0].Kind = documentKind(typed)
		record.Attachments[0].MIMEType = doc.MimeType
		record.Attachments[0].Size = doc.Size
		record.Attachments[0].FileName = fileName
		s.downloadAttachment(ctx, record, 0, doc.AsInputDocumentFileLocation(), fileName, opts)
	case *tg.MessageMediaPoll:
		if attached, ok := typed.GetAttachedMedia(); ok {
			copyMsg := *msg
			copyMsg.Media = attached
			s.downloadRecordMedia(ctx, &copyMsg, record, opts)
		}
	}
}

func (s *Session) downloadAttachment(
	ctx context.Context,
	record *harvest.MessageRecord,
	index int,
	location tg.InputFileLocationClass,
	fileName string,
	opts harvest.HistoryOptions,
) {
	if s.client == nil || record == nil || index < 0 || index >= len(record.Attachments) || location == nil {
		return
	}
	if transcriptMediaKind(record.Attachments[index].Kind) {
		s.transcribeAttachmentMedia(ctx, record, index, location, fileName, opts)
		return
	}
	if mediaSizeLimitExceeded(record, index, opts) {
		return
	}
	if strings.TrimSpace(opts.MediaDir) == "" {
		record.Attachments[index].DownloadError = "media dir is empty"
		return
	}
	s.saveAttachmentFile(ctx, record, index, location, fileName, opts.MediaDir, false)
}

func (s *Session) saveAttachmentFile(
	ctx context.Context,
	record *harvest.MessageRecord,
	index int,
	location tg.InputFileLocationClass,
	fileName string,
	mediaDir string,
	overwrite bool,
) {
	if s.client == nil || record == nil || index < 0 || index >= len(record.Attachments) || location == nil {
		return
	}
	if strings.TrimSpace(mediaDir) == "" {
		record.Attachments[index].DownloadError = "media dir is empty"
		return
	}
	target := mediaTargetPath(mediaDir, *record, index, fileName)
	record.Attachments[index].LocalPath = target
	if existing, err := os.Stat(target); err == nil && existing.Size() > 0 {
		if !overwrite {
			return
		}
		if err := os.Remove(target); err != nil {
			record.Attachments[index].DownloadError = fmt.Sprintf("remove existing media: %v", err)
			return
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		record.Attachments[index].DownloadError = fmt.Sprintf("prepare media dir: %v", err)
		return
	}
	if err := s.beforeRPC(ctx); err != nil {
		record.Attachments[index].DownloadError = err.Error()
		return
	}
	downloadCtx, cancel := context.WithTimeout(ctx, defaultDownloadTimeout)
	defer cancel()
	if _, err := s.client.Download(location).WithThreads(1).ToPath(downloadCtx, target); err != nil {
		_ = os.Remove(target)
		record.Attachments[index].DownloadError = err.Error()
		return
	}
}

func (s *Session) transcribeAttachmentMedia(
	ctx context.Context,
	record *harvest.MessageRecord,
	index int,
	location tg.InputFileLocationClass,
	fileName string,
	opts harvest.HistoryOptions,
) {
	if record == nil || index < 0 || index >= len(record.Attachments) {
		return
	}
	attachment := &record.Attachments[index]
	if !transcriptMediaKind(attachment.Kind) {
		return
	}
	transcriptPath := transcriptCachePath(opts.TranscriptDir, *record, index, *attachment)
	attachment.TranscriptPath = transcriptPath
	if transcript, err := readTranscriptFile(transcriptPath); err == nil {
		attachment.Transcript = transcript
		attachment.TranscriptCached = true
		touchTranscriptFile(transcriptPath)
		return
	}
	if !opts.TranscribeMedia {
		attachment.TranscriptError = "skipped: transcription disabled for audio/video media"
		return
	}
	if mediaSizeLimitExceeded(record, index, opts) {
		return
	}
	transcribeOpts := transcribeOptions(opts)
	if opts.Transcriber == nil && !transcribeOpts.Configured() {
		attachment.TranscriptError = "transcription is not configured"
		return
	}
	tempPath, err := createTemporaryMediaPath(opts.MediaDir, fileName)
	if err != nil {
		attachment.DownloadError = fmt.Sprintf("prepare temporary media: %v", err)
		return
	}
	defer os.Remove(tempPath)
	if err := s.beforeRPC(ctx); err != nil {
		attachment.DownloadError = err.Error()
		return
	}
	downloadCtx, cancelDownload := context.WithTimeout(ctx, defaultDownloadTimeout)
	defer cancelDownload()
	if _, err := s.client.Download(location).WithThreads(1).ToPath(downloadCtx, tempPath); err != nil {
		attachment.DownloadError = err.Error()
		return
	}

	transcribeCtx, cancel := context.WithTimeout(ctx, defaultTranscribeTimeout)
	defer cancel()
	transcript, err := runTranscriber(transcribeCtx, opts.Transcriber, transcribeOpts, tempPath, transcriptPath)
	if err != nil {
		attachment.TranscriptError = oneLine(err.Error())
		return
	}
	if strings.TrimSpace(transcript) == "" {
		if fromFile, readErr := readTranscriptFile(transcriptPath); readErr == nil {
			transcript = fromFile
		}
	}
	attachment.Transcript = transcript
}

func runTranscriber(ctx context.Context, runner harvest.Transcriber, opts transcribe.Options, inputPath string, outputPath string) (string, error) {
	if runner != nil {
		return runner.Run(ctx, inputPath, outputPath)
	}
	return transcribe.Run(ctx, opts, inputPath, outputPath)
}

func transcriptMediaKind(kind string) bool {
	switch kind {
	case "voice", "audio", "round_video", "video":
		return true
	default:
		return false
	}
}

func readTranscriptFile(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(content)), nil
}

func touchTranscriptFile(path string) {
	now := time.Now()
	_ = os.Chtimes(path, now, now)
}

func transcriptCachePath(transcriptDir string, record harvest.MessageRecord, index int, attachment harvest.Attachment) string {
	if strings.TrimSpace(transcriptDir) == "" {
		transcriptDir = "transcripts"
	}
	key := transcriptCacheKey(record, index, attachment)
	sum := sha256.Sum256([]byte(key))
	hash := hex.EncodeToString(sum[:])
	kind := safePathSegment(attachment.Kind)
	if kind == "" {
		kind = "media"
	}
	return filepath.Join(transcriptDir, "cache", hash[:2], fmt.Sprintf("%s-%s.txt", kind, hash[:24]))
}

func transcriptCacheKey(record harvest.MessageRecord, index int, attachment harvest.Attachment) string {
	if mediaID := strings.TrimSpace(attachment.MediaID); mediaID != "" {
		return attachment.Kind + ":" + mediaID
	}
	return fmt.Sprintf("message:%s:%d:%d:%d:%s:%s:%d",
		record.Source,
		record.Chat.ID,
		record.MessageID,
		index,
		attachment.Kind,
		attachment.FileName,
		attachment.Size,
	)
}

func createTemporaryMediaPath(mediaDir string, fileName string) (string, error) {
	if strings.TrimSpace(mediaDir) == "" {
		mediaDir = os.TempDir()
	}
	tempDir := filepath.Join(mediaDir, ".tmp")
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		return "", err
	}
	extension := filepath.Ext(safeFileName(fileName))
	if extension == "" {
		extension = ".bin"
	}
	file, err := os.CreateTemp(tempDir, "telegram-media-*"+extension)
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func transcribeOptions(opts harvest.HistoryOptions) transcribe.Options {
	return transcribe.Options{
		CommandTemplate: opts.TranscribeCommand,
		VoskCommand:     opts.VoskCommand,
		VoskModelPath:   opts.VoskModelPath,
		VoskGrammarPath: opts.VoskGrammarPath,
		FFmpegCommand:   opts.FFmpegCommand,
	}
}

func photoDownload(media *tg.MessageMediaPhoto) (tg.InputFileLocationClass, string, int64, bool) {
	if media == nil {
		return nil, "", 0, false
	}
	photoClass, ok := media.GetPhoto()
	if !ok {
		return nil, "", 0, false
	}
	photo, ok := photoClass.AsNotEmpty()
	if !ok {
		return nil, "", 0, false
	}
	thumbSize := bestPhotoThumbSize(photo)
	if thumbSize == "" {
		return nil, "", 0, false
	}
	fileName := fmt.Sprintf("photo-%d-%s.jpg", photo.ID, time.Unix(int64(photo.Date), 0).UTC().Format("20060102T150405"))
	size := photoThumbSizeBytes(photo, thumbSize)
	return &tg.InputPhotoFileLocation{
		ID:            photo.ID,
		AccessHash:    photo.AccessHash,
		FileReference: photo.FileReference,
		ThumbSize:     thumbSize,
	}, fileName, size, true
}

func bestPhotoThumbSize(photo *tg.Photo) string {
	if photo == nil {
		return ""
	}
	bestType := ""
	bestArea := -1
	for _, size := range photo.Sizes {
		width, height, ok := photoSizeDimensions(size)
		if !ok {
			continue
		}
		area := width * height
		if area > bestArea {
			bestArea = area
			bestType = size.GetType()
		}
	}
	return bestType
}

func photoSizeDimensions(size tg.PhotoSizeClass) (int, int, bool) {
	switch typed := size.(type) {
	case *tg.PhotoSize:
		return typed.W, typed.H, true
	case *tg.PhotoCachedSize:
		return typed.W, typed.H, true
	case *tg.PhotoSizeProgressive:
		return typed.W, typed.H, true
	default:
		return 0, 0, false
	}
}

func photoThumbSizeBytes(photo *tg.Photo, thumbSize string) int64 {
	if photo == nil || thumbSize == "" {
		return 0
	}
	for _, size := range photo.Sizes {
		if size.GetType() != thumbSize {
			continue
		}
		return photoSizeBytes(size)
	}
	return 0
}

func photoSizeBytes(size tg.PhotoSizeClass) int64 {
	switch typed := size.(type) {
	case *tg.PhotoSize:
		return int64(typed.Size)
	case *tg.PhotoCachedSize:
		return int64(len(typed.Bytes))
	case *tg.PhotoSizeProgressive:
		if len(typed.Sizes) == 0 {
			return 0
		}
		return int64(typed.Sizes[len(typed.Sizes)-1])
	default:
		return 0
	}
}

func mediaSizeLimitExceeded(record *harvest.MessageRecord, index int, opts harvest.HistoryOptions) bool {
	if record == nil || index < 0 || index >= len(record.Attachments) {
		return false
	}
	attachment := &record.Attachments[index]
	if attachment.Size <= 0 {
		return false
	}
	limit, label := mediaSizeLimit(*attachment, opts)
	if limit <= 0 || attachment.Size <= limit {
		return false
	}
	attachment.DownloadError = fmt.Sprintf("skipped: %s size %s exceeds %s cap %s", attachment.Kind, formatBytes(attachment.Size), label, formatBytes(limit))
	attachment.DownloadHint = manualDownloadHint(*record, index, opts.ManualDownloadCommand)
	return true
}

func mediaSizeLimit(attachment harvest.Attachment, opts harvest.HistoryOptions) (int64, string) {
	switch attachment.Kind {
	case "photo", "image":
		return mediaLimitOrDefault(opts.MaxPhotoBytes, defaultMaxPhotoBytes), "image"
	case "video", "round_video":
		return mediaLimitOrDefault(opts.MaxVideoBytes, defaultMaxVideoBytes), "video"
	case "voice", "audio":
		return mediaLimitOrDefault(opts.MaxAudioBytes, defaultMaxAudioBytes), "audio"
	default:
		return mediaLimitOrDefault(opts.MaxDocumentBytes, defaultMaxDocumentBytes), "document"
	}
}

func mediaLimitOrDefault(value int64, fallback int64) int64 {
	if value < 0 {
		return fallback
	}
	return value
}

func manualDownloadHint(record harvest.MessageRecord, index int, command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		command = "telegram-harvest download-media"
	}
	chat := record.Chat.Username
	if chat != "" {
		chat = "@" + chat
	} else {
		chat = strconv.FormatInt(record.Chat.ID, 10)
	}
	if chat == "" || record.MessageID == 0 {
		return "manual download available with download-media using chat, message id, and attachment index"
	}
	return fmt.Sprintf("to fetch manually without caps: %s --chat %s --message-id %d --index %d", command, shellArg(chat), record.MessageID, index+1)
}

func formatBytes(value int64) string {
	if value < 1024 {
		return fmt.Sprintf("%d B", value)
	}
	const unit = 1024
	units := []string{"KiB", "MiB", "GiB"}
	amount := float64(value)
	for _, unitName := range units {
		amount /= unit
		if amount < unit || unitName == units[len(units)-1] {
			return fmt.Sprintf("%.1f %s", amount, unitName)
		}
	}
	return fmt.Sprintf("%d B", value)
}

func shellArg(value string) string {
	if value == "" {
		return "''"
	}
	if strings.ContainsAny(value, " \t\n'\"\\$`") {
		return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
	}
	return value
}

func mediaTargetPath(mediaDir string, record harvest.MessageRecord, index int, fileName string) string {
	day := record.Date.In(time.FixedZone("Europe/Moscow", 3*60*60)).Format("2006-01-02")
	chatDir := safePathSegment(displayChannel(record.Chat.Title, record.Chat.Username, record.Chat.ID))
	if chatDir == "" {
		chatDir = strconv.FormatInt(record.Chat.ID, 10)
	}
	name := safeFileName(fileName)
	if name == "" {
		name = fallbackFileName(record.Kind, record.MessageID, "")
	}
	name = fmt.Sprintf("%d-%02d-%s", record.MessageID, index+1, name)
	return filepath.Join(mediaDir, chatDir, day, name)
}

func fallbackFileName(kind string, messageID int, mimeType string) string {
	extension := ".bin"
	if mimeType != "" {
		if extensions, err := mime.ExtensionsByType(mimeType); err == nil && len(extensions) > 0 {
			extension = extensions[0]
		}
	}
	kind = safePathSegment(kind)
	if kind == "" {
		kind = "attachment"
	}
	return fmt.Sprintf("%s-%d%s", kind, messageID, extension)
}

func safePathSegment(value string) string {
	value = safeFileName(value)
	value = strings.Trim(value, ". ")
	value = truncateRunes(value, 80)
	value = strings.TrimRight(value, ". ")
	return value
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) > limit {
		value = string(runes[:limit])
	}
	return value
}

func safeFileName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		"\n", " ",
		"\r", " ",
		"\t", " ",
	)
	value = replacer.Replace(value)
	value = strings.Join(strings.Fields(value), " ")
	value = strings.Trim(value, ". ")
	if value == "" {
		return ""
	}
	if len([]rune(value)) > 160 {
		ext := filepath.Ext(value)
		stem := strings.TrimSuffix(value, ext)
		if len(ext) > 16 {
			ext = ""
		}
		maxStem := 160 - len(ext)
		if maxStem < 1 {
			maxStem = 1
		}
		value = strings.TrimRight(truncateRunes(stem, maxStem), ". ") + ext
	}
	return value
}

func documentKind(media *tg.MessageMediaDocument) string {
	if media == nil {
		return "document"
	}
	if media.Voice {
		return "voice"
	}
	if media.Round {
		return "round_video"
	}
	if media.Video {
		return "video"
	}
	if document, ok := media.GetDocument(); ok {
		if doc, ok := document.(*tg.Document); ok {
			if strings.HasPrefix(strings.ToLower(doc.MimeType), "image/") {
				return "image"
			}
			for _, attr := range doc.Attributes {
				audio, ok := attr.(*tg.DocumentAttributeAudio)
				if ok {
					if audio.Voice {
						return "voice"
					}
					return "audio"
				}
				if _, ok := attr.(*tg.DocumentAttributeVideo); ok {
					return "video"
				}
			}
		}
	}
	return "document"
}

func documentMediaID(doc *tg.Document) string {
	if doc == nil || doc.ID == 0 {
		return ""
	}
	return fmt.Sprintf("document:%d", doc.ID)
}

func documentFileName(doc *tg.Document) string {
	for _, attr := range doc.Attributes {
		if filename, ok := attr.(*tg.DocumentAttributeFilename); ok {
			return filename.FileName
		}
	}
	return ""
}

func extractLinks(text string, entities []tg.MessageEntityClass) []string {
	var links []string
	for _, match := range linkPattern.FindAllString(text, -1) {
		links = appendNormalizedLink(links, match)
	}
	for _, entity := range entities {
		if textURL, ok := entity.(*tg.MessageEntityTextURL); ok {
			links = appendNormalizedLink(links, textURL.URL)
		}
	}
	return links
}

func extractMediaLinks(media tg.MessageMediaClass) []string {
	switch typed := media.(type) {
	case *tg.MessageMediaWebPage:
		link, _ := webPageMetadata(typed.Webpage)
		if link == "" {
			return nil
		}
		return []string{link}
	case *tg.MessageMediaPoll:
		if attached, ok := typed.GetAttachedMedia(); ok {
			return extractMediaLinks(attached)
		}
	}
	return nil
}

func mergeLinks(groups ...[]string) []string {
	var links []string
	for _, group := range groups {
		for _, link := range group {
			links = appendNormalizedLink(links, link)
		}
	}
	return links
}

func appendNormalizedLink(links []string, link string) []string {
	normalized := normalizeLink(link)
	if normalized == "" {
		return links
	}
	for _, existing := range links {
		if existing == normalized {
			return links
		}
	}
	return append(links, normalized)
}

func normalizeLink(link string) string {
	link = strings.TrimRight(strings.TrimSpace(link), ".,;:!?)]}")
	if link == "" {
		return ""
	}
	lower := strings.ToLower(link)
	if strings.HasPrefix(lower, "t.me/") || strings.HasPrefix(lower, "telegram.me/") {
		link = "https://" + link
	}
	if _, err := url.ParseRequestURI(link); err != nil {
		return ""
	}
	return link
}

func webPageMetadata(webpage tg.WebPageClass) (string, string) {
	switch typed := webpage.(type) {
	case *tg.WebPage:
		return typed.URL, firstNonEmpty(typed.Title, typed.SiteName, typed.DisplayURL)
	case *tg.WebPagePending:
		return typed.URL, ""
	case *tg.WebPageEmpty:
		return typed.URL, ""
	default:
		return "", ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func replyInfo(reply tg.MessageReplyHeaderClass) (int, int) {
	header, ok := reply.(*tg.MessageReplyHeader)
	if !ok || header == nil {
		return 0, 0
	}
	replyID, _ := header.GetReplyToMsgID()
	topID, _ := header.GetReplyToTopID()
	return replyID, topID
}

func serviceActionText(action tg.MessageActionClass) string {
	if action == nil {
		return "[service]"
	}
	switch action.(type) {
	case *tg.MessageActionPinMessage:
		return "[service] message pinned"
	default:
		return "[service] " + action.TypeName()
	}
}

func rawActionName(action tg.MessageActionClass) string {
	if action == nil {
		return ""
	}
	return action.TypeName()
}

func topMessageByID(messages []tg.MessageClass, id int) tg.MessageClass {
	for _, msg := range messages {
		if messageID(msg) == id {
			return msg
		}
	}
	return nil
}

func messageID(msg tg.MessageClass) int {
	switch typed := msg.(type) {
	case *tg.Message:
		return typed.ID
	case *tg.MessageService:
		return typed.ID
	default:
		return 0
	}
}

func messageDate(msg tg.MessageClass) int {
	switch typed := msg.(type) {
	case *tg.Message:
		return typed.Date
	case *tg.MessageService:
		return typed.Date
	default:
		return 0
	}
}

func messageOffset(msg tg.MessageClass) (int, int, tg.PeerClass, bool) {
	switch typed := msg.(type) {
	case *tg.Message:
		return typed.ID, typed.Date, typed.PeerID, typed.PeerID != nil
	case *tg.MessageService:
		return typed.ID, typed.Date, typed.PeerID, typed.PeerID != nil
	default:
		return 0, 0, nil, false
	}
}

func inputPeerFromMessagePeer(peerClass tg.PeerClass, entities peer.Entities) tg.InputPeerClass {
	switch typed := peerClass.(type) {
	case *tg.PeerUser:
		if user, ok := entities.User(typed.UserID); ok {
			return user.AsInputPeer()
		}
	case *tg.PeerChat:
		return &tg.InputPeerChat{ChatID: typed.ChatID}
	case *tg.PeerChannel:
		if channel, ok := entities.Channel(typed.ChannelID); ok {
			return channel.AsInputPeer()
		}
	}
	return nil
}

func peerClassID(peerClass tg.PeerClass) int64 {
	switch peer := peerClass.(type) {
	case *tg.PeerUser:
		return peer.UserID
	case *tg.PeerChat:
		return peer.ChatID
	case *tg.PeerChannel:
		return peer.ChannelID
	default:
		return 0
	}
}

func peerType(peerClass tg.PeerClass) string {
	switch peerClass.(type) {
	case *tg.PeerUser:
		return "user"
	case *tg.PeerChat:
		return "basic_group"
	case *tg.PeerChannel:
		return "channel"
	default:
		return ""
	}
}

func usernameOrID(username string, id int64) string {
	if strings.TrimSpace(username) != "" {
		return "@" + username
	}
	return strconv.FormatInt(id, 10)
}

func numericPeerCandidates(id int64) []int64 {
	candidates := []int64{id}
	if id < 0 {
		candidates = append(candidates, -id)
		const channelPrefix = int64(1000000000000)
		if -id > channelPrefix {
			candidates = append(candidates, -id-channelPrefix)
		}
	}
	return candidates
}

func displayChannel(title string, username string, id int64) string {
	if title != "" {
		return title
	}
	return usernameOrID(username, id)
}

func messageURL(chat harvest.Chat, messageID int) string {
	if chat.Username != "" {
		return fmt.Sprintf("https://t.me/%s/%d", strings.TrimPrefix(chat.Username, "@"), messageID)
	}
	if chat.Type == "supergroup" || chat.Type == "channel" {
		return fmt.Sprintf("https://t.me/c/%d/%d", chat.ID, messageID)
	}
	return ""
}

func ensureSessionDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}

func sessionFileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func (c *Client) newTelegramClient() *telegram.Client {
	return telegram.NewClient(c.cfg.AppID, c.cfg.AppHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: c.cfg.SessionPath},
		Resolver:       dcs.Plain(dcs.PlainOptions{Dial: proxyAwareDialContext}),
	})
}

func promptLine(out *os.File, reader *bufio.Reader, label string) (string, error) {
	_, _ = fmt.Fprint(out, label)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func maskPhone(phone string) string {
	trimmed := strings.TrimSpace(phone)
	if len(trimmed) <= 4 {
		return trimmed
	}
	return trimmed[:2] + strings.Repeat("*", max(0, len(trimmed)-4)) + trimmed[len(trimmed)-2:]
}

func sentCodeTypeSummary(sentCode *tg.AuthSentCode) string {
	switch sentCode.Type.(type) {
	case *tg.AuthSentCodeTypeApp:
		return "Telegram app"
	case *tg.AuthSentCodeTypeSMS:
		return "SMS"
	case *tg.AuthSentCodeTypeCall:
		return "phone call"
	case *tg.AuthSentCodeTypeFlashCall:
		return "flash call"
	case *tg.AuthSentCodeTypeMissedCall:
		return "missed call"
	case *tg.AuthSentCodeTypeEmailCode:
		return "email"
	case *tg.AuthSentCodeTypeSetUpEmailRequired:
		return "email setup required"
	case *tg.AuthSentCodeTypeFragmentSMS:
		return "fragment SMS"
	default:
		return fmt.Sprintf("%T", sentCode.Type)
	}
}
