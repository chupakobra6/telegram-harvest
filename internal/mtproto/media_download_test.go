package mtproto

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

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
