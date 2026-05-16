package harvest

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func LoadSyncState(path string) (SyncState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return SyncState{}, nil
		}
		return SyncState{}, fmt.Errorf("read sync state: %w", err)
	}
	var state SyncState
	if err := json.Unmarshal(data, &state); err != nil {
		return SyncState{}, fmt.Errorf("parse sync state: %w", err)
	}
	return state, nil
}

func SaveSyncState(path string, state SyncState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("prepare sync state dir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode sync state: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write sync state: %w", err)
	}
	return nil
}
