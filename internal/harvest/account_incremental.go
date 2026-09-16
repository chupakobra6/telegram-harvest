package harvest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

const accountIncrementalPendingName = ".incremental-pending.json"

type accountIncrementalPending struct {
	StreamSize int64 `json:"stream_size"`
	DeltaSize  int64 `json:"delta_size"`
	OldLastID  int   `json:"old_last_id"`
}

// Recover an unfinished publication from the previous on-disk format. New
// account increments are collected by the same runSync as other profiles.
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
