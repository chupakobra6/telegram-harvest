package transcribe

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/stages"
)

const DefaultFFmpegCommand = "ffmpeg"

type Options struct {
	WhisperCommand    string
	WhisperModelPath  string
	WhisperThreads    int
	WhisperDecode     WhisperDecodeOptions
	WhisperSpeechGate WhisperSpeechGateOptions
	Language          string
	Environment       map[string]string
	FFmpegCommand     string
	StageTiming       stages.Observer
	productionProfile bool
}

type ManagedRunner interface {
	Run(ctx context.Context, inputPath string, outputPath string) (string, error)
	RunDetailed(ctx context.Context, inputPath string, outputPath string) (Result, error)
	Close() error
}

type Result struct {
	Text                   string
	Engine                 string
	Backend                Descriptor
	FFmpegDuration         time.Duration
	ModelColdStartDuration time.Duration
	ASRDuration            time.Duration
	SpeechGateDuration     time.Duration
	TotalDuration          time.Duration
	InputBytes             int64
	WAVBytes               int64
	WAVDurationSeconds     float64
	TranscriptBytes        int64
	Diagnostics            *Diagnostics
}

func NewManagedRunner(opts Options) ManagedRunner {
	return &WhisperServerRunner{opts: opts}
}

func (o Options) Configured() bool {
	return o.Validate() == nil
}

func (o Options) EngineName() string {
	return BackendWhisperCPP
}

func Run(ctx context.Context, opts Options, inputPath string, outputPath string) (string, error) {
	result, err := RunDetailed(ctx, opts, inputPath, outputPath)
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

// RunDetailed is the one-shot convenience API. Product pipelines use a
// ManagedRunner so the expensive model remains loaded across attachments.
func RunDetailed(ctx context.Context, opts Options, inputPath string, outputPath string) (Result, error) {
	runner := NewManagedRunner(opts)
	result, runErr := runner.RunDetailed(ctx, inputPath, outputPath)
	return result, joinRunAndCloseErrors(runErr, runner.Close())
}

func joinRunAndCloseErrors(runErr, closeErr error) error {
	if runErr != nil {
		if closeErr != nil {
			return fmt.Errorf("%w; close ASR runner: %v", runErr, closeErr)
		}
		return runErr
	}
	return closeErr
}

func convertToASRWAV(ctx context.Context, opts Options, inputPath string, outputPath string) (string, func(), error) {
	ffmpegCommand := strings.TrimSpace(opts.FFmpegCommand)
	if ffmpegCommand == "" {
		ffmpegCommand = DefaultFFmpegCommand
	}
	wavFile, err := os.CreateTemp(filepath.Dir(outputPath), ".asr-*.wav")
	if err != nil {
		return "", func() {}, fmt.Errorf("create temporary wav: %w", err)
	}
	wavPath := wavFile.Name()
	cleanup := func() {
		_ = os.Remove(wavPath)
	}
	if err := wavFile.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("close temporary wav: %w", err)
	}

	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-i", inputPath,
		"-ac", "1",
		"-ar", "16000",
		"-sample_fmt", "s16",
		wavPath,
	}
	if output, err := exec.CommandContext(ctx, ffmpegCommand, args...).CombinedOutput(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return wavPath, cleanup, nil
}

func commandEnvironment(overrides map[string]string) []string {
	if len(overrides) == 0 {
		return nil
	}
	environment := os.Environ()
	for key, value := range overrides {
		environment = append(environment, key+"="+value)
	}
	return environment
}

type synchronizedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	const maxBytes = 128 << 10
	b.mu.Lock()
	defer b.mu.Unlock()
	written := len(p)
	if len(p) >= maxBytes {
		b.buf.Reset()
		_, _ = b.buf.Write(p[len(p)-maxBytes:])
		return written, nil
	}
	if overflow := b.buf.Len() + len(p) - maxBytes; overflow > 0 {
		current := b.buf.Bytes()
		kept := append([]byte(nil), current[min(overflow, len(current)):]...)
		b.buf.Reset()
		_, _ = b.buf.Write(kept)
	}
	_, _ = b.buf.Write(p)
	return written, nil
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func trimDetail(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	if len(value) <= 500 {
		return value
	}
	return value[:500] + "..."
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func wavPCM16MonoDuration(size int64) float64 {
	const wavHeaderBytes = 44
	const bytesPerSecond = 16000 * 2
	if size <= wavHeaderBytes {
		return 0
	}
	return float64(size-wavHeaderBytes) / bytesPerSecond
}
