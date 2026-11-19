package mtproto

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

type originalMediaTestTranscriber struct {
	input string
	calls int
}

func (r *originalMediaTestTranscriber) Run(_ context.Context, input, _ string) (string, error) {
	r.input = input
	r.calls++
	data, err := os.ReadFile(input)
	if err != nil || string(data) != "voice bytes" {
		return "", fmt.Errorf("unexpected ASR input: %q, %v", data, err)
	}
	return "voice transcript", nil
}

func TestArchiveVoiceUsesOneTelegramTransferAndKeepsItsOriginal(t *testing.T) {
	const original = "voice bytes"
	for _, keep := range []bool{false, true} {
		t.Run(fmt.Sprintf("keep=%t", keep), func(t *testing.T) {
			var transfers atomic.Int32
			wire := telegram.MiddlewareFunc(func(tg.Invoker) telegram.InvokeFunc {
				return func(_ context.Context, input bin.Encoder, output bin.Decoder) error {
					request, ok := input.(*tg.UploadGetFileRequest)
					if !ok || request.Offset != 0 {
						return fmt.Errorf("unexpected RPC: %T", input)
					}
					transfers.Add(1)
					output.(*tg.UploadFileBox).File = &tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: []byte(original)}
					return nil
				}
			})
			session := &Session{client: telegram.NewClient(1, "fixture", telegram.Options{Middlewares: []telegram.Middleware{wire}})}
			runner := &originalMediaTestTranscriber{}
			opts := harvest.HistoryOptions{
				DownloadMedia: true, TranscribeMedia: true, KeepTranscribedMedia: keep,
				MediaDir: t.TempDir(), TranscriptDir: t.TempDir(), Transcriber: runner,
			}
			if keep {
				opts.MediaSenderID = 10
			}
			pipeline, err := newMediaPipeline(t.Context(), opts)
			if err != nil {
				t.Fatal(err)
			}
			defer pipeline.abort()
			location := &tg.InputDocumentFileLocation{ID: 3}
			newRecord := func(id int, sender int64) harvest.MessageRecord {
				return harvest.MessageRecord{Chat: harvest.Chat{ID: 1}, MessageID: id, Sender: harvest.Sender{ID: sender},
					Attachments: []harvest.Attachment{{Kind: "voice", MediaID: "document:3", FileName: "voice.oga", Size: int64(len(original))}}}
			}
			records := []harvest.MessageRecord{newRecord(2, 10)}
			if keep {
				records = append(records, newRecord(3, 10), newRecord(4, 20))
			}
			plan := newMediaDownloadPlan()
			session.planAttachmentTranscription(&records[0], 0, location, "voice.oga", opts, pipeline, plan)
			if keep {
				session.planAttachmentTranscription(&records[1], 0, location, "voice.oga", opts, pipeline, plan)
				otherMedia := &tg.MessageMediaDocument{Voice: true}
				otherMedia.SetDocument(&tg.Document{ID: 4, Size: int64(len(original)), MimeType: "audio/ogg"})
				session.planRecordMediaDownloads(&tg.Message{Media: otherMedia}, &records[2], opts, pipeline, plan)
			}
			coordinator := newDownloadCoordinator(nil, nil)
			defer coordinator.finish()
			if err := coordinator.runBatch(t.Context(), plan.tasks, nil); err != nil {
				t.Fatal(err)
			}
			if err := pipeline.flushAndApply(records); err != nil {
				t.Fatal(err)
			}
			if transfers.Load() != 1 || runner.calls != 1 {
				t.Fatalf("Telegram transfers=%d, ASR calls=%d; want one each", transfers.Load(), runner.calls)
			}
			if _, err := os.Stat(runner.input); !os.IsNotExist(err) {
				t.Fatalf("temporary ASR copy survived: %v", err)
			}
			first := records[0].Attachments[0]
			if first.Transcript != "voice transcript" || first.DownloadError != "" || first.TranscriptError != "" {
				t.Fatalf("unexpected attachment: %+v", first)
			}
			if !keep {
				if first.LocalPath != "" {
					t.Fatalf("default dump retained an original: %s", first.LocalPath)
				}
				// The previous archive consumer downloads the same attachment again.
				session.saveAttachmentFile(t.Context(), &records[0], 0, location, "voice.oga", opts.MediaDir, false, nil, nil, nil)
				first = records[0].Attachments[0]
				if transfers.Load() != 2 {
					t.Fatalf("previous archive path transfers=%d, want two", transfers.Load())
				}
			} else {
				duplicate, other := records[1].Attachments[0], records[2].Attachments[0]
				if duplicate.LocalPath != first.LocalPath || duplicate.Transcript != first.Transcript || other.MediaID != first.MediaID || other.LocalPath != "" || other.Transcript != "" {
					t.Fatalf("duplicate or sender scope lost: duplicate=%+v other=%+v", duplicate, other)
				}
				retry := newRecord(2, 10)
				plan := newMediaDownloadPlan()
				session.planAttachmentTranscription(&retry, 0, location, "voice.oga", opts, pipeline, plan)
				if len(plan.tasks) != 0 || retry.Attachments[0].LocalPath != first.LocalPath || !retry.Attachments[0].TranscriptCached {
					t.Fatalf("cached transcript and original should be reused: %+v", retry.Attachments[0])
				}
				silent := newRecord(5, 10)
				silent.Attachments[0].Kind = "video"
				silent.Attachments[0].NoAudio = true
				plan = newMediaDownloadPlan()
				session.planAttachmentTranscription(&silent, 0, location, "silent.mp4", opts, pipeline, plan)
				if err := coordinator.runBatch(t.Context(), plan.tasks, nil); err != nil {
					t.Fatal(err)
				}
				silentBytes, err := os.ReadFile(silent.Attachments[0].LocalPath)
				if err != nil || string(silentBytes) != original || transfers.Load() != 2 || runner.calls != 1 {
					t.Fatalf("silent video was not saved without ASR: bytes=%q err=%v transfers=%d asr=%d", silentBytes, err, transfers.Load(), runner.calls)
				}
			}
			data, err := os.ReadFile(first.LocalPath)
			if err != nil || string(data) != original {
				t.Fatalf("archived original differs: %q, %v", data, err)
			}
		})
	}
}

func TestRetainedOriginalRetriesFailedTransferWithoutPublishingPartialFile(t *testing.T) {
	const original = "complete media bytes"
	for _, kind := range []string{"voice", "video"} {
		t.Run(kind, func(t *testing.T) {
			opts := harvest.HistoryOptions{DownloadMedia: true, TranscribeMedia: true, KeepTranscribedMedia: true,
				MediaDir: t.TempDir(), TranscriptDir: t.TempDir()}
			newRecord := func() harvest.MessageRecord {
				return harvest.MessageRecord{Chat: harvest.Chat{ID: 1}, MessageID: 2, Attachments: []harvest.Attachment{{
					Kind: kind, MediaID: "document:3", Size: int64(len(original)), NoAudio: kind == "video",
				}}}
			}
			if kind == "voice" {
				record := newRecord()
				path := transcriptCachePath(opts.TranscriptDir, transcribeOptions(opts).CacheIdentity(), record, 0, record.Attachments[0])
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("existing transcript"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			visibleMedia := func() ([]string, error) {
				var files []string
				err := filepath.WalkDir(opts.MediaDir, func(path string, entry fs.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if entry.IsDir() && entry.Name() == ".tmp" {
						return filepath.SkipDir
					}
					if !entry.IsDir() {
						files = append(files, path)
					}
					return nil
				})
				return files, err
			}
			var transfers atomic.Int32
			wire := telegram.MiddlewareFunc(func(tg.Invoker) telegram.InvokeFunc {
				return func(_ context.Context, input bin.Encoder, output bin.Decoder) error {
					if _, ok := input.(*tg.UploadGetFileRequest); !ok {
						return fmt.Errorf("unexpected RPC: %T", input)
					}
					if files, err := visibleMedia(); err != nil || len(files) != 0 {
						t.Errorf("final media visible during transfer: %v, %v", files, err)
					}
					data := original
					if transfers.Add(1) == 1 {
						data = "partial"
					}
					output.(*tg.UploadFileBox).File = &tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: []byte(data)}
					return nil
				}
			})
			session := &Session{client: telegram.NewClient(1, "fixture", telegram.Options{Middlewares: []telegram.Middleware{wire}})}
			coordinator := newDownloadCoordinator(nil, nil)
			defer coordinator.finish()
			fetch := func() harvest.Attachment {
				record := newRecord()
				plan := newMediaDownloadPlan()
				session.planAttachmentTranscription(&record, 0, &tg.InputDocumentFileLocation{ID: 3}, "original.bin", opts, nil, plan)
				if err := coordinator.runBatch(t.Context(), plan.tasks, nil); err != nil {
					t.Fatal(err)
				}
				return record.Attachments[0]
			}
			if failed := fetch(); failed.LocalPath != "" || !strings.Contains(failed.DownloadError, "downloaded media size") {
				t.Fatalf("short transfer was not rejected: %+v", failed)
			}
			if files, err := visibleMedia(); err != nil || len(files) != 0 {
				t.Fatalf("failed download left final media: %v, %v", files, err)
			}
			if files, err := os.ReadDir(filepath.Join(opts.MediaDir, ".tmp")); err != nil || len(files) != 0 {
				t.Fatalf("failed download left temporary media: %v, %v", files, err)
			}
			retry := fetch()
			data, err := os.ReadFile(retry.LocalPath)
			if err != nil || string(data) != original || retry.DownloadError != "" || transfers.Load() != 2 {
				t.Fatalf("retry did not publish complete media: %+v, bytes=%q, err=%v, transfers=%d", retry, data, err, transfers.Load())
			}
		})
	}
}

func TestMediaDownloadPlanRestoresCacheBeforeScheduling(t *testing.T) {
	mediaDir := filepath.Join(t.TempDir(), "media")
	session := &Session{client: &telegram.Client{}}
	attachment := harvest.Attachment{Kind: "photo", MediaID: "photo:1:y", FileName: "photo.jpg", Size: 7}
	identity, ok := mediaCacheIdentity(attachment)
	if !ok {
		t.Fatal("expected cache identity")
	}
	source := filepath.Join(t.TempDir(), "source.jpg")
	if err := os.WriteFile(source, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := session.persistentMediaCache(mediaDir).Store(identity, source, filepath.Join(t.TempDir(), "seed.jpg"), attachment.Size); err != nil {
		t.Fatal(err)
	}
	record := harvest.MessageRecord{Chat: harvest.Chat{ID: 1}, MessageID: 2, Attachments: []harvest.Attachment{attachment}}
	plan := newMediaDownloadPlan()
	session.planSavedAttachment(&record, 0, &tg.InputPhotoFileLocation{}, attachment.FileName, mediaDir, false, nil, plan)

	if len(plan.tasks) != 0 {
		t.Fatalf("cache hit scheduled %d Telegram tasks", len(plan.tasks))
	}
	if !record.Attachments[0].MediaCached {
		t.Fatal("cache hit was not recorded")
	}
	data, err := os.ReadFile(record.Attachments[0].LocalPath)
	if err != nil || string(data) != "content" {
		t.Fatalf("restored data=%q err=%v", data, err)
	}
}

func TestMediaDownloadPlanDeduplicatesColdCacheAndCleansCancellation(t *testing.T) {
	mediaDir := filepath.Join(t.TempDir(), "media")
	session := &Session{client: &telegram.Client{}}
	plan := newMediaDownloadPlan()
	records := []harvest.MessageRecord{
		{Chat: harvest.Chat{ID: 1}, MessageID: 10, Attachments: []harvest.Attachment{{Kind: "photo", MediaID: "photo:1:y", FileName: "a.jpg", Size: 7}}},
		{Chat: harvest.Chat{ID: 2}, MessageID: 20, Attachments: []harvest.Attachment{{Kind: "photo", MediaID: "photo:1:y", FileName: "b.jpg", Size: 7}}},
	}
	for index := range records {
		session.planSavedAttachment(&records[index], 0, &tg.InputPhotoFileLocation{}, records[index].Attachments[0].FileName, mediaDir, false, nil, plan)
	}
	if len(plan.tasks) != 1 || len(plan.savedByKey) != 1 {
		t.Fatalf("tasks=%d groups=%d, want one cold-cache transfer", len(plan.tasks), len(plan.savedByKey))
	}
	var sourcePath string
	for _, group := range plan.savedByKey {
		sourcePath = group.sourcePath
	}
	if _, err := os.Stat(sourcePath); err != nil {
		t.Fatalf("prepared source missing: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	coordinator := newDownloadCoordinator(nil, nil)
	if err := coordinator.runBatch(ctx, plan.tasks, nil); err == nil {
		t.Fatal("expected cancellation")
	}
	if _, err := os.Stat(sourcePath); !os.IsNotExist(err) {
		t.Fatalf("temporary source survived cancellation: %v", err)
	}
	for _, record := range records {
		if !strings.Contains(record.Attachments[0].DownloadError, context.Canceled.Error()) {
			t.Fatalf("attachment = %+v", record.Attachments[0])
		}
	}
}

func TestMediaDownloadPlanCancellationReleasesTranscriptClaim(t *testing.T) {
	harness := &pipelineHarness{}
	pipeline, err := newMediaPipelineWithConfig(t.Context(), harvest.HistoryOptions{}, mediaPipelineConfig{
		QueueCapacity:   1,
		RunnerFactory:   harness.factory(0, 1, 0, false, nil),
		SampleResources: func() mediaPipelineResourceSnapshot { return mediaPipelineResourceSnapshot{} },
		SampleRSS:       func(int) uint64 { return 0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pipeline.abort()
	record := harvest.MessageRecord{
		Chat:      harvest.Chat{ID: 1},
		MessageID: 2,
		Attachments: []harvest.Attachment{{
			Kind: "voice", MediaID: "document:3", FileName: "voice.oga", Size: 512,
		}},
	}
	plan := newMediaDownloadPlan()
	(&Session{client: &telegram.Client{}}).planAttachmentTranscription(
		&record,
		0,
		&tg.InputDocumentFileLocation{},
		"voice.oga",
		harvest.HistoryOptions{TranscribeMedia: true, TranscriptDir: t.TempDir(), MediaDir: t.TempDir(), Transcriber: &pipelineRunnerHarness{}},
		pipeline,
		plan,
	)
	if len(plan.tasks) != 1 {
		t.Fatalf("tasks=%d, want 1", len(plan.tasks))
	}
	key := record.Attachments[0].TranscriptPath
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := newDownloadCoordinator(nil, nil).runBatch(ctx, plan.tasks, nil); err == nil {
		t.Fatal("expected cancellation")
	}
	if !pipeline.claim(key) {
		t.Fatal("transcript claim survived canceled download")
	}
	pipeline.releaseClaim(key)
}
