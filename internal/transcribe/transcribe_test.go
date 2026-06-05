package transcribe

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRunCommandTemplateWritesStdoutTranscript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell template test uses POSIX shell")
	}
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "input.ogg")
	outputPath := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(inputPath, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}

	text, err := Run(context.Background(), Options{
		CommandTemplate: "printf '%s' 'готово'",
	}, inputPath, outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if text != "готово" {
		t.Fatalf("text = %q", text)
	}
	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "готово" {
		t.Fatalf("output = %q", string(content))
	}
}

func TestRunVoskUsesFFmpegAndVoskCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake commands use POSIX shell")
	}
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "input.mp4")
	outputPath := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(inputPath, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	voskPath := filepath.Join(dir, "vosk-transcribe")
	writeExecutable(t, ffmpegPath, "#!/bin/sh\nout=\"\"\nfor arg in \"$@\"; do out=\"$arg\"; done\nprintf 'wav' > \"$out\"\n")
	writeExecutable(t, voskPath, "#!/bin/sh\nprintf '%s' 'расшифровка'\n")

	text, err := Run(context.Background(), Options{
		VoskCommand:   voskPath,
		VoskModelPath: dir,
		FFmpegCommand: ffmpegPath,
	}, inputPath, outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if text != "расшифровка" {
		t.Fatalf("text = %q", text)
	}
	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "расшифровка" {
		t.Fatalf("output = %q", string(content))
	}
}

func writeExecutable(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}
