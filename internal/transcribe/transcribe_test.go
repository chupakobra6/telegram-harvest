package transcribe

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/stages"
)

func TestRunDetailedObservesFailedFFmpegWork(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake command uses POSIX shell")
	}
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "input.ogg")
	if err := os.WriteFile(inputPath, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	observed := map[stages.Name]time.Duration{}
	_, err := RunDetailed(context.Background(), Options{
		WhisperCommand:   filepath.Join(dir, "unused-whisper-server"),
		WhisperModelPath: filepath.Join(dir, "model.bin"),
		FFmpegCommand:    ffmpegPath,
		StageTiming: func(stage stages.Name, duration time.Duration) {
			observed[stage] += duration
		},
	}, inputPath, filepath.Join(dir, "out.txt"))
	if err == nil {
		t.Fatal("expected ffmpeg error")
	}
	if observed[stages.FFmpeg] <= 0 {
		t.Fatalf("failed ffmpeg timing = %s", observed[stages.FFmpeg])
	}
	if observed[stages.ASR] != 0 {
		t.Fatalf("asr should not run after ffmpeg failure: %s", observed[stages.ASR])
	}
}

func TestSynchronizedBufferKeepsBoundedTail(t *testing.T) {
	buffer := &synchronizedBuffer{}
	first := strings.Repeat("a", 100<<10)
	second := strings.Repeat("b", 100<<10)
	if _, err := buffer.Write([]byte(first)); err != nil {
		t.Fatal(err)
	}
	if _, err := buffer.Write([]byte(second)); err != nil {
		t.Fatal(err)
	}
	got := buffer.String()
	if len(got) != 128<<10 {
		t.Fatalf("buffer bytes = %d, want %d", len(got), 128<<10)
	}
	if !strings.HasSuffix(got, second) {
		t.Fatal("buffer did not retain newest output")
	}
}
