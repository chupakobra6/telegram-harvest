package mtproto

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/chupakobra6/telegram-harvest/internal/stages"
	"github.com/chupakobra6/telegram-harvest/internal/transcribe"
)

type pipelineRunnerHarness struct {
	delay         time.Duration
	reportedAudio float64
	reportedASR   time.Duration
	fail          bool
	gate          <-chan struct{}
	active        *atomic.Int32
	peak          *atomic.Int32
	calls         *atomic.Int32
}

func (r *pipelineRunnerHarness) Run(ctx context.Context, inputPath string, outputPath string) (string, error) {
	result, err := r.RunDetailed(ctx, inputPath, outputPath)
	return result.Text, err
}

func (r *pipelineRunnerHarness) RunDetailed(ctx context.Context, inputPath string, _ string) (transcribe.Result, error) {
	r.calls.Add(1)
	active := r.active.Add(1)
	defer r.active.Add(-1)
	for {
		peak := r.peak.Load()
		if active <= peak || r.peak.CompareAndSwap(peak, active) {
			break
		}
	}
	if r.gate != nil {
		select {
		case <-r.gate:
		case <-ctx.Done():
			return transcribe.Result{}, ctx.Err()
		}
	}
	if r.delay > 0 {
		select {
		case <-time.After(r.delay):
		case <-ctx.Done():
			return transcribe.Result{}, ctx.Err()
		}
	}
	if r.fail {
		return transcribe.Result{}, errors.New("synthetic ASR failure")
	}
	text := strings.TrimSuffix(filepath.Base(inputPath), filepath.Ext(inputPath))
	speechGatePassed := true
	return transcribe.Result{
		Text:                   text,
		Engine:                 "fake",
		FFmpegDuration:         time.Millisecond,
		SpeechGateDuration:     2 * time.Millisecond,
		ModelColdStartDuration: 10 * time.Millisecond,
		ASRDuration:            r.reportedASR,
		TotalDuration:          r.delay,
		InputBytes:             localFileSize(inputPath),
		WAVDurationSeconds:     r.reportedAudio,
		TranscriptBytes:        int64(len(text)),
		Diagnostics: &transcribe.Diagnostics{
			Segments:                      2,
			MeanAverageLogProb:            -0.25,
			MaximumNoSpeechProb:           0.1,
			SpeechGatePassed:              &speechGatePassed,
			RemovedTerminalHallucinations: []string{"Продолжение следует..."},
		},
	}, nil
}

func (r *pipelineRunnerHarness) Close() error { return nil }
func (r *pipelineRunnerHarness) ProcessID() int {
	return 0
}

type pipelineHarness struct {
	active atomic.Int32
	peak   atomic.Int32
	calls  atomic.Int32
}

type pipelineErrorRunner struct {
	err error
}

func (r pipelineErrorRunner) Run(context.Context, string, string) (string, error) {
	return "", r.err
}

func (h *pipelineHarness) factory(delay time.Duration, audio float64, asr time.Duration, fail bool, gate <-chan struct{}) func() harvest.Transcriber {
	return func() harvest.Transcriber {
		return &pipelineRunnerHarness{
			delay:         delay,
			reportedAudio: audio,
			reportedASR:   asr,
			fail:          fail,
			gate:          gate,
			active:        &h.active,
			peak:          &h.peak,
			calls:         &h.calls,
		}
	}
}

func TestMediaPipelineSingleWorkerCollectsDeterministicallyAndAtomically(t *testing.T) {
	dir := t.TempDir()
	harness := &pipelineHarness{}
	var observed stages.MediaPipelineMetrics
	var events []harvest.ASRLogEvent
	opts := harvest.HistoryOptions{
		AudioDurationTiming: func(float64) {},
		MediaPipelineTiming: func(metrics stages.MediaPipelineMetrics) { observed = metrics },
		ASRLog:              func(event harvest.ASRLogEvent) error { events = append(events, event); return nil },
	}
	pipeline, err := newMediaPipelineWithConfig(context.Background(), opts, mediaPipelineConfig{
		QueueCapacity:   4,
		RunnerFactory:   harness.factory(40*time.Millisecond, 12, 2*time.Second, false, nil),
		SampleResources: func() mediaPipelineResourceSnapshot { return mediaPipelineResourceSnapshot{} },
		SampleRSS:       func(int) uint64 { return 123 },
		Now:             time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}

	records := make([]harvest.MessageRecord, 3)
	for i, name := range []string{"a.ogg", "b.ogg", "c.ogg"} {
		input := filepath.Join(dir, name)
		if err := os.WriteFile(input, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		transcript := filepath.Join(dir, strings.TrimSuffix(name, ".ogg")+".txt")
		records[i] = harvest.MessageRecord{
			Date:      time.Unix(int64(i+1), 0),
			Chat:      harvest.Chat{ID: 1},
			MessageID: i + 1,
			Attachments: []harvest.Attachment{{
				Kind:           "voice",
				TranscriptPath: transcript,
			}},
		}
		if !pipeline.claim(transcript) {
			t.Fatalf("initial claim %s was rejected", transcript)
		}
		if err := pipeline.enqueue(mediaPipelineJob{
			Key:             transcript,
			InputPath:       input,
			TranscriptPath:  transcript,
			Record:          records[i],
			Attachment:      records[i].Attachments[0],
			AttachmentIndex: 0,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := pipeline.waitAndApply(records); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"a", "b", "c"} {
		if got := records[i].Attachments[0].Transcript; got != want {
			t.Fatalf("record %d transcript = %q, want %q", i, got, want)
		}
		content, err := os.ReadFile(records[i].Attachments[0].TranscriptPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != want {
			t.Fatalf("cache %d = %q, want %q", i, string(content), want)
		}
	}
	if harness.peak.Load() != 1 {
		t.Fatalf("peak concurrency = %d, want 1", harness.peak.Load())
	}
	if observed.WorkersPeak != 1 || observed.WorkersActivated != 1 || observed.JobsCompleted != 3 || observed.QueueCapacity != 4 || observed.AudioSeconds != 36 {
		t.Fatalf("pipeline metrics = %+v", observed)
	}
	if math.Abs(observed.SpeechGateSeconds-0.006) > 1e-9 {
		t.Fatalf("speech-gate seconds = %.3f, want 0.006", observed.SpeechGateSeconds)
	}
	var finalEvent *harvest.ASRLogEvent
	for index := range events {
		if events[index].Action == "transcribed" {
			finalEvent = &events[index]
			break
		}
	}
	if finalEvent == nil || finalEvent.SpeechGatePassed == nil || !*finalEvent.SpeechGatePassed {
		t.Fatalf("transcribed diagnostics event = %+v", finalEvent)
	}
	if finalEvent.ASRSegments != 2 || len(finalEvent.RemovedHallucinations) != 1 {
		t.Fatalf("transcribed diagnostics event = %+v", finalEvent)
	}
	if matches, err := filepath.Glob(filepath.Join(dir, ".transcript-*.tmp")); err != nil || len(matches) != 0 {
		t.Fatalf("temporary transcripts = %v, err=%v", matches, err)
	}
}

func TestMediaPipelineDeduplicatesInflightMedia(t *testing.T) {
	dir := t.TempDir()
	harness := &pipelineHarness{}
	pipeline, err := newMediaPipelineWithConfig(context.Background(), harvest.HistoryOptions{}, mediaPipelineConfig{
		QueueCapacity:   2,
		RunnerFactory:   harness.factory(20*time.Millisecond, 5, time.Second, false, nil),
		SampleResources: func() mediaPipelineResourceSnapshot { return mediaPipelineResourceSnapshot{} },
		SampleRSS:       func(int) uint64 { return 0 },
		Now:             time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, "shared.txt")
	input := filepath.Join(dir, "shared.ogg")
	if err := os.WriteFile(input, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !pipeline.claim(key) || pipeline.claim(key) {
		t.Fatal("in-flight key was not deduplicated")
	}
	record := harvest.MessageRecord{Attachments: []harvest.Attachment{{Kind: "voice", TranscriptPath: key}}}
	if err := pipeline.enqueue(mediaPipelineJob{
		Key: key, InputPath: input, TranscriptPath: key,
		Record: record, Attachment: record.Attachments[0],
	}); err != nil {
		t.Fatal(err)
	}
	records := []harvest.MessageRecord{record, record}
	if err := pipeline.waitAndApply(records); err != nil {
		t.Fatal(err)
	}
	if harness.calls.Load() != 1 {
		t.Fatalf("ASR calls = %d, want 1", harness.calls.Load())
	}
	if records[0].Attachments[0].Transcript != "shared" || records[1].Attachments[0].Transcript != "shared" {
		t.Fatalf("deduplicated transcripts = %+v", records)
	}
	if metrics := pipeline.metrics(); metrics.JobsDeduplicated != 1 {
		t.Fatalf("deduplicated metric = %d", metrics.JobsDeduplicated)
	}
}

func TestMediaPipelineBoundedQueueAppliesBackpressure(t *testing.T) {
	dir := t.TempDir()
	gate := make(chan struct{})
	harness := &pipelineHarness{}
	pipeline, err := newMediaPipelineWithConfig(context.Background(), harvest.HistoryOptions{}, mediaPipelineConfig{
		QueueCapacity:   1,
		RunnerFactory:   harness.factory(0, 1, time.Second, false, gate),
		SampleResources: func() mediaPipelineResourceSnapshot { return mediaPipelineResourceSnapshot{} },
		SampleRSS:       func(int) uint64 { return 0 },
		Now:             time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	makeJob := func(name string) mediaPipelineJob {
		input := filepath.Join(dir, name+".ogg")
		if err := os.WriteFile(input, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		key := filepath.Join(dir, name+".txt")
		if !pipeline.claim(key) {
			t.Fatalf("claim %s failed", key)
		}
		return mediaPipelineJob{Key: key, InputPath: input, TranscriptPath: key}
	}
	if err := pipeline.enqueue(makeJob("one")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for harness.active.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := pipeline.enqueue(makeJob("two")); err != nil {
		t.Fatal(err)
	}
	thirdJob := makeJob("three")
	thirdDone := make(chan error, 1)
	go func() { thirdDone <- pipeline.enqueue(thirdJob) }()
	select {
	case err := <-thirdDone:
		t.Fatalf("third enqueue did not block: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(gate)
	select {
	case err := <-thirdDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("third enqueue remained blocked after worker progressed")
	}
	if err := pipeline.waitAndApply(nil); err != nil {
		t.Fatal(err)
	}
	if pipeline.metrics().QueuePeak != 1 {
		t.Fatalf("queue peak = %d, want 1", pipeline.metrics().QueuePeak)
	}
}

func TestMediaPipelineFailureDoesNotPublishPartialCache(t *testing.T) {
	dir := t.TempDir()
	harness := &pipelineHarness{}
	pipeline, err := newMediaPipelineWithConfig(context.Background(), harvest.HistoryOptions{}, mediaPipelineConfig{
		QueueCapacity:   2,
		RunnerFactory:   harness.factory(0, 1, time.Second, true, nil),
		SampleResources: func() mediaPipelineResourceSnapshot { return mediaPipelineResourceSnapshot{} },
		SampleRSS:       func(int) uint64 { return 0 },
		Now:             time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(dir, "broken.ogg")
	key := filepath.Join(dir, "broken.txt")
	if err := os.WriteFile(input, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	pipeline.claim(key)
	record := harvest.MessageRecord{Attachments: []harvest.Attachment{{Kind: "voice", TranscriptPath: key}}}
	if err := pipeline.enqueue(mediaPipelineJob{
		Key: key, InputPath: input, TranscriptPath: key,
		Record: record, Attachment: record.Attachments[0],
	}); err != nil {
		t.Fatal(err)
	}
	records := []harvest.MessageRecord{record}
	if err := pipeline.waitAndApply(records); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(records[0].Attachments[0].TranscriptError, "synthetic ASR failure") {
		t.Fatalf("transcript error = %q", records[0].Attachments[0].TranscriptError)
	}
	if _, err := os.Stat(key); !os.IsNotExist(err) {
		t.Fatalf("failed transcript cache exists: %v", err)
	}
	if _, err := os.Stat(input); !os.IsNotExist(err) {
		t.Fatalf("temporary input exists: %v", err)
	}
}

func TestMediaPipelineClassifiesNoAudioAsSkip(t *testing.T) {
	dir := t.TempDir()
	var events []harvest.ASRLogEvent
	pipeline, err := newMediaPipelineWithConfig(context.Background(), harvest.HistoryOptions{
		ASRLog: func(event harvest.ASRLogEvent) error {
			events = append(events, event)
			return nil
		},
	}, mediaPipelineConfig{
		QueueCapacity: 2,
		RunnerFactory: func() harvest.Transcriber {
			return pipelineErrorRunner{err: errors.New("Stream map '0:a:0' matches no streams")}
		},
		SampleResources: func() mediaPipelineResourceSnapshot { return mediaPipelineResourceSnapshot{} },
		SampleRSS:       func(int) uint64 { return 0 },
		Now:             time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(dir, "silent.mp4")
	key := filepath.Join(dir, "silent.txt")
	if err := os.WriteFile(input, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	pipeline.claim(key)
	record := harvest.MessageRecord{Attachments: []harvest.Attachment{{Kind: "video", TranscriptPath: key}}}
	if err := pipeline.enqueue(mediaPipelineJob{
		Key: key, InputPath: input, TranscriptPath: key,
		Record: record, Attachment: record.Attachments[0],
	}); err != nil {
		t.Fatal(err)
	}
	records := []harvest.MessageRecord{record}
	if err := pipeline.waitAndApply(records); err != nil {
		t.Fatal(err)
	}
	metrics := pipeline.metrics()
	if metrics.JobsSkipped != 1 || metrics.JobsFailed != 0 || len(metrics.Workers) != 1 || metrics.Workers[0].Skips != 1 || metrics.Workers[0].Failures != 0 {
		t.Fatalf("skip metrics = %+v", metrics)
	}
	if metrics.AudioSeconds != 0 || metrics.Workers[0].AudioSeconds != 0 || metrics.PoolSpeedX != 0 || metrics.WorkerWorkSpeedX != 0 {
		t.Fatalf("skipped media affected audio throughput metrics: %+v", metrics)
	}
	if records[0].Attachments[0].TranscriptError != "skipped: media has no audio stream" {
		t.Fatalf("transcript error = %q", records[0].Attachments[0].TranscriptError)
	}
	if len(events) != 2 || events[1].Action != "skip" || events[1].Reason != "skipped: media has no audio stream" {
		t.Fatalf("ASR events = %+v", events)
	}
}

func TestMediaPipelineUsesOneGPUWorker(t *testing.T) {
	harness := &pipelineHarness{}
	opts := harvest.HistoryOptions{
		TranscribeMedia:    true,
		TranscriberFactory: harness.factory(0, 1, time.Millisecond, false, nil),
	}
	pipeline, err := newMediaPipeline(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	metrics := pipeline.metrics()
	if len(pipeline.workers) != 1 || metrics.WorkersRequested != 1 || metrics.Mode != "single-gpu" {
		t.Fatalf("pipeline worker contract = %+v", metrics)
	}
	pipeline.abort()
}
