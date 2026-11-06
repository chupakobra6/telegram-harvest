package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/config"
	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/chupakobra6/telegram-harvest/internal/mtproto"
	"github.com/chupakobra6/telegram-harvest/internal/runlock"
	"github.com/chupakobra6/telegram-harvest/internal/transcribe"
)

// Collection and narrative delivery have separate positions. Only confirmation
// of an observed chat result advances Through; preparing sources never does.
type recapState struct {
	Version     int          `json:"version"`
	AccountID   int64        `json:"account_id"`
	Scope       string       `json:"scope"`
	Through     time.Time    `json:"confirmed_through,omitempty"`
	DeliveryRef string       `json:"delivery_ref,omitempty"`
	Prepared    *recapBundle `json:"prepared,omitempty"`
}

type recapBundle struct {
	ID              string            `json:"id"`
	From            time.Time         `json:"from"`
	To              time.Time         `json:"to_exclusive"`
	ReusableThrough time.Time         `json:"reusable_through"`
	Options         string            `json:"options"`
	Ready           bool              `json:"ready"`
	Gaps            []string          `json:"gaps,omitempty"`
	Files           map[string]string `json:"files_sha256"`
}

func runDailyRecap(cfg config.Config, client *mtproto.Client, opts dailyOptions, from, to, confirm, delivery string, now time.Time, out io.Writer) (resultErr error) {
	root := filepath.Join(cfg.StateDir, "recap")
	statePath := filepath.Join(root, "state.json")
	lock, err := runlock.AcquireResources(statePath)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, lock.Release()) }()
	state, err := loadRecapState(statePath)
	if err != nil {
		return err
	}
	if confirm != "" {
		if from != "" || to != "" {
			return fmt.Errorf("confirm delivery separately, without --from or --to")
		}
		if err := confirmRecapDelivery(root, &state, confirm, delivery); err != nil {
			return err
		}
		if err := saveRecapState(statePath, state); err != nil {
			return err
		}
		fmt.Fprintf(out, "recap delivered=true confirmed_through=%s\n", state.Through.Format(time.RFC3339Nano))
		return nil
	}
	if delivery != "" {
		return fmt.Errorf("--delivery-ref requires --confirm-recap")
	}
	if !opts.DownloadMedia || !opts.TranscribeMedia {
		return fmt.Errorf("--recap requires download-media and configured transcription; voice/audio/video notes belong in the initial pass")
	}
	if err := validateDailyOptions(opts); err != nil {
		return err
	}
	start, end, err := recapBounds(state, from, to, now)
	if err != nil {
		return err
	}
	timings := newDailyStageTimingCollector("daily-recap", start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano))
	runErr := withRuntimeLock(cfg, func() error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return client.RunAuthorized(ctx, func(ctx context.Context, session *mtproto.Session) error {
			profile, err := session.SelfProfile(ctx)
			if err != nil {
				return err
			}
			scope := dailyDialogCheckpointScopeFingerprint(cfg, opts)
			if state.AccountID != 0 && (state.AccountID != profile.ID || state.Scope != scope) {
				if from == "" {
					return fmt.Errorf("recap account/scope changed; establish an explicit --from boundary for this scope")
				}
				state = recapState{}
			}
			state.Version, state.AccountID, state.Scope = 1, profile.ID, scope
			history := dailyHistoryOptions(cfg, opts)
			history.StageTiming = timings.Observe
			history.DownloadTiming = timings.ObserveDownloadTransfer
			history.DownloadQueueTiming = timings.ObserveDownloadQueueWait
			history.AudioDurationTiming = timings.ObserveAudioDuration
			history.MediaPipelineTiming = timings.ObserveMediaPipeline
			return prepareRecap(ctx, root, &state, session, history, opts, dailyAdditionalSenderIDsByChat(cfg), start, end, recapOptionsFingerprint(opts), timings, out)
		})
	})
	return finishDailyStageTimings(cfg.StateDir, timings, runErr, out)
}

func recapBounds(state recapState, from, to string, now time.Time) (time.Time, time.Time, error) {
	start := state.Through
	if start.IsZero() && state.Prepared != nil {
		start = state.Prepared.From
	}
	var err error
	if from != "" {
		start, err = parseCompactSince(from)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("recap --from must be RFC3339 or YYYY-MM-DD")
		}
	}
	if start.IsZero() {
		return start, time.Time{}, fmt.Errorf("no confirmed recap boundary; read the previous delivered recap and pass its exclusive cutoff as --from, or use an explicitly requested start")
	}
	end := now
	if to != "" {
		end, err = time.Parse(time.RFC3339, to)
		if err != nil {
			return start, end, fmt.Errorf("recap --to must be RFC3339")
		}
	}
	if !end.After(start) || end.After(now) {
		return start, end, fmt.Errorf("recap requires from < to <= request time")
	}
	return start, end, nil
}

func recapOptionsFingerprint(opts dailyOptions) string {
	// Progress and filesystem destinations do not change source content.
	opts.Progress, opts.MediaDir, opts.TranscriptDir = false, "", ""
	identity := transcribe.ProductionOptions(opts.WhisperCommand, opts.WhisperModelPath, opts.WhisperGateFilePath, opts.FFmpegCommand, nil).CacheIdentity()
	data, _ := json.Marshal(struct {
		Version int
		Options dailyOptions
		ASR     string
	}{1, opts, identity})
	return recapHash(data)
}

func recapHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func loadRecapState(path string) (recapState, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return recapState{}, nil
	}
	if err != nil {
		return recapState{}, err
	}
	var state recapState
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("invalid recap state; preserve it and recover the boundary from the delivered chat: %w", err)
	}
	if state.Version != 1 || state.AccountID == 0 || state.Scope == "" {
		return state, fmt.Errorf("invalid recap state identity")
	}
	return state, nil
}

func saveRecapState(path string, state recapState) error {
	temp, file, err := createAtomicOutput(path)
	if err != nil {
		return err
	}
	defer os.Remove(temp)
	defer file.Close()
	if err := json.NewEncoder(file).Encode(state); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

func confirmRecapDelivery(root string, state *recapState, id, delivery string) error {
	bundle := state.Prepared
	if bundle == nil || bundle.ID != id || !bundle.Ready || strings.TrimSpace(delivery) == "" {
		return fmt.Errorf("confirmation requires the current ready recap ID and a reference to its observed delivered narrative")
	}
	if _, err := loadRecapRecords(root, bundle); err != nil {
		return fmt.Errorf("cannot confirm altered/incomplete sources: %w", err)
	}
	if !state.Through.IsZero() && (bundle.From.After(state.Through) || !bundle.To.After(state.Through)) {
		if bundle.To.Equal(state.Through) && delivery == state.DeliveryRef {
			return nil
		}
		return fmt.Errorf("recap confirmation would skip a gap or rewind the delivered boundary")
	}
	state.Through, state.DeliveryRef = bundle.To, strings.TrimSpace(delivery)
	return nil
}

func recapJobs(dir string, start, end time.Time) []dailyJob {
	var jobs []dailyJob
	for cursor := start; cursor.Before(end); {
		boundary := moscowDay(cursor).AddDate(0, 0, 1)
		if boundary.After(end) {
			boundary = end
		}
		date := cursor.In(moscowLocation()).Format("2006-01-02")
		jobs = append(jobs, dailyJob{Date: date, Start: cursor, End: boundary, FullTranscripts: true,
			OutputPath: filepath.Join(dir, date+".jsonl"), MarkdownPath: filepath.Join(dir, date+".md"), ASRLogPath: filepath.Join(dir, date+".asr.jsonl")})
		cursor = boundary
	}
	return jobs
}

func loadRecapRecords(root string, bundle *recapBundle) ([]harvest.MessageRecord, error) {
	if bundle.ID == "" || filepath.Base(bundle.ID) != bundle.ID || !strings.HasPrefix(bundle.ID, "sources-") || !bundle.To.After(bundle.From) {
		return nil, fmt.Errorf("invalid recap source identity")
	}
	var records []harvest.MessageRecord
	if expected, ok := bundle.Files[harvest.DailyLatestCatchupFilename]; ok {
		data, err := os.ReadFile(filepath.Join(root, bundle.ID, harvest.DailyLatestCatchupFilename))
		if err != nil || recapHash(data) != expected {
			return nil, fmt.Errorf("recap handoff missing or changed")
		}
	}
	for _, job := range recapJobs(filepath.Join(root, bundle.ID), bundle.From, bundle.To) {
		for _, path := range []string{job.OutputPath, job.MarkdownPath} {
			data, err := os.ReadFile(path)
			if err != nil || recapHash(data) != bundle.Files[filepath.Base(path)] {
				return nil, fmt.Errorf("recap source missing or changed: %s", filepath.Base(path))
			}
			if path != job.OutputPath {
				continue
			}
			decoder := json.NewDecoder(strings.NewReader(string(data)))
			for {
				var record harvest.MessageRecord
				if err := decoder.Decode(&record); errors.Is(err, io.EOF) {
					break
				} else if err != nil {
					return nil, err
				}
				records = append(records, record)
			}
		}
	}
	return records, nil
}

// The existing range pipeline remains the sole collector. A proven prefix is
// emitted from its private source bundle; Telegram is asked only for the tail.
type recapRangeDumper struct {
	Source  dailyRangeDumper
	Through time.Time
	Cached  []harvest.MessageRecord
}

func (d recapRangeDumper) DumpOutgoingRange(ctx context.Context, opts harvest.OutgoingRangeOptions, emit func(harvest.MessageRecord) error) (harvest.OutgoingStats, error) {
	var cached []harvest.MessageRecord
	for _, record := range d.Cached {
		if !record.Date.Before(opts.Start) && record.Date.Before(opts.End) && record.Date.Before(d.Through) {
			if err := emit(record); err != nil {
				return harvest.OutgoingStats{}, err
			}
			cached = append(cached, record)
		}
	}
	if d.Through.After(opts.Start) {
		opts.Start = d.Through
	}
	if !opts.Start.Before(opts.End) {
		return dailyStatsFromRange(harvest.OutgoingStats{Complete: true}, cached), nil
	}
	start, end, include := opts.Start, opts.End, opts.IncludeRecord
	opts.IncludeRecord = func(record harvest.MessageRecord) bool {
		return !record.Date.Before(start) && record.Date.Before(end) && (include == nil || include(record))
	}
	stats, err := d.Source.DumpOutgoingRange(ctx, opts, emit)
	stats.Records += len(cached)
	return stats, err
}

func prepareRecap(ctx context.Context, root string, state *recapState, source dailyRangeDumper, history harvest.HistoryOptions, opts dailyOptions, additional map[int64][]int64, start, end time.Time, fingerprint string, timings *dailyStageTimingCollector, out io.Writer) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	dumper := recapRangeDumper{Source: source}
	previous := state.Prepared
	if previous != nil && previous.Options == fingerprint && !previous.From.After(start) && previous.ReusableThrough.After(start) {
		if records, err := loadRecapRecords(root, previous); err == nil {
			dumper.Cached, dumper.Through = records, previous.ReusableThrough
		} else {
			fmt.Fprintln(out, "recap cache_reused=false reason=source_changed")
		}
	}
	dir, err := os.MkdirTemp(root, "sources-")
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(dir)
		}
	}()
	jobs := recapJobs(dir, start, end)
	stats, err := runDailyRangeJobs(ctx, dumper, history, opts, jobs, additional, out)
	if timings != nil {
		timings.ObserveOutgoingStats(stats)
	}
	if err != nil {
		return err
	}
	bundle := &recapBundle{ID: filepath.Base(dir), From: start, To: end, Options: fingerprint, Files: map[string]string{}}
	for _, job := range jobs {
		for _, path := range []string{job.OutputPath, job.MarkdownPath} {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			bundle.Files[filepath.Base(path)] = recapHash(data)
		}
	}
	records, err := loadRecapRecords(root, bundle)
	if err != nil {
		return err
	}
	bundle.ReusableThrough = end
	for _, record := range records {
		if record.Date.Before(dumper.Through) {
			continue // Hash-checked prefix includes proof of empty/no-speech transcripts.
		}
		gaps := recapMediaGaps([]harvest.MessageRecord{record})
		bundle.Gaps = append(bundle.Gaps, gaps...)
		if record.Date.Before(bundle.ReusableThrough) && len(gaps) > 0 {
			bundle.ReusableThrough = record.Date
		}
	}
	bundle.Ready = len(bundle.Gaps) == 0
	dates := make([]string, 0, len(jobs))
	for _, job := range jobs {
		dates = append(dates, job.Date)
	}
	mergedPath, err := writeDailyCatchupMarkdown(dir, dates)
	if err != nil {
		return err
	}
	merged, err := os.ReadFile(mergedPath)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("Рекап: [%s, %s). Готовность источников: %t; пробелов медиа: %d. Это источники, не выданный личный рекап.\n\n", start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano), bundle.Ready, len(bundle.Gaps))
	merged = append([]byte(header), merged...)
	if err := os.WriteFile(mergedPath, merged, 0o600); err != nil {
		return err
	}
	bundle.Files[harvest.DailyLatestCatchupFilename] = recapHash(merged)
	state.Prepared = bundle
	if err := saveRecapState(filepath.Join(root, "state.json"), *state); err != nil {
		return err
	}
	keep = true
	// Only the current source bundle is a cache. Remove superseded owned bundles
	// and abandoned builds, never completed daily reports or the shared ASR cache.
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "sources-") && entry.Name() != bundle.ID {
			if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
				fmt.Fprintf(out, "warning recap_cache_cleanup=%s\n", entry.Name())
			}
		}
	}
	fmt.Fprintf(out, "recap ready=%t delivered=false id=%s from=%s to_exclusive=%s sources=%s reused_through=%s gaps=%d\n",
		bundle.Ready, bundle.ID, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano), mergedPath, dumper.Through.Format(time.RFC3339Nano), len(bundle.Gaps))
	for _, gap := range bundle.Gaps {
		fmt.Fprintf(out, "recap gap=%s\n", gap)
	}
	if !bundle.Ready {
		return fmt.Errorf("recap sources have unresolved media gaps; delivered boundary unchanged")
	}
	return nil
}

func recapMediaGaps(records []harvest.MessageRecord) []string {
	var gaps []string
	for _, record := range records {
		for index, attachment := range record.Attachments {
			switch attachment.Kind {
			case "voice", "audio", "video_note", "round_video", "video":
			default:
				continue
			}
			if attachment.NoAudio || attachment.TranscriptError == "skipped: media has no audio stream" {
				continue
			}
			if attachment.Kind == "video" && strings.HasPrefix(attachment.TranscriptError, "skipped:") && attachment.DownloadError == "" {
				continue // Explicit generic-video policy; voice/round video never use it.
			}
			if attachment.DownloadError == "" && attachment.TranscriptError == "" {
				if attachment.Transcript != "" {
					continue
				}
				// An atomically published empty transcript is a valid no-speech result.
				if info, err := os.Stat(attachment.TranscriptPath); err == nil && info.Mode().IsRegular() {
					continue
				}
			}
			gaps = append(gaps, fmt.Sprintf("chat=%d message=%d attachment=%d kind=%s: %s", record.Chat.ID, record.MessageID, index, attachment.Kind, strings.TrimSpace(attachment.DownloadError+" "+attachment.TranscriptError)))
		}
	}
	return gaps
}
