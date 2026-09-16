package harvest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const accountIncrementalPendingName = ".incremental-pending.json"

type accountIncrementalPending struct {
	StreamSize int64 `json:"stream_size"`
	DeltaSize  int64 `json:"delta_size"`
	OldLastID  int   `json:"old_last_id"`
}

func runAccountIncremental(ctx context.Context, source HistorySource, chatID string, streamPath string, statePath string, state SyncState) (SyncState, error) {
	chatDir := filepath.Dir(streamPath)
	pendingPath := filepath.Join(chatDir, accountIncrementalPendingName)
	if err := recoverAccountIncremental(pendingPath, streamPath, state); err != nil {
		return state, err
	}
	stage, err := os.CreateTemp(chatDir, ".delta-*.jsonl")
	if err != nil {
		return state, err
	}
	defer os.Remove(stage.Name())
	defer stage.Close()
	if err := stage.Chmod(0o600); err != nil {
		return state, err
	}
	encoder := json.NewEncoder(stage)
	count, maxID := 0, state.LastID
	seen := make(map[int]struct{})
	chat, stats, err := source.DumpHistory(ctx, chatID, HistoryOptions{
		All: true, BatchSize: DefaultAccountBatchSize, MinID: state.LastID,
	}, func(record MessageRecord) error {
		if record.MessageID <= state.LastID {
			return fmt.Errorf("incremental history returned message %d at or below checkpoint %d", record.MessageID, state.LastID)
		}
		if _, exists := seen[record.MessageID]; exists {
			return fmt.Errorf("incremental history returned duplicate message %d", record.MessageID)
		}
		seen[record.MessageID] = struct{}{}
		if err := encoder.Encode(record); err != nil {
			return err
		}
		count++
		if record.MessageID > maxID {
			maxID = record.MessageID
		}
		return nil
	})
	if err != nil {
		return state, err
	}
	if !stats.Complete || stats.Records != count {
		return state, fmt.Errorf("incremental history was not fully scanned: emitted=%d reported=%d complete=%t", count, stats.Records, stats.Complete)
	}
	if err := stage.Sync(); err != nil {
		return state, err
	}
	stageInfo, err := stage.Stat()
	if err != nil {
		return state, err
	}
	if count == 0 {
		state.LastSyncAt = time.Now().UTC()
		if chat.ID != 0 {
			state.Chat = chat
		}
		return state, SaveSyncState(statePath, state)
	}
	streamInfo, err := os.Stat(streamPath)
	if err != nil {
		return state, err
	}
	pending := accountIncrementalPending{StreamSize: streamInfo.Size(), DeltaSize: stageInfo.Size(), OldLastID: state.LastID}
	pendingData, err := json.Marshal(pending)
	if err != nil {
		return state, err
	}
	if err := writePrivateAtomic(pendingPath, append(pendingData, '\n')); err != nil {
		return state, err
	}
	stream, err := os.OpenFile(streamPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return state, err
	}
	if _, err := stage.Seek(0, io.SeekStart); err != nil {
		_ = stream.Close()
		return state, err
	}
	_, copyErr := io.Copy(stream, stage)
	if copyErr == nil {
		copyErr = stream.Sync()
	}
	closeErr := stream.Close()
	if copyErr != nil {
		return state, copyErr
	}
	if closeErr != nil {
		return state, closeErr
	}
	state.Records += count
	state.LastID = maxID
	state.LastSyncAt = time.Now().UTC()
	if chat.ID != 0 {
		state.Chat = chat
	}
	if err := SaveSyncState(statePath, state); err != nil {
		return state, err
	}
	if err := os.Remove(pendingPath); err != nil {
		return state, err
	}
	return state, nil
}

func recoverAccountIncremental(pendingPath string, streamPath string, state SyncState) error {
	data, err := os.ReadFile(pendingPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var pending accountIncrementalPending
	if err := json.Unmarshal(data, &pending); err != nil || pending.StreamSize < 0 || pending.DeltaSize <= 0 || pending.OldLastID < 0 {
		return fmt.Errorf("invalid incremental pending marker; refusing to modify %s", streamPath)
	}
	info, err := os.Stat(streamPath)
	if err != nil {
		return err
	}
	if state.LastID > pending.OldLastID {
		if info.Size() != pending.StreamSize+pending.DeltaSize {
			return fmt.Errorf("committed incremental stream size differs from pending marker: %s", streamPath)
		}
		return os.Remove(pendingPath)
	}
	if state.LastID != pending.OldLastID || info.Size() < pending.StreamSize || info.Size() > pending.StreamSize+pending.DeltaSize {
		return fmt.Errorf("incremental stream and state disagree; refusing to modify %s", streamPath)
	}
	stream, err := os.OpenFile(streamPath, os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := stream.Truncate(pending.StreamSize); err != nil {
		_ = stream.Close()
		return err
	}
	if err := stream.Sync(); err != nil {
		_ = stream.Close()
		return err
	}
	if err := stream.Close(); err != nil {
		return err
	}
	return os.Remove(pendingPath)
}
