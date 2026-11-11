package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chupakobra6/telegram-harvest/internal/config"
	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/chupakobra6/telegram-harvest/internal/transcribe"
)

func TestSyncRejectsInvalidTranscriptionBeforeRuntime(t *testing.T) {
	for _, tc := range []struct {
		profile string
		flags   []string
		want    string
	}{
		{"main", []string{"--retain-transcription-media", "--transcribe"}, "--retain-transcription-media requires --download-media and --transcribe"},
		{"main", []string{"--retain-transcription-media", "--download-media"}, "--retain-transcription-media requires --download-media and --transcribe"},
		{"main", []string{"--transcribe"}, "--transcribe requires --download-media"},
		{"main", []string{"--transcribe-video", "cinema"}, "--transcribe-video must be one of: phone, all, off"},
		{"study", []string{"--download-media", "--transcribe"}, "sync --transcribe is supported only for profile main"},
	} {
		t.Run(tc.profile+"/"+strings.Join(tc.flags, " "), func(t *testing.T) {
			dir := t.TempDir()
			session := filepath.Join(dir, "session.json")
			prefix := "TG_HARVEST_DAILY_"
			if tc.profile == "study" {
				prefix = "TG_HARVEST_STUDY_"
			}
			code, _, stderr := runCommand(t, append([]string{"--profile", tc.profile, "sync", "--chat", "12345", "--name", "example"}, tc.flags...), map[string]string{
				prefix + "STATE_DIR":    dir,
				prefix + "SESSION_PATH": session,
			})
			if code != 1 || !strings.Contains(stderr, tc.want) {
				t.Fatalf("code=%d stderr=%s, want %q", code, stderr, tc.want)
			}
			if _, err := os.Stat(session + ".runtime.lock"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid sync acquired the runtime lock: %v", err)
			}
		})
	}
}

type syncTranscriptionSource func(harvest.HistoryOptions, func(harvest.MessageRecord) error) (harvest.Chat, harvest.HistoryStats, error)

func (source syncTranscriptionSource) DumpHistory(_ context.Context, _ string, opts harvest.HistoryOptions, emit func(harvest.MessageRecord) error) (harvest.Chat, harvest.HistoryStats, error) {
	return source(opts, emit)
}

func TestSyncTranscriptionUsesConfirmedCursor(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{Mode: config.ModeMain, StateDir: dir}
	server := filepath.Join(dir, "whisper-server")
	model := filepath.Join(dir, transcribe.ProductionModelFile)
	gate := filepath.Join(dir, transcribe.ProductionSpeechGateFile)
	t.Setenv("TG_HARVEST_DAILY_WHISPER_COMMAND", server)
	t.Setenv("TG_HARVEST_DAILY_WHISPER_MODEL_PATH", model)
	t.Setenv("TG_HARVEST_DAILY_WHISPER_SPEECH_GATE_MODEL_PATH", gate)
	args := []string{"--chat", "12345", "--name", "example", "--download-media", "--media-dir", "originals", "--transcribe",
		"--retain-transcription-media", "--transcribe-video", "off", "--transcript-dir", "voice", "--max-audio-bytes", "2048"}
	initialState := filepath.Join(dir, "example.state.json")
	if err := harvest.SaveSyncState(initialState, harvest.SyncState{LastID: 10}); err != nil {
		t.Fatal(err)
	}

	nextID, work, wantMinID := 11, 0, 10
	incomplete := false
	chat := harvest.Chat{ID: 12345}
	attachment := harvest.Attachment{Kind: "voice", Transcript: "voice transcript", LocalPath: filepath.Join(dir, "originals", "voice.oga")}
	source := syncTranscriptionSource(func(opts harvest.HistoryOptions, emit func(harvest.MessageRecord) error) (harvest.Chat, harvest.HistoryStats, error) {
		if opts.MinID != wantMinID || !opts.DownloadMedia || !opts.TranscribeMedia || !opts.KeepTranscribedMedia ||
			opts.MediaDir != filepath.Join(dir, "originals") || opts.TranscriptDir != filepath.Join(dir, "voice") ||
			opts.VideoTranscribeMode != harvest.VideoTranscribeOff || opts.MaxAudioBytes != 2048 ||
			opts.WhisperCommand != server || opts.WhisperModelPath != model || opts.WhisperGateFilePath != gate {
			t.Fatalf("wrong archive options at history source: %+v", opts)
		}
		if nextID <= opts.MinID {
			return chat, harvest.HistoryStats{Complete: true}, nil
		}
		work++
		record := harvest.MessageRecord{Chat: chat, MessageID: nextID, Kind: "voice", Attachments: []harvest.Attachment{attachment}}
		return chat, harvest.HistoryStats{Records: 1, FirstID: nextID, LastID: nextID, Complete: !incomplete}, emit(record)
	})
	run := func() (harvest.SyncResult, error) {
		t.Helper()
		opts, err := parseSyncOptions(cfg, args, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		return harvest.RunSync(t.Context(), source, opts)
	}
	result, err := run()
	if err != nil || result.State.LastID != 11 || work != 1 {
		t.Fatalf("first archive pass: result=%+v work=%d err=%v", result, work, err)
	}
	wantMinID = 11
	result, err = run()
	if err != nil || result.Stats.Records != 0 || work != 1 {
		t.Fatalf("repeated archive did attachment work: result=%+v work=%d err=%v", result, work, err)
	}

	nextID, incomplete = 12, true
	if _, err := run(); err == nil || !strings.Contains(err.Error(), "incremental range incomplete") {
		t.Fatalf("incomplete scan returned %v", err)
	}
	state, err := harvest.LoadSyncState(initialState)
	if err != nil || state.LastID != 11 {
		t.Fatalf("incomplete scan advanced cursor: state=%+v err=%v", state, err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "example.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var published harvest.MessageRecord
	if err := json.Unmarshal(body, &published); err != nil {
		t.Fatalf("expected one published record: %s: %v", body, err)
	}
	if published.MessageID != 11 || len(published.Attachments) != 1 ||
		published.Attachments[0].Transcript != attachment.Transcript || published.Attachments[0].LocalPath != attachment.LocalPath {
		t.Fatalf("published record lost the original/transcript: %+v", published)
	}
}

func TestSyncKeepsTranscriptionOptIn(t *testing.T) {
	t.Setenv("TG_HARVEST_DAILY_WHISPER_COMMAND", "whisper-server")
	t.Setenv("TG_HARVEST_DAILY_WHISPER_MODEL_PATH", transcribe.ProductionModelFile)
	t.Setenv("TG_HARVEST_DAILY_WHISPER_SPEECH_GATE_MODEL_PATH", transcribe.ProductionSpeechGateFile)
	for _, mode := range []config.Mode{config.ModeMain, config.ModeStudy} {
		cfg := config.Config{Mode: mode, StateDir: t.TempDir()}
		opts, err := parseSyncOptions(cfg, []string{"--chat", "12345", "--name", "example", "--download-media"}, io.Discard)
		if err != nil || !opts.History.DownloadMedia || opts.History.TranscribeMedia || opts.History.KeepTranscribedMedia {
			t.Fatalf("mode=%s history=%+v err=%v", mode, opts.History, err)
		}
	}
}
