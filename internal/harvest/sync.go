package harvest

import (
	"context"
	"fmt"
	"os"
	"time"
)

type SyncOptions struct {
	Chat       string
	StreamPath string
	StatePath  string
	MergedPath string

	History     HistoryOptions
	Reset       bool
	ResetMerged bool
	Now         func() time.Time
	Progress    func(SyncProgress)
}

type SyncResult struct {
	Chat       Chat
	Topic      *Topic
	Stats      HistoryStats
	State      SyncState
	StreamPath string
	StatePath  string
	MergedPath string
}

type SyncProgress struct {
	History    HistoryProgress
	State      SyncState
	StreamPath string
	StatePath  string
	MergedPath string
}

func RunSync(ctx context.Context, source HistorySource, opts SyncOptions) (SyncResult, error) {
	if source == nil {
		return SyncResult{}, fmt.Errorf("history source is required")
	}
	if opts.Chat == "" {
		return SyncResult{}, fmt.Errorf("chat is required")
	}
	if opts.StreamPath == "" {
		return SyncResult{}, fmt.Errorf("stream path is required")
	}
	if opts.StatePath == "" {
		return SyncResult{}, fmt.Errorf("state path is required")
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}

	state := SyncState{}
	loaded, err := LoadSyncState(opts.StatePath)
	if err != nil {
		return SyncResult{}, err
	}
	if opts.Reset {
		opts.History.MinID = 0
	} else {
		state = loaded
		if opts.History.All {
			if state.Backfill == nil || !state.Backfill.Active {
				return SyncResult{}, fmt.Errorf("full history sync requires --reset unless an incomplete backfill exists")
			}
			opts.History.StartOffsetID = state.Backfill.NextOffsetID
		} else {
			if state.Backfill != nil && state.Backfill.Active {
				return SyncResult{}, fmt.Errorf("incremental sync is blocked by an incomplete full backfill; resume with --all or restart with --all --reset")
			}
			opts.History.MinID = state.LastID
		}
	}
	if opts.History.All && opts.ResetMerged && !opts.Reset {
		return SyncResult{}, fmt.Errorf("--reset-merged is only valid when starting a full sync with --reset")
	}
	if opts.History.All {
		state = initBackfillState(state, opts.History, opts.Reset, now())
	}

	streamEncoder, streamFile, err := OpenJSONL(opts.StreamPath, !opts.Reset)
	if err != nil {
		return SyncResult{}, err
	}
	defer streamFile.Close()

	var mergedEncoder interface{ Encode(any) error }
	var mergedFile *os.File
	if opts.MergedPath != "" {
		mergedEncoder, mergedFile, err = OpenJSONL(opts.MergedPath, !opts.ResetMerged)
		if err != nil {
			return SyncResult{}, err
		}
		defer mergedFile.Close()
	}

	baseBackfillRecords := 0
	baseBackfillBatches := 0
	if state.Backfill != nil && !opts.Reset {
		baseBackfillRecords = state.Backfill.Records
		baseBackfillBatches = state.Backfill.Batches
	}
	originalProgress := opts.History.Progress
	if opts.History.All {
		opts.History.Progress = func(progress HistoryProgress) error {
			if err := syncFiles(streamFile, mergedFile); err != nil {
				return err
			}
			nowValue := now().UTC()
			state.LastSyncAt = nowValue
			state.Backfill = updateBackfillState(state.Backfill, progress, baseBackfillRecords, baseBackfillBatches, nowValue)
			state.Records = state.Backfill.Records
			if state.Backfill.Complete {
				state.LastID = state.Backfill.LatestID
			}
			if err := SaveSyncState(opts.StatePath, state); err != nil {
				return err
			}
			if opts.Progress != nil {
				opts.Progress(SyncProgress{
					History:    progress,
					State:      state,
					StreamPath: opts.StreamPath,
					StatePath:  opts.StatePath,
					MergedPath: opts.MergedPath,
				})
			}
			if originalProgress != nil {
				return originalProgress(progress)
			}
			return nil
		}
	}

	chat, stats, err := source.DumpHistory(ctx, opts.Chat, opts.History, func(record MessageRecord) error {
		if err := streamEncoder.Encode(record); err != nil {
			return err
		}
		if mergedEncoder != nil {
			if err := mergedEncoder.Encode(record); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return SyncResult{}, err
	}

	state.Chat = chat
	if opts.History.TopicID > 0 {
		state.Topic = &Topic{ID: opts.History.TopicID, Title: opts.History.TopicTitle}
	}
	state.LastSyncAt = now().UTC()
	if opts.History.All {
		if state.Backfill == nil || state.Backfill.Batches == baseBackfillBatches {
			state.Backfill = updateBackfillState(state.Backfill, HistoryProgress{
				Records:      stats.Records,
				FirstID:      stats.FirstID,
				LastID:       stats.LastID,
				Batches:      stats.Batches,
				NextOffsetID: stats.FirstID,
				Done:         stats.Complete,
				FloodWaits:   stats.FloodWaits,
			}, baseBackfillRecords, baseBackfillBatches, state.LastSyncAt)
		}
		state.Records = state.Backfill.Records
		if state.Backfill.Complete {
			state.LastID = state.Backfill.LatestID
		}
	} else {
		if stats.LastID > state.LastID || opts.Reset {
			state.LastID = stats.LastID
		}
		if opts.Reset {
			state.Records = stats.Records
		} else {
			state.Records += stats.Records
		}
	}
	if err := SaveSyncState(opts.StatePath, state); err != nil {
		return SyncResult{}, err
	}
	return SyncResult{
		Chat:       chat,
		Topic:      state.Topic,
		Stats:      stats,
		State:      state,
		StreamPath: opts.StreamPath,
		StatePath:  opts.StatePath,
		MergedPath: opts.MergedPath,
	}, nil
}

func initBackfillState(state SyncState, history HistoryOptions, reset bool, now time.Time) SyncState {
	if reset || state.Backfill == nil {
		state.Backfill = &Backfill{
			Active:       true,
			StartedAt:    now.UTC(),
			UpdatedAt:    now.UTC(),
			NextOffsetID: history.StartOffsetID,
		}
		return state
	}
	state.Backfill.Active = true
	state.Backfill.Complete = false
	state.Backfill.CompletedAt = time.Time{}
	state.Backfill.UpdatedAt = now.UTC()
	return state
}

func updateBackfillState(backfill *Backfill, progress HistoryProgress, baseRecords int, baseBatches int, now time.Time) *Backfill {
	if backfill == nil {
		backfill = &Backfill{Active: true, StartedAt: now.UTC()}
	}
	backfill.UpdatedAt = now.UTC()
	backfill.NextOffsetID = progress.NextOffsetID
	backfill.Records = baseRecords + progress.Records
	backfill.Batches = baseBatches + progress.Batches
	if progress.LastID > backfill.LatestID {
		backfill.LatestID = progress.LastID
	}
	if progress.FirstID > 0 {
		backfill.OldestID = progress.FirstID
	}
	backfill.Complete = progress.Done
	backfill.Active = !progress.Done
	if progress.Done {
		backfill.CompletedAt = now.UTC()
	}
	return backfill
}

func syncFiles(files ...*os.File) error {
	for _, file := range files {
		if file == nil {
			continue
		}
		if err := file.Sync(); err != nil {
			return fmt.Errorf("sync output file: %w", err)
		}
	}
	return nil
}
