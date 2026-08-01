package mtproto

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/chupakobra6/telegram-harvest/internal/stages"
	"github.com/chupakobra6/telegram-harvest/internal/transcribe"
	"github.com/gotd/td/tg"
)

type mediaDownloadPlan struct {
	tasks      []*mediaDownloadTask
	savedByKey map[string]*savedMediaDownload
}

type savedMediaTarget struct {
	attachment *harvest.Attachment
	path       string
}

type savedMediaDownload struct {
	session        *Session
	location       tg.InputFileLocationClass
	expectedBytes  int64
	sourcePath     string
	removeSource   bool
	cache          *persistentMediaCache
	cacheIdentity  string
	targets        []savedMediaTarget
	downloadTiming stages.DownloadTransferObserver
	transferErr    error
}

func newMediaDownloadPlan() *mediaDownloadPlan {
	return &mediaDownloadPlan{savedByKey: make(map[string]*savedMediaDownload)}
}

func (p *mediaDownloadPlan) add(task *mediaDownloadTask) {
	if p == nil || task == nil || task.Transfer == nil {
		return
	}
	p.tasks = append(p.tasks, task)
}

func (s *Session) planRecordMediaDownloads(
	msgClass tg.MessageClass,
	record *harvest.MessageRecord,
	opts harvest.HistoryOptions,
	pipeline *mediaPipeline,
	plan *mediaDownloadPlan,
) {
	if !opts.DownloadMedia || record == nil || plan == nil {
		return
	}
	ensureDailyAttachments(msgClass, record)
	if len(record.Attachments) == 0 {
		return
	}
	msg, ok := msgClass.(*tg.Message)
	if !ok {
		return
	}
	switch typed := msg.Media.(type) {
	case *tg.MessageMediaPhoto:
		location, fileName, mediaID, size, ok := photoDownload(typed)
		if !ok {
			record.Attachments[0].DownloadError = "photo location is unavailable"
			return
		}
		record.Attachments[0].MIMEType = "image/jpeg"
		record.Attachments[0].MediaID = mediaID
		record.Attachments[0].FileName = fileName
		record.Attachments[0].Size = size
		s.planAttachmentDownload(record, 0, location, fileName, opts, pipeline, plan)
	case *tg.MessageMediaDocument:
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
		applyDocumentMetadata(&record.Attachments[0], doc)
		s.planAttachmentDownload(record, 0, doc.AsInputDocumentFileLocation(), fileName, opts, pipeline, plan)
	case *tg.MessageMediaPoll:
		if attached, ok := typed.GetAttachedMedia(); ok {
			copyMsg := *msg
			copyMsg.Media = attached
			s.planRecordMediaDownloads(&copyMsg, record, opts, pipeline, plan)
		}
	}
}

func (s *Session) planAttachmentDownload(
	record *harvest.MessageRecord,
	index int,
	location tg.InputFileLocationClass,
	fileName string,
	opts harvest.HistoryOptions,
	pipeline *mediaPipeline,
	plan *mediaDownloadPlan,
) {
	if s.client == nil || record == nil || index < 0 || index >= len(record.Attachments) || location == nil {
		return
	}
	if transcriptMediaKind(record.Attachments[index].Kind) {
		s.planAttachmentTranscription(record, index, location, fileName, opts, pipeline, plan)
		return
	}
	if mediaSizeLimitExceeded(record, index, opts) {
		return
	}
	if strings.TrimSpace(opts.MediaDir) == "" {
		record.Attachments[index].DownloadError = "media dir is empty"
		return
	}
	s.planSavedAttachment(record, index, location, fileName, opts.MediaDir, false, opts.DownloadTiming, plan)
}

func (s *Session) planSavedAttachment(
	record *harvest.MessageRecord,
	index int,
	location tg.InputFileLocationClass,
	fileName string,
	mediaDir string,
	overwrite bool,
	downloadTiming stages.DownloadTransferObserver,
	plan *mediaDownloadPlan,
) {
	if s.client == nil || record == nil || index < 0 || index >= len(record.Attachments) || location == nil || plan == nil {
		return
	}
	attachment := &record.Attachments[index]
	if strings.TrimSpace(mediaDir) == "" {
		attachment.DownloadError = "media dir is empty"
		return
	}
	target := mediaTargetPath(mediaDir, *record, index, fileName)
	attachment.LocalPath = target
	if existing, err := os.Stat(target); err == nil && existing.Size() > 0 {
		if !overwrite {
			return
		}
		if err := os.Remove(target); err != nil {
			attachment.DownloadError = fmt.Sprintf("remove existing media: %v", err)
			return
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		attachment.DownloadError = fmt.Sprintf("prepare media dir: %v", err)
		return
	}

	cacheIdentity, cacheable := mediaCacheIdentity(*attachment)
	var cache *persistentMediaCache
	if cacheable && !overwrite {
		cache = s.persistentMediaCache(mediaDir)
		if hit, err := cache.Restore(cacheIdentity, target, attachment.Size); err == nil && hit {
			attachment.MediaCached = true
			return
		}
	}
	targetInfo := savedMediaTarget{attachment: attachment, path: target}
	groupKey := ""
	if cache != nil {
		groupKey = mediaCacheRoot(mediaDir) + "\x00" + cacheIdentity + "\x00" + strconv.FormatInt(attachment.Size, 10)
		if existing := plan.savedByKey[groupKey]; existing != nil {
			existing.targets = append(existing.targets, targetInfo)
			return
		}
	}

	sourcePath := target
	removeSource := false
	if cache != nil {
		if tempPath, err := cache.NewDownloadPath(fileName); err == nil {
			sourcePath = tempPath
			removeSource = true
		} else {
			cache = nil
		}
	}
	group := &savedMediaDownload{
		session:        s,
		location:       location,
		expectedBytes:  attachment.Size,
		sourcePath:     sourcePath,
		removeSource:   removeSource,
		cache:          cache,
		cacheIdentity:  cacheIdentity,
		targets:        []savedMediaTarget{targetInfo},
		downloadTiming: downloadTiming,
	}
	if groupKey != "" {
		plan.savedByKey[groupKey] = group
	}
	plan.add(group.task())
}

func (d *savedMediaDownload) task() *mediaDownloadTask {
	return &mediaDownloadTask{
		Slots: adaptiveDownloadThreads(d.expectedBytes),
		Transfer: func(ctx context.Context) {
			downloadCtx, cancel := context.WithTimeout(ctx, defaultDownloadTimeout)
			defer cancel()
			d.transferErr = d.session.downloadFile(downloadCtx, d.location, d.sourcePath, d.expectedBytes, d.downloadTiming)
		},
		Fail:  func(err error) { d.transferErr = err },
		After: d.publish,
		Cancel: func(err error) {
			d.transferErr = err
			d.publish()
		},
	}
}

func (d *savedMediaDownload) publish() {
	if d == nil {
		return
	}
	if d.removeSource {
		defer os.Remove(d.sourcePath)
	}
	if d.transferErr != nil {
		_ = os.Remove(d.sourcePath)
		for _, target := range d.targets {
			target.attachment.DownloadError = d.transferErr.Error()
		}
		return
	}
	for index, target := range d.targets {
		if d.cache != nil {
			if index == 0 {
				if err := d.cache.Store(d.cacheIdentity, d.sourcePath, target.path, d.expectedBytes); err == nil {
					continue
				}
			} else if hit, err := d.cache.Restore(d.cacheIdentity, target.path, d.expectedBytes); err == nil && hit {
				target.attachment.MediaCached = true
				continue
			}
		}
		if d.sourcePath == target.path {
			continue
		}
		if err := publishMediaFileAtomic(d.sourcePath, target.path); err != nil {
			target.attachment.DownloadError = fmt.Sprintf("publish downloaded media: %v", err)
		}
	}
}

func (s *Session) planAttachmentTranscription(
	record *harvest.MessageRecord,
	index int,
	location tg.InputFileLocationClass,
	fileName string,
	opts harvest.HistoryOptions,
	pipeline *mediaPipeline,
	plan *mediaDownloadPlan,
) {
	if record == nil || index < 0 || index >= len(record.Attachments) || plan == nil {
		return
	}
	attachment := &record.Attachments[index]
	if !transcriptMediaKind(attachment.Kind) {
		return
	}
	transcribeOpts := transcribeOptions(opts)
	transcriptPath := transcriptCachePath(opts.TranscriptDir, transcribeOpts.CacheIdentity(), *record, index, *attachment)
	attachment.TranscriptPath = transcriptPath
	if transcript, err := readTranscriptFile(transcriptPath); err == nil {
		attachment.Transcript = transcript
		attachment.TranscriptCached = true
		touchTranscriptFile(transcriptPath)
		emitASRLog(opts, asrLogEvent("cache_hit", "", "", *record, index, *attachment))
		return
	}
	if !opts.TranscribeMedia {
		attachment.TranscriptError = "skipped: transcription disabled for audio/video media"
		emitASRLog(opts, asrLogEvent("skip", "policy", attachment.TranscriptError, *record, index, *attachment))
		return
	}
	if ok, reason := genericVideoTranscriptAllowed(*attachment, opts); !ok {
		attachment.TranscriptError = reason
		emitASRLog(opts, asrLogEvent("skip", "policy", reason, *record, index, *attachment))
		return
	}
	if mediaSizeLimitExceeded(record, index, opts) {
		emitASRLog(opts, asrLogEvent("skip", "size", attachment.DownloadError, *record, index, *attachment))
		return
	}
	if opts.Transcriber == nil && opts.TranscriberFactory == nil && !transcribeOpts.Configured() {
		attachment.TranscriptError = "transcription is not configured"
		emitASRLog(opts, asrLogEvent("skip", "config", attachment.TranscriptError, *record, index, *attachment))
		return
	}
	if pipeline == nil {
		attachment.TranscriptError = "transcription pipeline is unavailable"
		emitASRLog(opts, asrLogEvent("error", "transcribe", attachment.TranscriptError, *record, index, *attachment))
		return
	}
	if !pipeline.claim(transcriptPath) {
		return
	}

	var tempPath string
	var transferErr error
	var downloadSeconds float64
	var startEvent *harvest.ASRLogEvent
	plan.add(&mediaDownloadTask{
		Slots: adaptiveDownloadThreads(attachment.Size),
		Transfer: func(ctx context.Context) {
			var err error
			tempPath, err = createTemporaryMediaPath(opts.MediaDir, fileName)
			if err != nil {
				transferErr = fmt.Errorf("prepare temporary media: %w", err)
				return
			}
			event := asrLogEvent("download_start", "download", "", *record, index, *attachment)
			startEvent = &event
			downloadCtx, cancel := context.WithTimeout(ctx, defaultDownloadTimeout)
			defer cancel()
			downloadStarted := time.Now()
			transferErr = s.downloadFile(downloadCtx, location, tempPath, attachment.Size, opts.DownloadTiming)
			downloadSeconds = secondsSince(downloadStarted)
		},
		Fail: func(err error) { transferErr = err },
		After: func() {
			finishTranscriptionDownload(record, index, attachment, pipeline, opts, transcribeOpts, transcriptPath, tempPath, downloadSeconds, startEvent, transferErr)
		},
		Cancel: func(err error) {
			pipeline.releaseClaim(transcriptPath)
			attachment.DownloadError = err.Error()
		},
	})
}

func (s *Session) reserveDownloadWave(ctx context.Context, observer stages.DownloadQueueWaitObserver) error {
	startedAt := time.Now()
	err := s.beforeRPC(ctx, "download_media")
	stages.ObserveDownloadQueueWait(observer, time.Since(startedAt))
	return err
}

func finishTranscriptionDownload(
	record *harvest.MessageRecord,
	index int,
	attachment *harvest.Attachment,
	pipeline *mediaPipeline,
	opts harvest.HistoryOptions,
	transcribeOpts transcribe.Options,
	transcriptPath string,
	tempPath string,
	downloadSeconds float64,
	startEvent *harvest.ASRLogEvent,
	transferErr error,
) {
	if startEvent != nil {
		emitASRLog(opts, *startEvent)
	}
	if transferErr != nil {
		if tempPath != "" {
			_ = os.Remove(tempPath)
		}
		pipeline.releaseClaim(transcriptPath)
		attachment.DownloadError = transferErr.Error()
		event := asrLogEvent("error", "download", attachment.DownloadError, *record, index, *attachment)
		event.DownloadSeconds = downloadSeconds
		emitASRLog(opts, event)
		return
	}
	job := mediaPipelineJob{
		Key:              transcriptPath,
		InputPath:        tempPath,
		TranscriptPath:   transcriptPath,
		Record:           *record,
		Attachment:       *attachment,
		AttachmentIndex:  index,
		DownloadSeconds:  downloadSeconds,
		TranscribeOption: transcribeOpts,
	}
	if err := pipeline.enqueue(job); err != nil {
		_ = os.Remove(tempPath)
		pipeline.releaseClaim(transcriptPath)
		attachment.TranscriptError = transcriptErrorMessage(err)
		emitASRLog(opts, asrLogEvent("error", "transcribe", attachment.TranscriptError, *record, index, *attachment))
	}
}

func (s *Session) saveAttachmentFile(
	ctx context.Context,
	record *harvest.MessageRecord,
	index int,
	location tg.InputFileLocationClass,
	fileName string,
	mediaDir string,
	overwrite bool,
	stageTiming stages.Observer,
	downloadTiming stages.DownloadTransferObserver,
	downloadQueueTiming stages.DownloadQueueWaitObserver,
) {
	plan := newMediaDownloadPlan()
	s.planSavedAttachment(record, index, location, fileName, mediaDir, overwrite, downloadTiming, plan)
	coordinator := newDownloadCoordinator(nil, func(ctx context.Context) error {
		return s.reserveDownloadWave(ctx, downloadQueueTiming)
	})
	_ = coordinator.runBatch(ctx, plan.tasks, stageTiming)
	coordinator.finish()
}
