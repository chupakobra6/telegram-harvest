package harvest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/runlock"
)

// The public LastID is a verified lower boundary, not the newest fetched ID.
// Pages go to a private resumable stage. Only a complete interval is published.
type incrementalStage struct {
	Chat       string           `json:"chat"`
	TopicID    int              `json:"topic_id"`
	MinID      int              `json:"min_id"`
	Start      time.Time        `json:"start"`
	Added      int              `json:"added"`
	Base       SyncState        `json:"base"`
	OffsetID   int              `json:"offset_id"`
	Bytes      int64            `json:"bytes"`
	Stats      HistoryStats     `json:"stats"`
	ResultChat Chat             `json:"result_chat"`
	Ready      bool             `json:"ready"`
	Outputs    map[string]int64 `json:"outputs,omitempty"`
	Next       *SyncState       `json:"next,omitempty"`
}

func runIncrementalSync(ctx context.Context, source HistorySource, opts SyncOptions, state SyncState, stale bool, now func() time.Time) (SyncResult, error) {
	paths := []string{opts.StreamPath}
	if opts.MergedPath != "" {
		paths = append(paths, opts.MergedPath)
	}
	journalPath, stagePath := opts.StatePath+".incremental.json", opts.StatePath+".incremental.jsonl"
	if err := os.MkdirAll(filepath.Dir(opts.StatePath), 0o700); err != nil {
		return SyncResult{}, err
	}
	journal := incrementalStage{Chat: opts.Chat, TopicID: opts.History.TopicID, MinID: opts.History.MinID, Start: opts.History.Start, Base: state}
	data, err := os.ReadFile(journalPath)
	exists := err == nil
	if exists {
		if err := json.Unmarshal(data, &journal); err != nil {
			return SyncResult{}, fmt.Errorf("invalid incremental journal: %w", err)
		}
		if journal.Outputs != nil {
			// Check before committed-journal cleanup too: only the outputs locked
			// by this call may be read or have publication markers removed.
			if err := matchIncrementalOutputs(&journal, paths); err != nil {
				return SyncResult{}, err
			}
		}
		// A routine sync first resumes an interrupted reconciliation before any
		// fresh scan. An explicit different range must not repurpose its stage.
		if opts.History.Start.IsZero() && !journal.Start.IsZero() {
			opts.History.Start, opts.History.MinID = journal.Start, journal.MinID
		}
		if journal.Chat != opts.Chat || journal.TopicID != opts.History.TopicID || !journal.Start.Equal(opts.History.Start) {
			return SyncResult{}, fmt.Errorf("incremental journal belongs to a different chat/topic")
		}
		if journal.Next != nil && equalSyncState(state, *journal.Next) {
			// A crash after checkpoint publication needs cleanup, not replay.
			if err := verifyIncrementalOutputs(journal, true); err != nil {
				return SyncResult{}, err
			}
			for path := range journal.Outputs {
				if err := checkPublicationOwner(path, opts.StatePath); err != nil {
					return SyncResult{}, err
				}
				if err := finishPublication(path); err != nil {
					return SyncResult{}, err
				}
			}
			if err := removeIncrementalStage(journalPath, stagePath); err != nil {
				return SyncResult{}, err
			}
			return incrementalResult(opts, state, journal), nil
		}
		if !equalSyncState(state, journal.Base) || journal.MinID != opts.History.MinID {
			return SyncResult{}, fmt.Errorf("incremental checkpoint changed while a range was staged")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return SyncResult{}, err
	}
	stage, err := os.OpenFile(stagePath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return SyncResult{}, err
	}
	defer stage.Close()
	info, err := stage.Stat()
	if err != nil {
		return SyncResult{}, err
	}
	if journal.Bytes < 0 || info.Size() < journal.Bytes {
		return SyncResult{}, fmt.Errorf("incremental stage shorter than durable checkpoint")
	}
	// Discard only the uncommitted tail of this tool-owned staging file.
	if err := stage.Truncate(journal.Bytes); err != nil {
		return SyncResult{}, err
	}
	if _, err := stage.Seek(journal.Bytes, io.SeekStart); err != nil {
		return SyncResult{}, err
	}
	if !exists {
		if err := saveIncrementalStage(journalPath, journal); err != nil {
			return SyncResult{}, err
		}
	}
	if !journal.Ready {
		known := map[int]string{}
		if !opts.History.Start.IsZero() {
			var err error
			known, err = knownMessageContents(opts.StreamPath, opts.History.Start)
			if err != nil {
				return SyncResult{}, err
			}
		}
		history := opts.History
		history.All, history.Limit, history.MaxBatches = true, 0, 0
		history.StartOffsetID = journal.OffsetID
		base := journal.Stats
		baseAdded := journal.Added
		encoder := json.NewEncoder(stage)
		count := 0
		added := 0
		lastID := base.LastID
		seen := make(map[int]struct{})
		previousProgress := history.Progress
		history.Progress = func(progress HistoryProgress) error {
			if progress.Records != count {
				return fmt.Errorf("history emitted count differs from page checkpoint")
			}
			if err := stage.Sync(); err != nil {
				return err
			}
			position, err := stage.Seek(0, io.SeekCurrent)
			if err != nil {
				return err
			}
			journal.Bytes, journal.OffsetID = position, progress.NextOffsetID
			journal.Added = baseAdded + added
			journal.Stats = combineIncrementalStats(base, HistoryStats{Records: progress.Records, FirstID: progress.FirstID, LastID: progress.LastID, Batches: progress.Batches, FloodWaits: progress.FloodWaits})
			if err := saveIncrementalStage(journalPath, journal); err != nil {
				return err
			}
			clear(seen)
			if previousProgress != nil {
				return previousProgress(progress)
			}
			return nil
		}
		chat, stats, err := source.DumpHistory(ctx, opts.Chat, history, func(record MessageRecord) error {
			if record.MessageID <= journal.MinID || (journal.OffsetID > 0 && record.MessageID >= journal.OffsetID) {
				return fmt.Errorf("message %d outside incremental interval", record.MessageID)
			}
			if _, duplicate := seen[record.MessageID]; duplicate {
				return fmt.Errorf("incremental history returned duplicate message %d", record.MessageID)
			}
			seen[record.MessageID] = struct{}{}
			count++
			if record.MessageID > lastID {
				lastID = record.MessageID
			}
			if old, exists := known[record.MessageID]; exists {
				if old == messageContent(record) {
					return nil
				}
				record.Revision = true
			}
			if !opts.History.Start.IsZero() && record.MessageID <= state.LastID {
				record.Revision = true
			}
			if err := encoder.Encode(record); err != nil {
				return err
			}
			added++
			return nil
		})
		if err != nil {
			return SyncResult{}, err
		}
		if !stats.Complete || stats.Records != count || (count > 0 && stats.LastID > lastID) {
			return SyncResult{}, fmt.Errorf("incremental range incomplete: emitted=%d reported=%d complete=%t", count, stats.Records, stats.Complete)
		}
		if err := stage.Sync(); err != nil {
			return SyncResult{}, err
		}
		journal.Bytes, err = stage.Seek(0, io.SeekCurrent)
		if err != nil {
			return SyncResult{}, err
		}
		journal.Stats, journal.ResultChat, journal.Ready = combineIncrementalStats(base, stats), chat, true
		journal.Added = baseAdded + added
		if err := saveIncrementalStage(journalPath, journal); err != nil {
			return SyncResult{}, err
		}
	}
	if journal.Outputs == nil {
		journal.Outputs = make(map[string]int64)
		for _, path := range paths {
			if _, duplicate := journal.Outputs[path]; duplicate {
				return SyncResult{}, fmt.Errorf("stream and merged output must differ")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return SyncResult{}, err
			}
			file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				return SyncResult{}, err
			}
			info, statErr := file.Stat()
			closeErr := file.Close()
			if statErr != nil {
				return SyncResult{}, statErr
			}
			if closeErr != nil {
				return SyncResult{}, closeErr
			}
			journal.Outputs[path] = info.Size()
		}
		state.Chat, state.LastSyncAt = journal.ResultChat, now().UTC()
		if opts.History.TopicID > 0 {
			state.Topic = &Topic{ID: opts.History.TopicID, Title: opts.History.TopicTitle}
		}
		if stale {
			state.LastID = recoveredIncrementalLastID(journal.Stats, state.Chat, journal.MinID)
		} else if journal.Stats.LastID > state.LastID {
			state.LastID = journal.Stats.LastID
		}
		state.Records += journal.Added
		if state.Chat.TopMessageID > 0 && state.LastID > state.Chat.TopMessageID {
			state.Chat.TopMessageID = state.LastID
		}
		journal.Next = &state
		if err := saveIncrementalStage(journalPath, journal); err != nil {
			return SyncResult{}, err
		}
	}
	if len(journal.Outputs) != len(paths) {
		return SyncResult{}, fmt.Errorf("incremental output paths changed")
	}
	for _, path := range paths {
		if _, ok := journal.Outputs[path]; !ok {
			return SyncResult{}, fmt.Errorf("incremental output path changed: %s", path)
		}
	}
	if err := verifyIncrementalOutputs(journal, false); err != nil {
		return SyncResult{}, err
	}
	if err := beginPublication(opts.StatePath, paths...); err != nil {
		return SyncResult{}, err
	}
	for path, size := range journal.Outputs {
		file, err := os.Open(path)
		if err != nil {
			return SyncResult{}, err
		}
		info, statErr := file.Stat()
		if statErr != nil {
			file.Close()
			return SyncResult{}, statErr
		}
		// Refuse to overwrite another chat's append, even if its size fits.
		actual := io.NewSectionReader(file, size, info.Size()-size)
		expected := io.NewSectionReader(stage, 0, info.Size()-size)
		left, right := make([]byte, 32*1024), make([]byte, 32*1024)
		for {
			n, readErr := actual.Read(left)
			if n > 0 {
				if _, err := io.ReadFull(expected, right[:n]); err != nil || !bytes.Equal(left[:n], right[:n]) {
					file.Close()
					return SyncResult{}, fmt.Errorf("incremental output suffix belongs to another writer: %s", path)
				}
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				file.Close()
				return SyncResult{}, readErr
			}
		}
		if err := file.Close(); err != nil {
			return SyncResult{}, err
		}
	}
	for _, path := range paths {
		file, err := os.OpenFile(path, os.O_WRONLY, 0o600)
		if err != nil {
			return SyncResult{}, err
		}
		// Replay the staged suffix, including recovery from a partial append.
		_, err = file.Seek(journal.Outputs[path], io.SeekStart)
		if err == nil {
			_, err = stage.Seek(0, io.SeekStart)
		}
		if err == nil {
			_, err = io.Copy(file, stage)
		}
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			return SyncResult{}, err
		}
		if closeErr != nil {
			return SyncResult{}, closeErr
		}
	}
	if journal.Next == nil {
		return SyncResult{}, fmt.Errorf("incremental publication has no checkpoint")
	}
	if err := SaveSyncState(opts.StatePath, *journal.Next); err != nil {
		return SyncResult{}, err
	}
	if err := finishPublication(paths...); err != nil {
		return SyncResult{}, err
	}
	if err := removeIncrementalStage(journalPath, stagePath); err != nil {
		return SyncResult{}, err
	}
	return incrementalResult(opts, *journal.Next, journal), nil
}

func matchIncrementalOutputs(journal *incrementalStage, paths []string) error {
	if len(journal.Outputs) != len(paths) {
		return fmt.Errorf("incremental output paths changed")
	}
	canonical := make(map[string]int64, len(paths))
	for path, size := range journal.Outputs {
		key, err := runlock.CanonicalPath(path)
		if err != nil {
			return err
		}
		canonical[key] = size
	}
	matched := make(map[string]int64, len(paths))
	for _, path := range paths {
		key, err := runlock.CanonicalPath(path)
		if err != nil {
			return err
		}
		size, ok := canonical[key]
		if !ok {
			return fmt.Errorf("incremental output path changed: %s", path)
		}
		matched[path] = size
		delete(canonical, key)
	}
	journal.Outputs = matched
	return nil
}

func incrementalResult(opts SyncOptions, state SyncState, journal incrementalStage) SyncResult {
	stats := journal.Stats
	stats.Records = journal.Added
	return SyncResult{Chat: state.Chat, Topic: state.Topic, State: state, Stats: stats, StreamPath: opts.StreamPath, StatePath: opts.StatePath, MergedPath: opts.MergedPath}
}

func knownMessageContents(path string, start time.Time) (map[int]string, error) {
	known := make(map[int]string)
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return known, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	for {
		var record MessageRecord
		err := decoder.Decode(&record)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if !record.Date.Before(start) {
			known[record.MessageID] = messageContent(record)
		}
	}
	return known, nil
}

func messageContent(record MessageRecord) string {
	record.Chat = Chat{ID: record.Chat.ID, Type: record.Chat.Type}
	record.Sender = Sender{ID: record.Sender.ID, Type: record.Sender.Type}
	if record.Topic != nil {
		record.Topic = &Topic{ID: record.Topic.ID, Title: record.Topic.Title}
	}
	record.EditedAt, record.Revision, record.Views = nil, false, 0
	record.Attachments = append([]Attachment(nil), record.Attachments...)
	for i := range record.Attachments {
		record.Attachments[i].MediaCached = false
		record.Attachments[i].TranscriptCached = false
		record.Attachments[i].TranscriptPath = ""
		record.Attachments[i].TranscriptError = ""
		if record.Attachments[i].Kind == "webpage" {
			record.Attachments[i].Title = ""
		}
		record.Attachments[i].DownloadError = ""
		record.Attachments[i].DownloadHint = ""
	}
	data, _ := json.Marshal(record)
	return string(data)
}

func combineIncrementalStats(base, next HistoryStats) HistoryStats {
	next.Records += base.Records
	next.Batches += base.Batches
	next.FloodWaits += base.FloodWaits
	if base.FirstID > 0 && (next.FirstID == 0 || base.FirstID < next.FirstID) {
		next.FirstID = base.FirstID
	}
	if base.LastID > next.LastID {
		next.LastID = base.LastID
	}
	return next
}

func equalSyncState(a, b SyncState) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func saveIncrementalStage(path string, value incrementalStage) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writePrivateAtomic(path, append(data, '\n'))
}

func verifyIncrementalOutputs(journal incrementalStage, committed bool) error {
	for path, size := range journal.Outputs {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if size < 0 || info.Size() < size || (!committed && info.Size() > size+journal.Bytes) || (committed && info.Size() < size+journal.Bytes) {
			return fmt.Errorf("incremental output changed unexpectedly: %s", path)
		}
	}
	return nil
}

func removeIncrementalStage(journal, stage string) error {
	// Remove the journal first: a leftover stage without a journal is disposable.
	if err := os.Remove(journal); err != nil {
		return err
	}
	if err := os.Remove(stage); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
