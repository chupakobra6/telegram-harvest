package transcribe

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/stages"
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

	observed := map[stages.Name]time.Duration{}
	text, err := Run(context.Background(), Options{
		VoskCommand:   voskPath,
		VoskModelPath: dir,
		FFmpegCommand: ffmpegPath,
		StageTiming: func(stage stages.Name, duration time.Duration) {
			observed[stage] += duration
		},
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
	if observed[stages.FFmpeg] <= 0 {
		t.Fatalf("ffmpeg timing = %s", observed[stages.FFmpeg])
	}
	if observed[stages.Vosk] <= 0 {
		t.Fatalf("vosk timing = %s", observed[stages.Vosk])
	}
}

func TestRunVoskObservesFailedFFmpegWork(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake command uses POSIX shell")
	}
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "input.ogg")
	if err := os.WriteFile(inputPath, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	writeExecutable(t, ffmpegPath, "#!/bin/sh\nexit 7\n")
	observed := map[stages.Name]time.Duration{}
	_, err := RunDetailed(context.Background(), Options{
		VoskCommand:   filepath.Join(dir, "unused-vosk"),
		VoskModelPath: dir,
		FFmpegCommand: ffmpegPath,
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
	if observed[stages.Vosk] != 0 {
		t.Fatalf("vosk should not run after ffmpeg failure: %s", observed[stages.Vosk])
	}
}

func TestManagedVoskRunnerKeepsOneSessionForMultipleFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake commands use POSIX shell")
	}
	dir := t.TempDir()
	ffmpegLog := filepath.Join(dir, "ffmpeg.log")
	workerLog := filepath.Join(dir, "worker.log")
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	voskPath := filepath.Join(dir, "vosk-transcribe")
	writeExecutable(t, ffmpegPath, fmt.Sprintf(`#!/bin/sh
out=""
for arg in "$@"; do out="$arg"; done
printf 'wav' > "$out"
printf 'converted %%s\n' "$out" >> %s
`, shellLiteral(ffmpegLog)))
	writeExecutable(t, voskPath, fmt.Sprintf(`#!/bin/sh
printf 'start %%s\n' "$*" >> %s
if [ "$1" != "--session" ]; then
  printf 'unexpected mode: %%s\n' "$1" >&2
  exit 2
fi
while IFS= read -r line; do
  id=$(printf '%%s\n' "$line" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p')
  printf '{"id":%%s,"text":"текст %%s"}\n' "$id" "$id"
done
`, shellLiteral(workerLog)))
	inputOne := filepath.Join(dir, "one.ogg")
	inputTwo := filepath.Join(dir, "two.ogg")
	outputOne := filepath.Join(dir, "one.txt")
	outputTwo := filepath.Join(dir, "two.txt")
	if err := os.WriteFile(inputOne, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inputTwo, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}

	runner := NewManagedRunner(Options{
		VoskCommand:     voskPath,
		VoskModelPath:   filepath.Join(dir, "model"),
		VoskGrammarPath: filepath.Join(dir, "grammar.json"),
		FFmpegCommand:   ffmpegPath,
	})
	textOne, err := runner.Run(context.Background(), inputOne, outputOne)
	if err != nil {
		t.Fatal(err)
	}
	textTwo, err := runner.Run(context.Background(), inputTwo, outputTwo)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Close(); err != nil {
		t.Fatal(err)
	}
	if textOne != "текст 1" || textTwo != "текст 2" {
		t.Fatalf("texts = %q, %q", textOne, textTwo)
	}
	workerLines := readNonEmptyLines(t, workerLog)
	if len(workerLines) != 1 {
		t.Fatalf("worker starts = %d, lines=%q", len(workerLines), workerLines)
	}
	if want := "start --session " + filepath.Join(dir, "model") + " " + filepath.Join(dir, "grammar.json"); workerLines[0] != want {
		t.Fatalf("worker command = %q, want %q", workerLines[0], want)
	}
	if ffmpegLines := readNonEmptyLines(t, ffmpegLog); len(ffmpegLines) != 2 {
		t.Fatalf("ffmpeg calls = %d, lines=%q", len(ffmpegLines), ffmpegLines)
	}
}

func writeExecutable(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}

func readNonEmptyLines(t *testing.T, path string) []string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	trimmed := strings.TrimSpace(string(content))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func shellLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
