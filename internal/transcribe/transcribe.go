package transcribe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/stages"
)

const DefaultFFmpegCommand = "ffmpeg"
const VoskSessionFlag = "--session"

type Options struct {
	CommandTemplate string
	VoskCommand     string
	VoskModelPath   string
	VoskGrammarPath string
	FFmpegCommand   string
	StageTiming     stages.Observer
}

type ManagedRunner interface {
	Run(ctx context.Context, inputPath string, outputPath string) (string, error)
	RunDetailed(ctx context.Context, inputPath string, outputPath string) (Result, error)
	Close() error
}

type Result struct {
	Text                   string
	Engine                 string
	FFmpegDuration         time.Duration
	ModelColdStartDuration time.Duration
	ASRDuration            time.Duration
	TotalDuration          time.Duration
	InputBytes             int64
	WAVBytes               int64
	WAVDurationSeconds     float64
	TranscriptBytes        int64
}

func NewManagedRunner(opts Options) ManagedRunner {
	if strings.TrimSpace(opts.CommandTemplate) != "" {
		return standaloneRunner{opts: opts}
	}
	return &VoskSessionRunner{opts: opts}
}

func (o Options) Configured() bool {
	if strings.TrimSpace(o.CommandTemplate) != "" {
		return true
	}
	return strings.TrimSpace(o.VoskCommand) != "" && strings.TrimSpace(o.VoskModelPath) != ""
}

func (o Options) EngineName() string {
	if strings.TrimSpace(o.CommandTemplate) != "" {
		return "command"
	}
	if strings.TrimSpace(o.VoskCommand) != "" || strings.TrimSpace(o.VoskModelPath) != "" {
		return "vosk"
	}
	return ""
}

func Run(ctx context.Context, opts Options, inputPath string, outputPath string) (string, error) {
	result, err := RunDetailed(ctx, opts, inputPath, outputPath)
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

func RunDetailed(ctx context.Context, opts Options, inputPath string, outputPath string) (Result, error) {
	start := time.Now()
	if strings.TrimSpace(inputPath) == "" {
		return Result{}, fmt.Errorf("input path is empty")
	}
	if strings.TrimSpace(outputPath) == "" {
		return Result{}, fmt.Errorf("output path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return Result{}, fmt.Errorf("prepare transcript dir: %w", err)
	}
	inputBytes := fileSize(inputPath)
	if strings.TrimSpace(opts.CommandTemplate) != "" {
		text, err := runCommandTemplate(ctx, opts.CommandTemplate, inputPath, outputPath)
		if err != nil {
			return Result{}, err
		}
		return Result{
			Text:            text,
			Engine:          opts.EngineName(),
			TotalDuration:   time.Since(start),
			InputBytes:      inputBytes,
			TranscriptBytes: int64(len([]byte(text))),
		}, nil
	}
	result, err := runVoskDetailed(ctx, opts, inputPath, outputPath)
	if err != nil {
		return Result{}, err
	}
	result.TotalDuration = time.Since(start)
	result.InputBytes = inputBytes
	return result, nil
}

type standaloneRunner struct {
	opts Options
}

func (r standaloneRunner) Run(ctx context.Context, inputPath string, outputPath string) (string, error) {
	return Run(ctx, r.opts, inputPath, outputPath)
}

func (r standaloneRunner) RunDetailed(ctx context.Context, inputPath string, outputPath string) (Result, error) {
	return RunDetailed(ctx, r.opts, inputPath, outputPath)
}

func (r standaloneRunner) Close() error {
	return nil
}

type VoskSessionRunner struct {
	opts Options

	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   *bufio.Reader
	stderr   *synchronizedBuffer
	waitDone chan error
	nextID   int
	closed   bool
}

type voskSessionRequest struct {
	ID      int    `json:"id"`
	WAVPath string `json:"wav_path"`
}

type voskSessionResponse struct {
	ID    int    `json:"id,omitempty"`
	Ready bool   `json:"ready,omitempty"`
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
}

func (r *VoskSessionRunner) Run(ctx context.Context, inputPath string, outputPath string) (string, error) {
	result, err := r.RunDetailed(ctx, inputPath, outputPath)
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

func (r *VoskSessionRunner) RunDetailed(ctx context.Context, inputPath string, outputPath string) (Result, error) {
	start := time.Now()
	if strings.TrimSpace(inputPath) == "" {
		return Result{}, fmt.Errorf("input path is empty")
	}
	if strings.TrimSpace(outputPath) == "" {
		return Result{}, fmt.Errorf("output path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return Result{}, fmt.Errorf("prepare transcript dir: %w", err)
	}

	ffmpegStart := time.Now()
	wavPath, cleanup, err := convertToVoskWAV(ctx, r.opts, inputPath, outputPath)
	stages.ObserveSince(r.opts.StageTiming, stages.FFmpeg, ffmpegStart)
	if err != nil {
		return Result{}, err
	}
	ffmpegDuration := time.Since(ffmpegStart)
	defer cleanup()
	wavBytes := fileSize(wavPath)

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return Result{}, fmt.Errorf("vosk session is closed")
	}
	modelColdStartDuration, err := r.startLocked(ctx)
	if modelColdStartDuration > 0 && r.opts.StageTiming != nil {
		r.opts.StageTiming(stages.ModelColdStart, modelColdStartDuration)
	}
	if err != nil {
		return Result{}, err
	}
	r.nextID++
	requestID := r.nextID
	asrStart := time.Now()
	defer stages.ObserveSince(r.opts.StageTiming, stages.Vosk, asrStart)
	if err := json.NewEncoder(r.stdin).Encode(voskSessionRequest{
		ID:      requestID,
		WAVPath: wavPath,
	}); err != nil {
		r.stopProcessLocked(true)
		return Result{}, fmt.Errorf("write vosk session request: %w%s", err, r.processDetailLocked())
	}

	response, err := r.readResponseLocked(ctx)
	if err != nil {
		return Result{}, err
	}
	asrDuration := time.Since(asrStart)
	if response.ID != requestID {
		r.stopProcessLocked(true)
		return Result{}, fmt.Errorf("vosk session response id = %d, want %d", response.ID, requestID)
	}
	if detail := strings.TrimSpace(response.Error); detail != "" {
		return Result{}, fmt.Errorf("vosk session: %s", detail)
	}
	text := strings.TrimSpace(response.Text)
	if err := os.WriteFile(outputPath, []byte(text), 0o600); err != nil {
		return Result{}, fmt.Errorf("write transcript: %w", err)
	}
	return Result{
		Text:                   text,
		Engine:                 r.opts.EngineName(),
		FFmpegDuration:         ffmpegDuration,
		ModelColdStartDuration: modelColdStartDuration,
		ASRDuration:            asrDuration,
		TotalDuration:          time.Since(start),
		InputBytes:             fileSize(inputPath),
		WAVBytes:               wavBytes,
		WAVDurationSeconds:     wavPCM16MonoDuration(wavBytes),
		TranscriptBytes:        int64(len([]byte(text))),
	}, nil
}

func (r *VoskSessionRunner) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return r.stopProcessLocked(false)
}

func (r *VoskSessionRunner) ProcessID() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd == nil || r.cmd.Process == nil {
		return 0
	}
	return r.cmd.Process.Pid
}

func runVosk(ctx context.Context, opts Options, inputPath string, outputPath string) (string, error) {
	result, err := runVoskDetailed(ctx, opts, inputPath, outputPath)
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

func runVoskDetailed(ctx context.Context, opts Options, inputPath string, outputPath string) (Result, error) {
	start := time.Now()
	voskCommand := strings.TrimSpace(opts.VoskCommand)
	if voskCommand == "" {
		return Result{}, fmt.Errorf("vosk command is empty")
	}
	modelPath := strings.TrimSpace(opts.VoskModelPath)
	if modelPath == "" {
		return Result{}, fmt.Errorf("vosk model path is empty")
	}

	ffmpegStart := time.Now()
	wavPath, cleanup, err := convertToVoskWAV(ctx, opts, inputPath, outputPath)
	stages.ObserveSince(opts.StageTiming, stages.FFmpeg, ffmpegStart)
	if err != nil {
		return Result{}, err
	}
	ffmpegDuration := time.Since(ffmpegStart)
	defer cleanup()
	wavBytes := fileSize(wavPath)

	voskArgs := []string{modelPath, wavPath}
	if grammarPath := strings.TrimSpace(opts.VoskGrammarPath); grammarPath != "" {
		voskArgs = append(voskArgs, grammarPath)
	}
	cmd := exec.CommandContext(ctx, voskCommand, voskArgs...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	asrStart := time.Now()
	defer stages.ObserveSince(opts.StageTiming, stages.Vosk, asrStart)
	if err := cmd.Run(); err != nil {
		return Result{}, fmt.Errorf("vosk: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	asrDuration := time.Since(asrStart)
	text := strings.TrimSpace(stdout.String())
	if err := os.WriteFile(outputPath, []byte(text), 0o600); err != nil {
		return Result{}, fmt.Errorf("write transcript: %w", err)
	}
	return Result{
		Text:               text,
		Engine:             opts.EngineName(),
		FFmpegDuration:     ffmpegDuration,
		ASRDuration:        asrDuration,
		TotalDuration:      time.Since(start),
		InputBytes:         fileSize(inputPath),
		WAVBytes:           wavBytes,
		WAVDurationSeconds: wavPCM16MonoDuration(wavBytes),
		TranscriptBytes:    int64(len([]byte(text))),
	}, nil
}

func (r *VoskSessionRunner) startLocked(ctx context.Context) (time.Duration, error) {
	if r.cmd != nil {
		return 0, nil
	}
	startedAt := time.Now()
	voskCommand := strings.TrimSpace(r.opts.VoskCommand)
	if voskCommand == "" {
		return time.Since(startedAt), fmt.Errorf("vosk command is empty")
	}
	modelPath := strings.TrimSpace(r.opts.VoskModelPath)
	if modelPath == "" {
		return time.Since(startedAt), fmt.Errorf("vosk model path is empty")
	}
	args := []string{VoskSessionFlag, modelPath}
	if grammarPath := strings.TrimSpace(r.opts.VoskGrammarPath); grammarPath != "" {
		args = append(args, grammarPath)
	}
	cmd := exec.Command(voskCommand, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return time.Since(startedAt), fmt.Errorf("prepare vosk session stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return time.Since(startedAt), fmt.Errorf("prepare vosk session stdout: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return time.Since(startedAt), fmt.Errorf("prepare vosk session stderr: %w", err)
	}
	stderr := &synchronizedBuffer{}
	if err := cmd.Start(); err != nil {
		return time.Since(startedAt), fmt.Errorf("start vosk session: %w", err)
	}
	go func() {
		_, _ = io.Copy(stderr, stderrPipe)
	}()
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()
	r.cmd = cmd
	r.stdin = stdin
	r.stdout = bufio.NewReader(stdout)
	r.stderr = stderr
	r.waitDone = waitDone
	response, err := r.readResponseLocked(ctx)
	coldStartDuration := time.Since(startedAt)
	if err != nil {
		return coldStartDuration, fmt.Errorf("wait for vosk session readiness: %w", err)
	}
	if !response.Ready {
		r.stopProcessLocked(true)
		return coldStartDuration, fmt.Errorf("vosk session did not report readiness")
	}
	return coldStartDuration, nil
}

func (r *VoskSessionRunner) readResponseLocked(ctx context.Context) (voskSessionResponse, error) {
	type lineResult struct {
		line string
		err  error
	}
	result := make(chan lineResult, 1)
	go func() {
		line, err := r.stdout.ReadString('\n')
		result <- lineResult{line: line, err: err}
	}()
	select {
	case <-ctx.Done():
		r.stopProcessLocked(true)
		return voskSessionResponse{}, ctx.Err()
	case read := <-result:
		if read.err != nil && strings.TrimSpace(read.line) == "" {
			r.stopProcessLocked(true)
			return voskSessionResponse{}, fmt.Errorf("read vosk session response: %w%s", read.err, r.processDetailLocked())
		}
		var response voskSessionResponse
		if err := json.Unmarshal([]byte(read.line), &response); err != nil {
			r.stopProcessLocked(true)
			return voskSessionResponse{}, fmt.Errorf("decode vosk session response: %w: %s", err, trimDetail(read.line))
		}
		if read.err != nil && read.err != io.EOF {
			r.stopProcessLocked(true)
			return voskSessionResponse{}, fmt.Errorf("read vosk session response: %w%s", read.err, r.processDetailLocked())
		}
		return response, nil
	}
}

func (r *VoskSessionRunner) stopProcessLocked(kill bool) error {
	if r.cmd == nil {
		return nil
	}
	cmd := r.cmd
	stdin := r.stdin
	waitDone := r.waitDone
	if stdin != nil {
		_ = stdin.Close()
	}
	if kill && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	var err error
	select {
	case err = <-waitDone:
	case <-time.After(2 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		err = <-waitDone
	}
	r.cmd = nil
	r.stdin = nil
	r.stdout = nil
	r.stderr = nil
	r.waitDone = nil
	if kill {
		return nil
	}
	if err != nil {
		return fmt.Errorf("close vosk session: %w", err)
	}
	return nil
}

func (r *VoskSessionRunner) processDetailLocked() string {
	if r.stderr == nil {
		return ""
	}
	if detail := strings.TrimSpace(r.stderr.String()); detail != "" {
		return ": " + trimDetail(detail)
	}
	return ""
}

func convertToVoskWAV(ctx context.Context, opts Options, inputPath string, outputPath string) (string, func(), error) {
	ffmpegCommand := strings.TrimSpace(opts.FFmpegCommand)
	if ffmpegCommand == "" {
		ffmpegCommand = DefaultFFmpegCommand
	}
	wavFile, err := os.CreateTemp(filepath.Dir(outputPath), ".vosk-*.wav")
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

func runCommandTemplate(ctx context.Context, template string, inputPath string, outputPath string) (string, error) {
	outputDir := filepath.Dir(outputPath)
	outputBase := strings.TrimSuffix(outputPath, filepath.Ext(outputPath))
	command := strings.ReplaceAll(template, "{input}", shellQuote(inputPath))
	command = strings.ReplaceAll(command, "{output}", shellQuote(outputPath))
	command = strings.ReplaceAll(command, "{output_dir}", shellQuote(outputDir))
	command = strings.ReplaceAll(command, "{output_base}", shellQuote(outputBase))
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", err, detail)
	}
	text := strings.TrimSpace(string(output))
	if _, statErr := os.Stat(outputPath); os.IsNotExist(statErr) {
		if err := os.WriteFile(outputPath, []byte(text), 0o600); err != nil {
			return "", fmt.Errorf("write transcript: %w", err)
		}
	}
	return text, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

type synchronizedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
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
