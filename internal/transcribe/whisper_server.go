package transcribe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/stages"
)

const whisperReadyTimeout = 2 * time.Minute

// WhisperServerRunner owns one long-lived whisper-server process. The model is
// loaded exactly once per activated worker and inference remains serialized.
type WhisperServerRunner struct {
	opts Options

	mu        sync.Mutex
	cmd       *exec.Cmd
	baseURL   string
	stderr    *synchronizedBuffer
	stdout    *synchronizedBuffer
	waitDone  chan error
	client    *http.Client
	closed    bool
	processID atomic.Int64
}

type whisperResponse struct {
	Text  string `json:"text"`
	Error string `json:"error"`
}

func (r *WhisperServerRunner) Run(ctx context.Context, inputPath string, outputPath string) (string, error) {
	result, err := r.RunDetailed(ctx, inputPath, outputPath)
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

func (r *WhisperServerRunner) RunDetailed(ctx context.Context, inputPath string, outputPath string) (Result, error) {
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
	wavPath, cleanup, err := convertToASRWAV(ctx, r.opts, inputPath, outputPath)
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
		return Result{}, fmt.Errorf("whisper.cpp session is closed")
	}
	modelColdStartDuration, err := r.startLocked(ctx)
	if modelColdStartDuration > 0 && r.opts.StageTiming != nil {
		r.opts.StageTiming(stages.ModelColdStart, modelColdStartDuration)
	}
	if err != nil {
		return Result{}, err
	}
	asrStart := time.Now()
	defer stages.ObserveSince(r.opts.StageTiming, stages.ASR, asrStart)
	text, err := r.inferLocked(ctx, wavPath)
	if err != nil {
		return Result{}, err
	}
	asrDuration := time.Since(asrStart)
	text = strings.TrimSpace(text)
	if err := os.WriteFile(outputPath, []byte(text), 0o600); err != nil {
		return Result{}, fmt.Errorf("write transcript: %w", err)
	}
	return Result{
		Text:                   text,
		Engine:                 r.opts.EngineName(),
		Backend:                r.opts.Descriptor(),
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

func (r *WhisperServerRunner) startLocked(ctx context.Context) (time.Duration, error) {
	if r.cmd != nil {
		return 0, nil
	}
	startedAt := time.Now()
	if err := r.opts.Validate(); err != nil {
		return time.Since(startedAt), err
	}
	port, err := availableLoopbackPort()
	if err != nil {
		return time.Since(startedAt), err
	}
	threads := r.opts.WhisperThreads
	if threads <= 0 {
		threads = 4
	}
	args := []string{
		"--model", strings.TrimSpace(r.opts.WhisperModelPath),
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--threads", strconv.Itoa(threads),
		"--processors", "1",
		"--language", normalizedLanguage(r.opts.Language),
		"--no-timestamps",
	}
	if normalizedWhisperAccelerator(r.opts.WhisperAccelerator) == AcceleratorCPU {
		args = append(args, "--no-gpu")
	}
	if vadModel := strings.TrimSpace(r.opts.WhisperVADModelPath); vadModel != "" {
		args = append(args, "--vad", "--vad-model", vadModel)
	}
	cmd := exec.Command(strings.TrimSpace(r.opts.WhisperCommand), args...)
	cmd.Env = commandEnvironment(r.opts.Environment)
	stdout := &synchronizedBuffer{}
	stderr := &synchronizedBuffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return time.Since(startedAt), fmt.Errorf("start whisper.cpp server: %w", err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	r.cmd = cmd
	r.processID.Store(int64(cmd.Process.Pid))
	r.stdout = stdout
	r.stderr = stderr
	r.waitDone = waitDone
	r.baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	r.client = &http.Client{Timeout: 0}

	readyCtx, cancel := context.WithTimeout(ctx, whisperReadyTimeout)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, requestErr := http.NewRequestWithContext(readyCtx, http.MethodGet, r.baseURL+"/health", nil)
		if requestErr == nil {
			response, getErr := r.client.Do(request)
			if getErr == nil {
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK {
					if err := r.verifyAccelerationLocked(); err != nil {
						r.stopProcessLocked(true)
						return time.Since(startedAt), err
					}
					return time.Since(startedAt), nil
				}
			}
		}
		select {
		case err := <-waitDone:
			r.waitDone = closedWaitChannel(err)
			r.stopProcessLocked(true)
			return time.Since(startedAt), fmt.Errorf("whisper.cpp server exited before readiness: %v%s", err, r.processDetailLocked())
		case <-readyCtx.Done():
			r.stopProcessLocked(true)
			return time.Since(startedAt), fmt.Errorf("wait for whisper.cpp readiness: %w%s", readyCtx.Err(), r.processDetailLocked())
		case <-ticker.C:
		}
	}
}

func (r *WhisperServerRunner) verifyAccelerationLocked() error {
	evidence := strings.ToLower(r.processOutputLocked())
	switch normalizedWhisperAccelerator(r.opts.WhisperAccelerator) {
	case AcceleratorMetal:
		if !strings.Contains(evidence, "ggml_metal_init: found device") {
			return fmt.Errorf("whisper.cpp did not confirm Metal activation%s", r.processDetailLocked())
		}
	case AcceleratorMetalCoreML:
		if !strings.Contains(evidence, "core ml model loaded") ||
			!strings.Contains(evidence, "ggml_metal_init: found device") {
			return fmt.Errorf("whisper.cpp did not confirm Metal + Core ML activation%s", r.processDetailLocked())
		}
	}
	return nil
}

func (r *WhisperServerRunner) inferLocked(ctx context.Context, wavPath string) (string, error) {
	file, err := os.Open(wavPath)
	if err != nil {
		return "", fmt.Errorf("open whisper.cpp WAV: %w", err)
	}
	bodyReader, bodyWriter := io.Pipe()
	writer := multipart.NewWriter(bodyWriter)
	contentType := writer.FormDataContentType()
	go streamWhisperRequest(writer, bodyWriter, file, wavPath, normalizedLanguage(r.opts.Language))
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/inference", bodyReader)
	if err != nil {
		_ = bodyReader.Close()
		return "", fmt.Errorf("prepare whisper.cpp request: %w", err)
	}
	request.Header.Set("Content-Type", contentType)
	response, err := r.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("whisper.cpp inference: %w%s", err, r.processDetailLocked())
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return "", fmt.Errorf("read whisper.cpp response: %w", err)
	}
	var decoded whisperResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return "", fmt.Errorf("decode whisper.cpp response: %w: %s", err, trimDetail(string(payload)))
	}
	if response.StatusCode != http.StatusOK {
		detail := strings.TrimSpace(decoded.Error)
		if detail == "" {
			detail = trimDetail(string(payload))
		}
		return "", fmt.Errorf("whisper.cpp inference status %s: %s", response.Status, detail)
	}
	if detail := strings.TrimSpace(decoded.Error); detail != "" {
		return "", fmt.Errorf("whisper.cpp inference: %s", detail)
	}
	return decoded.Text, nil
}

func streamWhisperRequest(
	writer *multipart.Writer,
	pipe *io.PipeWriter,
	file *os.File,
	wavPath string,
	language string,
) {
	fail := func(err error) {
		_ = file.Close()
		_ = pipe.CloseWithError(err)
	}
	filePart, err := writer.CreateFormFile("file", filepath.Base(wavPath))
	if err != nil {
		fail(fmt.Errorf("prepare whisper.cpp upload: %w", err))
		return
	}
	if _, err := io.Copy(filePart, file); err != nil {
		fail(fmt.Errorf("read whisper.cpp WAV: %w", err))
		return
	}
	if err := file.Close(); err != nil {
		_ = pipe.CloseWithError(fmt.Errorf("close whisper.cpp WAV: %w", err))
		return
	}
	for key, value := range map[string]string{
		"response_format": "json",
		"language":        language,
		"temperature":     "0.0",
	} {
		if err := writer.WriteField(key, value); err != nil {
			_ = pipe.CloseWithError(fmt.Errorf("prepare whisper.cpp field %s: %w", key, err))
			return
		}
	}
	if err := writer.Close(); err != nil {
		_ = pipe.CloseWithError(fmt.Errorf("finish whisper.cpp upload: %w", err))
		return
	}
	_ = pipe.Close()
}

func (r *WhisperServerRunner) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return r.stopProcessLocked(false)
}

func (r *WhisperServerRunner) ProcessID() int {
	return int(r.processID.Load())
}

func (r *WhisperServerRunner) RuntimeEvidence() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.TrimSpace(r.processOutputLocked())
}

func (r *WhisperServerRunner) stopProcessLocked(force bool) error {
	if r.cmd == nil {
		return nil
	}
	cmd := r.cmd
	waitDone := r.waitDone
	if cmd.Process != nil {
		if force {
			_ = cmd.Process.Kill()
		} else {
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}
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
	r.baseURL = ""
	r.client = nil
	r.waitDone = nil
	r.processID.Store(0)
	if force {
		return nil
	}
	if err != nil && !isExpectedProcessExit(err) {
		return fmt.Errorf("close whisper.cpp server: %w%s", err, r.processDetailLocked())
	}
	return nil
}

func (r *WhisperServerRunner) processDetailLocked() string {
	if detail := trimDetail(r.processOutputLocked()); detail != "" {
		return ": " + detail
	}
	return ""
}

func (r *WhisperServerRunner) processOutputLocked() string {
	var parts []string
	if r.stderr != nil {
		parts = append(parts, r.stderr.String())
	}
	if r.stdout != nil {
		parts = append(parts, r.stdout.String())
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func availableLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve whisper.cpp loopback port: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, fmt.Errorf("release whisper.cpp loopback port: %w", err)
	}
	return port, nil
}

func normalizedLanguage(value string) string {
	if value = strings.ToLower(strings.TrimSpace(value)); value != "" {
		return value
	}
	return "ru"
}

func closedWaitChannel(err error) chan error {
	ch := make(chan error, 1)
	ch <- err
	return ch
}

func isExpectedProcessExit(err error) bool {
	var exitErr *exec.ExitError
	return err == nil || strings.Contains(fmt.Sprint(err), "signal: terminated") ||
		strings.Contains(fmt.Sprint(err), "signal: killed") ||
		(errors.As(err, &exitErr) && exitErr.ExitCode() == 0)
}
